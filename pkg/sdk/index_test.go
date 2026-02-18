package vecdex

import (
	"testing"
)

func TestNewIndex_ValidGeo(t *testing.T) {
	// NewIndex only parses schema, doesn't need a real client.
	idx, err := NewIndex[geoPlace](nil, "test-places")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.name != "test-places" {
		t.Errorf("name = %q, want test-places", idx.name)
	}
	if idx.meta.colType != CollectionTypeGeo {
		t.Errorf("colType = %q, want geo", idx.meta.colType)
	}
}

func TestNewIndex_ValidText(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test-docs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	idx, err := NewIndex[geoPlace](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search().
		Near(34.77, 32.42).
		Km(10).
		Where("country", "CY").
		Limit(50)

	if b.lat != 34.77 {
		t.Errorf("lat = %f, want 34.77", b.lat)
	}
	if b.lon != 32.42 {
		t.Errorf("lon = %f, want 32.42", b.lon)
	}
	if b.radiusKm != 10 {
		t.Errorf("radiusKm = %f, want 10", b.radiusKm)
	}
	if b.limit != 50 {
		t.Errorf("limit = %d, want 50", b.limit)
	}
	if len(b.filters) != 1 {
		t.Fatalf("len(filters) = %d, want 1", len(b.filters))
	}
	if b.filters[0].Key != "country" || b.filters[0].Match != "CY" {
		t.Errorf("filter = %+v", b.filters[0])
	}
}

func TestSearchBuilder_TextChaining(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search().
		Query("hello world").
		Mode(ModeSemantic).
		Limit(20)

	if b.query != "hello world" {
		t.Errorf("query = %q, want 'hello world'", b.query)
	}
	if b.mode != ModeSemantic {
		t.Errorf("mode = %q, want semantic", b.mode)
	}
	if b.limit != 20 {
		t.Errorf("limit = %d, want 20", b.limit)
	}
}

func TestSearchBuilder_ToHits_Geo(t *testing.T) {
	idx, err := NewIndex[geoPlace](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search()
	results := []SearchResult{
		{
			ID:       "paphos",
			Score:    1500.0,
			Content:  "Paphos Castle",
			Tags:     map[string]string{"country": "CY"},
			Numerics: map[string]float64{"latitude": 34.75, "longitude": 32.40, "population": 35000},
		},
	}

	hits := b.toHits(results, true)
	if len(hits) != 1 {
		t.Fatalf("len = %d, want 1", len(hits))
	}
	if hits[0].Item.ID != "paphos" {
		t.Errorf("ID = %q, want paphos", hits[0].Item.ID)
	}
	if hits[0].Item.Name != "Paphos Castle" {
		t.Errorf("Name = %q, want Paphos Castle", hits[0].Item.Name)
	}
	if hits[0].Item.Country != "CY" {
		t.Errorf("Country = %q, want CY", hits[0].Item.Country)
	}
	if hits[0].Score != 1500.0 {
		t.Errorf("Score = %f, want 1500", hits[0].Score)
	}
	if hits[0].Distance != 1500.0 {
		t.Errorf("Distance = %f, want 1500 (geo)", hits[0].Distance)
	}
}

func TestSearchBuilder_ToHits_Text(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search()
	results := []SearchResult{
		{
			ID:      "doc-1",
			Score:   0.95,
			Content: "hello world",
			Tags:    map[string]string{"author": "test"},
		},
	}

	hits := b.toHits(results, false)
	if len(hits) != 1 {
		t.Fatalf("len = %d, want 1", len(hits))
	}
	if hits[0].Item.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", hits[0].Item.ID)
	}
	if hits[0].Item.Content != "hello world" {
		t.Errorf("Content = %q, want hello world", hits[0].Item.Content)
	}
	if hits[0].Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", hits[0].Score)
	}
	if hits[0].Distance != 0 {
		t.Errorf("Distance = %f, want 0 (text)", hits[0].Distance)
	}
}

func TestSearchBuilder_ToHits_Empty(t *testing.T) {
	idx, err := NewIndex[geoPlace](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hits := idx.Search().toHits(nil, false)
	if len(hits) != 0 {
		t.Errorf("len = %d, want 0", len(hits))
	}
}
