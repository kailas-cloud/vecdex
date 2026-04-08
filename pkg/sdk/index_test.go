package vecdex

import "testing"

func TestNewIndex_ValidText(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test-docs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.name != "test-docs" {
		t.Errorf("name = %q, want test-docs", idx.name)
	}
	if idx.meta.colType != CollectionTypeText {
		t.Errorf("colType = %q, want text", idx.meta.colType)
	}
}

func TestNewIndex_InvalidStruct(t *testing.T) {
	_, err := NewIndex[noIDDoc](nil, "bad")
	if err == nil {
		t.Fatal("expected error for struct without id tag")
	}
}

func TestNewIndex_NonStruct(t *testing.T) {
	_, err := NewIndex[int](nil, "bad")
	if err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

func TestSearchBuilder_Chaining(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search().
		Query("hello world").
		Mode(ModeSemantic).
		Where("author", "alice").
		Limit(20)

	if b.query != "hello world" {
		t.Errorf("query = %q, want hello world", b.query)
	}
	if b.mode != ModeSemantic {
		t.Errorf("mode = %q, want semantic", b.mode)
	}
	if b.limit != 20 {
		t.Errorf("limit = %d, want 20", b.limit)
	}
	if len(b.filters) != 1 {
		t.Fatalf("len(filters) = %d, want 1", len(b.filters))
	}
	if b.filters[0].Key != "author" || b.filters[0].Match != "alice" {
		t.Errorf("filter = %+v", b.filters[0])
	}
}

func TestSearchBuilder_ToHits_Text(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := []SearchResult{
		{
			ID:      "doc-1",
			Score:   0.95,
			Content: "hello world",
			Tags:    map[string]string{"author": "test"},
			Numerics: map[string]float64{
				"priority": 42,
			},
		},
	}

	hits, err := idx.Search().toHits(results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len = %d, want 1", len(hits))
	}
	if hits[0].Item.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", hits[0].Item.ID)
	}
	if hits[0].Item.Content != "hello world" {
		t.Errorf("Content = %q, want hello world", hits[0].Item.Content)
	}
	if hits[0].Item.Author != "test" {
		t.Errorf("Author = %q, want test", hits[0].Item.Author)
	}
	if hits[0].Item.Priority != 42 {
		t.Errorf("Priority = %d, want 42", hits[0].Item.Priority)
	}
	if hits[0].Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", hits[0].Score)
	}
}

func TestSearchBuilder_ToHits_Empty(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hits, err := idx.Search().toHits(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("len = %d, want 0", len(hits))
	}
}
