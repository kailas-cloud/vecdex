package vecdex

import (
	"errors"
	"testing"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

func TestToInternalFields(t *testing.T) {
	fields := []FieldInfo{
		{Name: "country", Type: FieldTag},
		{Name: "population", Type: FieldNumeric},
	}

	got, err := toInternalFields(fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name() != "country" || got[0].FieldType() != field.Tag {
		t.Errorf("field[0] = %s/%s, want country/tag", got[0].Name(), got[0].FieldType())
	}
	if got[1].Name() != "population" || got[1].FieldType() != field.Numeric {
		t.Errorf("field[1] = %s/%s, want population/numeric", got[1].Name(), got[1].FieldType())
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

func TestToBatchResponse_Nil(t *testing.T) {
	resp := toBatchResponse(nil)
	if len(resp.Results) != 0 {
		t.Errorf("len = %d, want 0", len(resp.Results))
	}
}

func TestCollectionOptionFunctions(t *testing.T) {
	cfg := &collectionConfig{}
	Geo().applyCollection(cfg)
	if cfg.colType != CollectionTypeGeo {
		t.Errorf("Geo() → colType = %q, want geo", cfg.colType)
	}

	cfg2 := &collectionConfig{}
	Text().applyCollection(cfg2)
	if cfg2.colType != CollectionTypeText {
		t.Errorf("Text() → colType = %q, want text", cfg2.colType)
	}

	cfg3 := &collectionConfig{}
	WithField("country", FieldTag).applyCollection(cfg3)
	WithField("pop", FieldNumeric).applyCollection(cfg3)
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

func TestFromSearchResults_WithData(t *testing.T) {
	r := result.New(
		"doc-1", 0.95, "hello",
		map[string]string{"lang": "en"},
		map[string]float64{"score": 0.99},
		nil,
	)
	out := fromSearchResults([]result.Result{r})
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", out[0].ID)
	}
	if out[0].Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", out[0].Score)
	}
	if out[0].Content != "hello" {
		t.Errorf("Content = %q, want hello", out[0].Content)
	}
	if out[0].Tags["lang"] != "en" {
		t.Errorf("Tags[lang] = %q, want en", out[0].Tags["lang"])
	}
	if out[0].Numerics["score"] != 0.99 {
		t.Errorf("Numerics[score] = %f, want 0.99", out[0].Numerics["score"])
	}
}

func TestToBatchResponse_WithData(t *testing.T) {
	ok := dombatch.NewOK("doc-1")
	fail := dombatch.NewError("doc-2", errors.New("conflict"))

	resp := toBatchResponse([]dombatch.Result{ok, fail})
	if len(resp.Results) != 2 {
		t.Fatalf("len = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].ID != "doc-1" || !resp.Results[0].OK {
		t.Errorf("result[0] = %+v, want doc-1/OK", resp.Results[0])
	}
	if resp.Results[1].ID != "doc-2" || resp.Results[1].OK {
		t.Errorf("result[1] = %+v, want doc-2/error", resp.Results[1])
	}
	if resp.Results[1].Err == nil {
		t.Error("expected non-nil error for failed result")
	}
	if resp.Succeeded != 1 || resp.Failed != 1 {
		t.Errorf("succeeded=%d failed=%d, want 1/1", resp.Succeeded, resp.Failed)
	}
}

func TestToInternalFilters_ShouldAndMustNot(t *testing.T) {
	fe := FilterExpression{
		Should:  []FilterCondition{{Key: "country", Match: "CY"}},
		MustNot: []FilterCondition{{Key: "country", Match: "TR"}},
	}

	expr, err := toInternalFilters(fe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(expr.Should()) != 1 {
		t.Errorf("len(Should) = %d, want 1", len(expr.Should()))
	}
	if len(expr.MustNot()) != 1 {
		t.Errorf("len(MustNot) = %d, want 1", len(expr.MustNot()))
	}
}

func TestToInternalFilters_ShouldError(t *testing.T) {
	// Пустой ключ вызывает ошибку в filter.NewMatch.
	fe := FilterExpression{
		Should: []FilterCondition{{Key: "", Match: "val"}},
	}
	_, err := toInternalFilters(fe)
	if err == nil {
		t.Fatal("expected error for empty key in should")
	}
}

func TestToInternalFilters_MustNotError(t *testing.T) {
	fe := FilterExpression{
		MustNot: []FilterCondition{{Key: "", Match: "val"}},
	}
	_, err := toInternalFilters(fe)
	if err == nil {
		t.Fatal("expected error for empty key in must_not")
	}
}

func TestToConditions_MatchError(t *testing.T) {
	// Пустой match value вызывает ошибку.
	fe := FilterExpression{
		Must: []FilterCondition{{Key: "country", Match: ""}},
	}
	_, err := toInternalFilters(fe)
	if err == nil {
		t.Fatal("expected error for empty match value")
	}
}
