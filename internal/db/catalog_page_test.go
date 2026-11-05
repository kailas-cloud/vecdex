package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

type partitionCatalogFixture struct {
	rows    map[string][]string
	fetched int
	err     error
}

func (f *partitionCatalogFixture) CatalogIDPages(
	ctx context.Context, keys []string, after string, limit int,
) ([][]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	pages := make([][]string, len(keys))
	for i, key := range keys {
		for _, id := range f.rows[key] {
			if id > after {
				pages[i] = append(pages[i], id)
				if len(pages[i]) == limit {
					break
				}
			}
		}
		f.fetched += len(pages[i])
	}
	return pages, nil
}

func (*partitionCatalogFixture) CatalogCounts(context.Context, []string) ([]int, error) {
	return nil, nil
}

func TestPartitionCatalogPagination(t *testing.T) {
	fixture := &partitionCatalogFixture{rows: map[string][]string{}}
	keys := make([]string, 256)
	for p := range keys {
		keys[p] = fmt.Sprintf("partition-%d", p)
	}
	all := make([]string, 10000)
	for i := range all {
		id := fmt.Sprintf("document-%05d", i)
		all[i] = id
		key := keys[(i*17+3)%len(keys)]
		fixture.rows[key] = append(fixture.rows[key], id)
	}
	for _, size := range []int{1, 7, 20, 100, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var got []string
			after := ""
			for len(got) < len(all) {
				page, err := ReadCatalogPage(context.Background(), fixture, keys, after, size)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					t.Fatal("pagination ended before every document was read")
				}
				got = append(got, page...)
				after = page[len(page)-1]
			}
			if !slices.Equal(got, all) {
				t.Fatal("partition merge skipped, duplicated or reordered documents")
			}
			page, err := ReadCatalogPage(context.Background(), fixture, keys, after, size)
			if err != nil || len(page) != 0 {
				t.Fatalf("end page=%v err=%v", page, err)
			}
		})
	}
}

func TestPartitionCatalogRefillsOnlyConsumedBlocks(t *testing.T) {
	fixture := &partitionCatalogFixture{rows: map[string][]string{}}
	keys := []string{"busy", "later", "empty"}
	for i := range 10000 {
		fixture.rows["busy"] = append(fixture.rows["busy"], fmt.Sprintf("a%05d", i))
		fixture.rows["later"] = append(fixture.rows["later"], fmt.Sprintf("z%05d", i))
	}
	got, err := ReadCatalogPage(context.Background(), fixture, keys, "", 100)
	if err != nil || len(got) != 100 {
		t.Fatalf("page=%v err=%v", got, err)
	}
	if fixture.fetched > 100+len(keys)*catalogBlockSize {
		t.Fatalf("unbounded catalog hydration: %d IDs", fixture.fetched)
	}
	if got[99] != "a00099" {
		t.Fatalf("wrong last ID: %s", got[99])
	}
}

func TestPartitionCatalogFailsClosed(t *testing.T) {
	failure := errors.New("shard unavailable")
	fixture := &partitionCatalogFixture{err: failure}
	page, err := ReadCatalogPage(context.Background(), fixture, []string{"partition"}, "", 20)
	if !errors.Is(err, failure) || page != nil {
		t.Fatalf("page=%v err=%v", page, err)
	}
	fixture.err = nil
	fixture.rows = map[string][]string{"a": {"duplicate"}, "b": {"duplicate"}}
	if page, err := ReadCatalogPage(context.Background(), fixture, []string{"a", "b"}, "", 20); err == nil || page != nil {
		t.Fatal("accepted duplicate source catalogs")
	}
	fixture.rows = map[string][]string{"a": {"z", "a"}}
	if _, err := ReadCatalogPage(context.Background(), fixture, []string{"a"}, "", 20); err == nil {
		t.Fatal("accepted an unsorted catalog")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadCatalogPage(ctx, fixture, []string{"a"}, "", 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
