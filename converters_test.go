package vecdex

import (
	"testing"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

func TestToInternalFields(t *testing.T) {
	fields := []FieldInfo{
		{Name: "country", Type: FieldTag},
		{Name: "population", Type: FieldNumeric},
	}

	result, err := toInternalFields(fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[0].Name() != "country" || result[0].FieldType() != field.Tag {
		t.Errorf("field[0] = %s/%s, want country/tag", result[0].Name(), result[0].FieldType())
	}
	if result[1].Name() != "population" || result[1].FieldType() != field.Numeric {
		t.Errorf("field[1] = %s/%s, want population/numeric", result[1].Name(), result[1].FieldType())
	}
}

func TestToInternalFields_InvalidName(t *testing.T) {
	// Reserved field names like "id", "content", "score", "vector" should fail.
	fields := []FieldInfo{{Name: "id", Type: FieldTag}}
	_, err := toInternalFields(fields)
	if err == nil {
		t.Fatal("expected error for reserved field name 'id'")
	}
}

func TestFromInternalCollection(t *testing.T) {
	f1, _ := field.New("country", field.Tag)
	f2, _ := field.New("pop", field.Numeric)
	col := domcol.Reconstruct("places", domcol.TypeGeo, []field.Field{f1, f2}, 3, 1000, 1)

	info := fromInternalCollection(col)
	if info.Name != "places" {
		t.Errorf("Name = %q, want places", info.Name)
	}
	if info.Type != CollectionTypeGeo {
		t.Errorf("Type = %q, want geo", info.Type)
	}
	if info.VectorDim != 3 {
		t.Errorf("VectorDim = %d, want 3", info.VectorDim)
	}
	if info.CreatedAt != 1000 {
		t.Errorf("CreatedAt = %d, want 1000", info.CreatedAt)
	}
	if len(info.Fields) != 2 {
		t.Fatalf("len(Fields) = %d, want 2", len(info.Fields))
	}
	if info.Fields[0].Name != "country" || info.Fields[0].Type != FieldTag {
		t.Errorf("Fields[0] = %+v", info.Fields[0])
	}
}

func TestToInternalDocument(t *testing.T) {
	doc := Document{
		ID:       "doc-1",
		Content:  "hello",
		Tags:     map[string]string{"lang": "en"},
		Numerics: map[string]float64{"score": 0.95},
	}

	d, err := toInternalDocument(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ID() != "doc-1" {
		t.Errorf("ID = %q, want doc-1", d.ID())
	}
	if d.Content() != "hello" {
		t.Errorf("Content = %q, want hello", d.Content())
	}
	if d.Tags()["lang"] != "en" {
		t.Errorf("Tags[lang] = %q, want en", d.Tags()["lang"])
	}
}

func TestToInternalDocument_InvalidID(t *testing.T) {
	doc := Document{ID: ""} // empty ID
	_, err := toInternalDocument(doc)
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestToInternalDocument_ReservedID(t *testing.T) {
	doc := Document{ID: "search"} // reserved
	_, err := toInternalDocument(doc)
	if err == nil {
		t.Fatal("expected error for reserved ID 'search'")
	}
}

func TestFromInternalDocument(t *testing.T) {
	d, _ := toInternalDocument(Document{
		ID: "x", Content: "hi",
		Tags:     map[string]string{"k": "v"},
		Numerics: map[string]float64{"n": 1.5},
	})

	out := fromInternalDocument(d)
	if out.ID != "x" {
		t.Errorf("ID = %q, want x", out.ID)
	}
	if out.Content != "hi" {
		t.Errorf("Content = %q, want hi", out.Content)
	}
}

func TestToInternalPatch(t *testing.T) {
	content := "new content"
	p := DocumentPatch{Content: &content}

	pp, err := toInternalPatch(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pp.HasContent() {
		t.Error("expected HasContent=true")
	}
}

func TestToInternalPatch_Empty(t *testing.T) {
	p := DocumentPatch{} // all nil
	_, err := toInternalPatch(p)
	if err == nil {
		t.Fatal("expected error for empty patch")
	}
}

func TestFromBatchResults(t *testing.T) {
	results := fromBatchResults(nil)
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestCollectionOptionFunctions(t *testing.T) {
	cfg := &collectionConfig{}
	Geo()(cfg)
	if cfg.colType != CollectionTypeGeo {
		t.Errorf("Geo() → colType = %q, want geo", cfg.colType)
	}

	cfg2 := &collectionConfig{}
	Text()(cfg2)
	if cfg2.colType != CollectionTypeText {
		t.Errorf("Text() → colType = %q, want text", cfg2.colType)
	}

	cfg3 := &collectionConfig{}
	WithField("country", FieldTag)(cfg3)
	WithField("pop", FieldNumeric)(cfg3)
	if len(cfg3.fields) != 2 {
		t.Fatalf("len(fields) = %d, want 2", len(cfg3.fields))
	}
	if cfg3.fields[0].Name != "country" || cfg3.fields[0].Type != FieldTag {
		t.Errorf("fields[0] = %+v", cfg3.fields[0])
	}
}

func TestToInternalFilters(t *testing.T) {
	fe := FilterExpression{
		Must: []FilterCondition{
			{Key: "country", Match: "CY"},
		},
	}

	expr, err := toInternalFilters(fe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if expr.IsEmpty() {
		t.Error("expected non-empty expression")
	}
	if len(expr.Must()) != 1 {
		t.Errorf("len(Must) = %d, want 1", len(expr.Must()))
	}
	if expr.Must()[0].Key() != "country" {
		t.Errorf("key = %q, want country", expr.Must()[0].Key())
	}
}

func TestToInternalFilters_Empty(t *testing.T) {
	expr, err := toInternalFilters(FilterExpression{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !expr.IsEmpty() {
		t.Error("expected empty expression")
	}
}

func TestToInternalFilters_Range(t *testing.T) {
	gte := 10.0
	lte := 100.0
	fe := FilterExpression{
		Must: []FilterCondition{
			{Key: "pop", Range: &RangeFilter{GTE: &gte, LTE: &lte}},
		},
	}

	expr, err := toInternalFilters(fe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cond := expr.Must()[0]
	if !cond.IsRange() {
		t.Error("expected range condition")
	}
	if *cond.Range().GTE() != 10.0 {
		t.Errorf("GTE = %f, want 10.0", *cond.Range().GTE())
	}
}

func TestToInternalFilters_InvalidRange(t *testing.T) {
	// gt and gte are mutually exclusive.
	gt := 5.0
	gte := 10.0
	fe := FilterExpression{
		Must: []FilterCondition{
			{Key: "pop", Range: &RangeFilter{GT: &gt, GTE: &gte}},
		},
	}
	_, err := toInternalFilters(fe)
	if err == nil {
		t.Fatal("expected error for mutually exclusive gt/gte")
	}
}

func TestFromSearchResults(t *testing.T) {
	results := fromSearchResults(nil)
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}
