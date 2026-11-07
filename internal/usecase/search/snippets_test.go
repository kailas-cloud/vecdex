package search

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

func TestSnippetBudgetPreservesRankAndSourceProjection(t *testing.T) {
	object, err := metadata.Parse([]byte(`{"nested":{"key":"value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{1, 4, 11, 40, 1000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			texts := []string{strings.Repeat("🙂", 20), "я", strings.Repeat("abc", 20)}
			hits := make([]result.Result, len(texts))
			for i, text := range texts {
				hits[i] = result.New(fmt.Sprint(i), 1-float64(i)/10, text, nil, nil, nil).
					WithSource(fmt.Sprintf("version-%d", i), object)
			}
			got := limitSnippets(hits, budget, "")
			if len(got) != len(texts) {
				t.Fatal("text budget removed ranked documents")
			}
			total := 0
			for i, hit := range got {
				total += len(hit.Content())
				if hit.ContentTruncated() != (len(hit.Content()) < len(texts[i])) {
					t.Fatalf("incorrect excerpt truncation flag for %q", hit.ID())
				}
				if !utf8.ValidString(hit.Content()) || !strings.HasPrefix(texts[i], hit.Content()) {
					t.Fatalf("invalid UTF-8 excerpt: %q", hit.Content())
				}
				if hit.ID() != fmt.Sprint(i) || hit.Version() != fmt.Sprintf("version-%d", i) ||
					hit.Score() != 1-float64(i)/10 || !bytes.Equal(hit.Metadata().JSON(), object.JSON()) {
					t.Fatal("excerpt truncation changed rank or source projection")
				}
			}
			if total > budget {
				t.Fatalf("excerpts use %d bytes, budget %d", total, budget)
			}
		})
	}
}

func TestSnippetBudgetRedistributesUnusedBytes(t *testing.T) {
	hits := []result.Result{
		result.New("short", 1, "a", nil, nil, nil),
		result.New("long", .9, "long excerpt", nil, nil, nil),
	}
	got := limitSnippets(hits, 8, "")
	if got[0].Content() != "a" || got[1].Content() != "long ex" {
		t.Fatalf("unused bytes were not offered to the remaining hits: %+v", got)
	}
}

func TestSnippetWindowKeepsAMatchAtTheEnd(t *testing.T) {
	text := strings.Repeat("Начало документа 中文 🚀\n", 2500) + "Final unique tailwordfragment."
	hits := []result.Result{result.New("source", 1, text, nil, nil, nil)}
	got := limitSnippets(hits, 100, "tailwordfragment")
	if !strings.Contains(got[0].Content(), "tailwordfragment") || len(got[0].Content()) > 100 ||
		!utf8.ValidString(got[0].Content()) {
		t.Fatalf("text budget dropped the matching term: %q", got[0].Content())
	}
}

func TestSnippetWindowTranslatesUnicodeCaseOffsets(t *testing.T) {
	text := strings.Repeat("İ", 100) + " КЛЮЧ " + strings.Repeat("🙂", 100)
	for _, query := range []string{"ключ", "найти ключ"} {
		got := matchingExcerpt(text, query, 32)
		if !strings.Contains(got, "КЛЮЧ") || len(got) > 32 || !utf8.ValidString(got) {
			t.Fatalf("Unicode case mapping shifted the excerpt: query=%q excerpt=%q", query, got)
		}
	}
}

func TestSnippetTruncationSurvivesRankingProjection(t *testing.T) {
	hits := []result.Result{
		result.New("long", .9, "long source excerpt", nil, nil, nil),
		result.New("short", .8, "x", nil, nil, nil),
	}
	trimmed := limitSnippets(hits, 8, "")
	fused := fuseRRF(trimmed, nil, 10)
	if len(fused) != 2 || !fused[0].ContentTruncated() || fused[1].ContentTruncated() {
		t.Fatalf("ranking lost excerpt truncation flags: %+v", fused)
	}
	repeated := limitSnippets(fused, 1000, "")
	if !repeated[0].ContentTruncated() {
		t.Fatal("a second budget pass forgot earlier shortening")
	}
}
