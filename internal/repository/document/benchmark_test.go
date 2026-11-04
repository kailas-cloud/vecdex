package document

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/redis/rueidis"
)

type catalogCorpus struct {
	store            *valkey.Store
	observer         rueidis.Client
	repo             *Repo
	ctx              context.Context
	name, generation string
	size             int
}

// BenchmarkDocumentCatalogLive measures list/count paths with 1k and 100k real
// parents written through the repository. The old SCAN path is retained only as
// a benchmark baseline. Embedding providers and the search index are excluded.
func BenchmarkDocumentCatalogLive(b *testing.B) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		b.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	observer, err := rueidis.NewClient(
		rueidis.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, DisableCache: true},
	)
	if err != nil {
		b.Fatal(err)
	}
	defer observer.Close()
	for _, size := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("documents=%d", size), func(b *testing.B) {
			corpus := &catalogCorpus{store: store, observer: observer, repo: New(store), size: size,
				name: "catalog-bench-" + rand.Text(), generation: rand.Text()}
			corpus.ctx = domain.WithCollectionGeneration(context.Background(), corpus.generation)
			defer corpus.cleanup(b)
			corpus.populate(b)
			corpus.benchPages(b)
			for _, legacy := range []bool{true, false} {
				b.Run(fmt.Sprintf("count/scan=%t", legacy), func(b *testing.B) { corpus.benchCount(b, legacy) })
			}
		})
	}
}

func (c *catalogCorpus) populate(b *testing.B) {
	b.Helper()
	if err := c.store.HSet(c.ctx, domain.CollectionMetadataKey(c.name), map[string]string{
		"generation": c.generation, "state": "active"}); err != nil {
		b.Fatal(err)
	}
	memoryBefore := c.info(b, "memory")["used_memory"]
	start := time.Now()
	failures := make(chan error, 32)
	var workers sync.WaitGroup
	for worker := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			vector := make([]float32, 256)
			vector[0] = 1
			content := strings.Repeat("representative source text ", 20)
			for i := worker; i < c.size; i += 16 {
				doc := domdoc.Reconstruct(c.id(i), content, map[string]string{"category": "fixture"}, nil, vector, 1)
				if _, err := c.repo.Upsert(c.ctx, c.name, &doc); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		b.Fatal(err)
	}
	elapsed := time.Since(start)
	if count, err := c.repo.Count(c.ctx, c.name); err != nil || count != c.size {
		b.Fatalf("corpus count %d: %v", count, err)
	}
	memoryAfter := c.info(b, "memory")["used_memory"]
	b.Logf(
		"fixture: %d documents, 256 dimensions, 540-byte source, 16 writers; %.2fs, %.0f documents/s; Valkey +%d bytes",
		c.size,
		elapsed.Seconds(),
		float64(c.size)/elapsed.Seconds(),
		memoryAfter-memoryBefore,
	)
}

func (c *catalogCorpus) benchPages(b *testing.B) {
	b.Helper()
	for _, offset := range []int{0, c.size / 2, c.size - 20} {
		for _, legacy := range []bool{true, false} {
			b.Run(
				fmt.Sprintf("page/offset=%d/scan=%t", offset, legacy),
				func(b *testing.B) { c.benchPage(b, offset, legacy) },
			)
		}
	}
}

func (c *catalogCorpus) benchPage(b *testing.B, offset int, legacy bool) {
	b.Helper()
	cursor := ""
	if offset > 0 {
		cursor = encodeDocumentCursor(domain.CollectionDataBase(c.name, c.generation), c.id(offset-1))
	}
	before := c.info(b, "commandstats")
	samples := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		var docs []domdoc.Document
		var err error
		if legacy {
			docs, err = c.legacyPage(offset)
		} else {
			docs, _, err = c.repo.List(c.ctx, c.name, cursor, 20)
		}
		if err != nil || len(docs) != 20 || docs[0].ID() != c.id(offset) {
			b.Fatalf("page mismatch: %d %v", len(docs), err)
		}
		samples = append(samples, time.Since(start))
	}
	b.StopTimer()
	c.report(b, before, samples)
}

func (c *catalogCorpus) legacyPage(offset int) ([]domdoc.Document, error) {
	result, err := c.store.SearchList(c.ctx, domain.GenerationIndexName(c.name, c.generation), "*", offset, 21, nil)
	if err != nil {
		return nil, fmt.Errorf("baseline scan list: %w", err)
	}
	entries := result.Entries
	if len(entries) > 20 {
		entries = entries[:20]
	}
	docs := make([]domdoc.Document, len(entries))
	for i, entry := range entries {
		docs[i] = parseHashFields(
			strings.TrimPrefix(entry.Key, domain.GenerationDocumentPrefix(c.name, c.generation)),
			entry.Fields,
		)
	}
	return docs, nil
}

func (c *catalogCorpus) benchCount(b *testing.B, legacy bool) {
	b.Helper()
	before := c.info(b, "commandstats")
	samples := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		var count int
		var err error
		if legacy {
			count, err = c.store.SearchCount(c.ctx, domain.GenerationIndexName(c.name, c.generation), "*")
		} else {
			count, err = c.repo.Count(c.ctx, c.name)
		}
		if err != nil || count != c.size {
			b.Fatalf("count mismatch %d: %v", count, err)
		}
		samples = append(samples, time.Since(start))
	}
	b.StopTimer()
	c.report(b, before, samples)
}

func (c *catalogCorpus) report(b *testing.B, before map[string]int64, samples []time.Duration) {
	b.Helper()
	after := c.info(b, "commandstats")
	for _, name := range []string{"scan", "hgetall", "zrangebylex", "zcard"} {
		b.ReportMetric(float64(after[name]-before[name])/float64(b.N), strings.ToUpper(name)+"/op")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	b.ReportMetric(float64(samples[int(math.Ceil(float64(len(samples))*0.95))-1])/float64(time.Millisecond), "p95-ms")
	b.ReportMetric(float64(samples[int(math.Ceil(float64(len(samples))*0.99))-1])/float64(time.Millisecond), "p99-ms")
}

func (c *catalogCorpus) info(b *testing.B, section string) map[string]int64 {
	b.Helper()
	value, err := c.observer.Do(c.ctx, c.observer.B().Arbitrary("INFO").Args(section).Build()).ToString()
	if err != nil {
		b.Fatal(err)
	}
	result := make(map[string]int64)
	for _, line := range strings.Split(value, "\n") {
		name, details, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if section == "commandstats" {
			name = strings.TrimPrefix(name, "cmdstat_")
			details = strings.SplitN(strings.TrimPrefix(details, "calls="), ",", 2)[0]
		}
		if number, err := strconv.ParseInt(details, 10, 64); err == nil {
			result[name] = number
		}
	}
	return result
}

func (c *catalogCorpus) id(index int) string { return fmt.Sprintf("doc-%06d", index) }

func (c *catalogCorpus) cleanup(b *testing.B) {
	b.Helper()
	keys, err := c.store.Scan(c.ctx, domain.CollectionDataBase(c.name, c.generation)+":*")
	if err != nil {
		b.Error(err)
		return
	}
	keys = append(keys, domain.CollectionMetadataKey(c.name))
	for start := 0; start < len(keys); start += 256 {
		commands := make([]rueidis.Completed, 0, 256)
		for _, key := range keys[start:min(start+256, len(keys))] {
			commands = append(commands, c.observer.B().Unlink().Key(key).Build())
		}
		for _, reply := range c.observer.DoMulti(c.ctx, commands...) {
			if err := reply.Error(); err != nil {
				b.Error(err)
			}
		}
	}
}
