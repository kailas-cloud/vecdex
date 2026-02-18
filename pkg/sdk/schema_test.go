package vecdex

import (
	"testing"
)

type geoPlace struct {
	ID      string  `vecdex:"id,id"`
	Name    string  `vecdex:"name,content"`
	Country string  `vecdex:"country,tag"`
	Lat     float64 `vecdex:"latitude,geo_lat"`
	Lon     float64 `vecdex:"longitude,geo_lon"`
	Pop     int     `vecdex:"population,numeric"`
}

type textDoc struct {
	ID      string `vecdex:"id,id"`
	Content string `vecdex:"body,content"`
	Author  string `vecdex:"author,tag"`
}

type minimalDoc struct {
	ID string `vecdex:"id,id"`
}

func TestParseSchema_GeoPlace(t *testing.T) {
	meta, err := parseSchema[geoPlace]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.colType != CollectionTypeGeo {
		t.Errorf("colType = %q, want %q", meta.colType, CollectionTypeGeo)
	}
	if meta.idIdx != 0 {
		t.Errorf("idIdx = %d, want 0", meta.idIdx)
	}
	if meta.contentIdx != 1 {
		t.Errorf("contentIdx = %d, want 1", meta.contentIdx)
	}
	if meta.geoLatIdx != 3 {
		t.Errorf("geoLatIdx = %d, want 3", meta.geoLatIdx)
	}
	if meta.geoLonIdx != 4 {
		t.Errorf("geoLonIdx = %d, want 4", meta.geoLonIdx)
	}

	// 3 fields: country(tag), latitude(numeric), longitude(numeric), population(numeric)
	if len(meta.fields) != 4 {
		t.Fatalf("len(fields) = %d, want 4", len(meta.fields))
	}
	if meta.fields[0].Name != "country" || meta.fields[0].Type != FieldTag {
		t.Errorf("fields[0] = %+v, want country/tag", meta.fields[0])
	}
}

func TestParseSchema_TextDoc(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.colType != CollectionTypeText {
		t.Errorf("colType = %q, want %q", meta.colType, CollectionTypeText)
	}
	if meta.contentIdx != 1 {
		t.Errorf("contentIdx = %d, want 1", meta.contentIdx)
	}
	if meta.geoLatIdx != -1 {
		t.Errorf("geoLatIdx = %d, want -1", meta.geoLatIdx)
	}
}

