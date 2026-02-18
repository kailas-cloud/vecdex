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