func TestParseSchema_MinimalDoc(t *testing.T) {
	meta, err := parseSchema[minimalDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.colType != CollectionTypeText {
		t.Errorf("colType = %q, want %q", meta.colType, CollectionTypeText)
	}
	if meta.idIdx != 0 {
		t.Errorf("idIdx = %d, want 0", meta.idIdx)
	}
}

type noIDDoc struct {
	Name string `vecdex:"name,content"`
}

func TestParseSchema_NoID(t *testing.T) {
	_, err := parseSchema[noIDDoc]()
	if err == nil {
		t.Fatal("expected error for struct without id tag")
	}
}

type duplicateIDDoc struct {
	ID1 string `vecdex:"id1,id"`
	ID2 string `vecdex:"id2,id"`
}

func TestParseSchema_DuplicateID(t *testing.T) {
	_, err := parseSchema[duplicateIDDoc]()
	if err == nil {
		t.Fatal("expected error for duplicate id tag")
	}
}

type geoLatOnly struct {
	ID  string  `vecdex:"id,id"`
	Lat float64 `vecdex:"lat,geo_lat"`
}

func TestParseSchema_GeoLatOnly(t *testing.T) {
	_, err := parseSchema[geoLatOnly]()
	if err == nil {
		t.Fatal("expected error when only geo_lat present")
	}
}

type unknownModifier struct {
	ID   string `vecdex:"id,id"`
	Name string `vecdex:"name,foobar"`
}

func TestParseSchema_UnknownModifier(t *testing.T) {
	_, err := parseSchema[unknownModifier]()
	if err == nil {
		t.Fatal("expected error for unknown modifier")
	}
}

func TestParseSchema_NonStruct(t *testing.T) {
	_, err := parseSchema[string]()
	if err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

type skipFieldDoc struct {
	ID      string `vecdex:"id,id"`
	Ignored string `vecdex:"-"`
	NoTag   string
}

func TestParseSchema_SkipFields(t *testing.T) {
	meta, err := parseSchema[skipFieldDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.fields) != 0 {
		t.Errorf("len(fields) = %d, want 0 (skipped fields should not appear)", len(meta.fields))
	}
}

func TestToDocument_GeoPlace(t *testing.T) {
	meta, err := parseSchema[geoPlace]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	place := geoPlace{
		ID: "test-1", Name: "Test Place", Country: "CY",
		Lat: 34.77, Lon: 32.42, Pop: 50000,
	}

	doc := meta.toDocument(place)

	if doc.ID != "test-1" {
		t.Errorf("ID = %q, want %q", doc.ID, "test-1")
	}
	if doc.Content != "Test Place" {
		t.Errorf("Content = %q, want %q", doc.Content, "Test Place")
	}
	if doc.Tags["country"] != "CY" {
		t.Errorf("Tags[country] = %q, want %q", doc.Tags["country"], "CY")
	}
	if doc.Numerics["latitude"] != 34.77 {
		t.Errorf("Numerics[latitude] = %f, want 34.77", doc.Numerics["latitude"])
	}
	if doc.Numerics["longitude"] != 32.42 {
		t.Errorf("Numerics[longitude] = %f, want 32.42", doc.Numerics["longitude"])
	}
	if doc.Numerics["population"] != 50000 {
		t.Errorf("Numerics[population] = %f, want 50000", doc.Numerics["population"])
	}
}

func TestFromDocument_GeoPlace(t *testing.T) {
	meta, err := parseSchema[geoPlace]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	doc := Document{
		ID:       "test-1",
		Content:  "Test Place",
		Tags:     map[string]string{"country": "CY"},
		Numerics: map[string]float64{"latitude": 34.77, "longitude": 32.42, "population": 50000},
	}

	result := meta.fromDocument(doc)
	place, ok := result.(geoPlace)
	if !ok {
		t.Fatalf("type assertion failed: got %T", result)
	}

	if place.ID != "test-1" {
		t.Errorf("ID = %q, want %q", place.ID, "test-1")
	}
	if place.Name != "Test Place" {
		t.Errorf("Name = %q, want %q", place.Name, "Test Place")
	}
	if place.Country != "CY" {
		t.Errorf("Country = %q, want %q", place.Country, "CY")
	}
	if place.Lat != 34.77 {
		t.Errorf("Lat = %f, want 34.77", place.Lat)
	}
	if place.Lon != 32.42 {
		t.Errorf("Lon = %f, want 32.42", place.Lon)
	}
	if place.Pop != 50000 {
		t.Errorf("Pop = %d, want 50000", place.Pop)
	}
}

func TestToDocument_Roundtrip(t *testing.T) {
	meta, err := parseSchema[geoPlace]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	original := geoPlace{
		ID: "rt-1", Name: "Roundtrip", Country: "GR",
		Lat: 37.97, Lon: 23.72, Pop: 3000000,
	}

	doc := meta.toDocument(original)

	restored, ok := meta.fromDocument(doc).(geoPlace)
	if !ok {
		t.Fatal("type assertion failed")
	}

	if original != restored {
		t.Errorf("roundtrip mismatch:\n  original: %+v\n  restored: %+v", original, restored)
	}
}

func TestCollectionOptions_Geo(t *testing.T) {
	meta, err := parseSchema[geoPlace]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	opts := meta.collectionOptions()
	// 1 type option + 4 field options
	if len(opts) != 5 {
		t.Errorf("len(opts) = %d, want 5", len(opts))
	}

	// Apply options to verify they work.
	cfg := &collectionConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.colType != CollectionTypeGeo {
		t.Errorf("colType = %q, want %q", cfg.colType, CollectionTypeGeo)
	}
	if len(cfg.fields) != 4 {
		t.Errorf("len(fields) = %d, want 4", len(cfg.fields))
	}
}

func TestCollectionOptions_Text(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg := &collectionConfig{}
	for _, o := range meta.collectionOptions() {
		o(cfg)
	}
	if cfg.colType != CollectionTypeText {
		t.Errorf("colType = %q, want %q", cfg.colType, CollectionTypeText)
	}
}

type uintDoc struct {
	ID  string `vecdex:"id,id"`
	Val uint32 `vecdex:"val,numeric"`
}

func TestToDocument_UintField(t *testing.T) {
	meta, err := parseSchema[uintDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	doc := meta.toDocument(uintDoc{ID: "u1", Val: 42})
	if doc.Numerics["val"] != 42 {
		t.Errorf("val = %f, want 42", doc.Numerics["val"])
	}
}

func TestFromDocument_UintField(t *testing.T) {
	meta, err := parseSchema[uintDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	result := meta.fromDocument(Document{
		ID:       "u1",
		Numerics: map[string]float64{"val": 42},
	})

	u, ok := result.(uintDoc)
	if !ok {
		t.Fatalf("type assertion failed: got %T", result)
	}
	if u.Val != 42 {
		t.Errorf("Val = %d, want 42", u.Val)
	}
}

type duplicateContent struct {
	ID string `vecdex:"id,id"`
	A  string `vecdex:"a,content"`
	B  string `vecdex:"b,content"`
}

func TestParseSchema_DuplicateContent(t *testing.T) {
	_, err := parseSchema[duplicateContent]()
	if err == nil {
		t.Fatal("expected error for duplicate content tag")
	}
}

type duplicateGeoLat struct {
	ID   string  `vecdex:"id,id"`
	Lat1 float64 `vecdex:"lat1,geo_lat"`
	Lat2 float64 `vecdex:"lat2,geo_lat"`
	Lon  float64 `vecdex:"lon,geo_lon"`
}

func TestParseSchema_DuplicateGeoLat(t *testing.T) {
	_, err := parseSchema[duplicateGeoLat]()
	if err == nil {
		t.Fatal("expected error for duplicate geo_lat tag")
	}
}

type duplicateGeoLon struct {
	ID   string  `vecdex:"id,id"`
	Lat  float64 `vecdex:"lat,geo_lat"`
	Lon1 float64 `vecdex:"lon1,geo_lon"`
	Lon2 float64 `vecdex:"lon2,geo_lon"`
}

func TestParseSchema_DuplicateGeoLon(t *testing.T) {
	_, err := parseSchema[duplicateGeoLon]()
	if err == nil {
		t.Fatal("expected error for duplicate geo_lon tag")
	}
}
