package vecdex

import "testing"

type textDoc struct {
	ID       string `vecdex:"id,id"`
	Content  string `vecdex:"body,content"`
	Author   string `vecdex:"author,tag"`
	Priority int    `vecdex:"priority,numeric"`
}

type minimalDoc struct {
	ID string `vecdex:"id,id"`
}

type noIDDoc struct {
	Name string `vecdex:"name,content"`
}

type duplicateIDDoc struct {
	ID1 string `vecdex:"id1,id"`
	ID2 string `vecdex:"id2,id"`
}

type duplicateContentDoc struct {
	ID string `vecdex:"id,id"`
	A  string `vecdex:"a,content"`
	B  string `vecdex:"b,content"`
}

type unsupportedModifierDoc struct {
	ID  string  `vecdex:"id,id"`
	Lat float64 `vecdex:"lat,unknown"`
}

type skipFieldDoc struct {
	ID      string `vecdex:"id,id"`
	Ignored string `vecdex:"-"`
	NoTag   string
}

type storedFieldDoc struct {
	ID   string `vecdex:"id,id"`
	Name string `vecdex:"name,stored"`
	Cat  int    `vecdex:"cat,numeric"`
}

type uintDoc struct {
	ID  string `vecdex:"id,id"`
	Val uint32 `vecdex:"val,numeric"`
}

func TestParseSchema_TextDoc(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.colType != CollectionTypeText {
		t.Errorf("colType = %q, want %q", meta.colType, CollectionTypeText)
	}
	if meta.idIdx != 0 {
		t.Errorf("idIdx = %d, want 0", meta.idIdx)
	}
	if meta.contentIdx != 1 {
		t.Errorf("contentIdx = %d, want 1", meta.contentIdx)
	}
	if len(meta.fields) != 2 {
		t.Fatalf("len(fields) = %d, want 2", len(meta.fields))
	}
	if meta.fields[0].Name != "author" || meta.fields[0].Type != FieldTag {
		t.Errorf("fields[0] = %+v, want author/tag", meta.fields[0])
	}
	if meta.fields[1].Name != "priority" || meta.fields[1].Type != FieldNumeric {
		t.Errorf("fields[1] = %+v, want priority/numeric", meta.fields[1])
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

func TestParseSchema_NoID(t *testing.T) {
	_, err := parseSchema[noIDDoc]()
	if err == nil {
		t.Fatal("expected error for struct without id tag")
	}
}

func TestParseSchema_DuplicateID(t *testing.T) {
	_, err := parseSchema[duplicateIDDoc]()
	if err == nil {
		t.Fatal("expected error for duplicate id tag")
	}
}

func TestParseSchema_DuplicateContent(t *testing.T) {
	_, err := parseSchema[duplicateContentDoc]()
	if err == nil {
		t.Fatal("expected error for duplicate content tag")
	}
}

func TestParseSchema_UnsupportedModifier(t *testing.T) {
	_, err := parseSchema[unsupportedModifierDoc]()
	if err == nil {
		t.Fatal("expected error for unsupported modifier")
	}
}

func TestParseSchema_NonStruct(t *testing.T) {
	_, err := parseSchema[string]()
	if err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

func TestParseSchema_SkipFields(t *testing.T) {
	meta, err := parseSchema[skipFieldDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.fields) != 0 {
		t.Errorf("len(fields) = %d, want 0", len(meta.fields))
	}
}

func TestToDocument_TextDoc(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	doc := meta.toDocument(textDoc{
		ID:       "doc-1",
		Content:  "hello world",
		Author:   "alice",
		Priority: 42,
	})

	if doc.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", doc.ID)
	}
	if doc.Content != "hello world" {
		t.Errorf("Content = %q, want hello world", doc.Content)
	}
	if doc.Tags["author"] != "alice" {
		t.Errorf("Tags[author] = %q, want alice", doc.Tags["author"])
	}
	if doc.Numerics["priority"] != 42 {
		t.Errorf("Numerics[priority] = %f, want 42", doc.Numerics["priority"])
	}
}

func TestFromDocument_TextDoc(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	result := meta.fromDocument(Document{
		ID:       "doc-1",
		Content:  "hello world",
		Tags:     map[string]string{"author": "alice"},
		Numerics: map[string]float64{"priority": 42},
	})
	item, ok := result.(textDoc)
	if !ok {
		t.Fatalf("type assertion failed: got %T", result)
	}
	if item != (textDoc{ID: "doc-1", Content: "hello world", Author: "alice", Priority: 42}) {
		t.Errorf("item = %+v", item)
	}
}

func TestToDocument_Roundtrip(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	original := textDoc{
		ID:       "rt-1",
		Content:  "roundtrip",
		Author:   "bob",
		Priority: 7,
	}
	restored, ok := meta.fromDocument(meta.toDocument(original)).(textDoc)
	if !ok {
		t.Fatal("type assertion failed")
	}
	if original != restored {
		t.Errorf("roundtrip mismatch:\n  original: %+v\n  restored: %+v", original, restored)
	}
}

func TestCollectionOptions_Text(t *testing.T) {
	meta, err := parseSchema[textDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg := &collectionConfig{}
	for _, o := range meta.collectionOptions() {
		o.applyCollection(cfg)
	}
	if cfg.colType != CollectionTypeText {
		t.Errorf("colType = %q, want %q", cfg.colType, CollectionTypeText)
	}
	if len(cfg.fields) != 2 {
		t.Errorf("len(fields) = %d, want 2", len(cfg.fields))
	}
}

func TestParseSchema_StoredFieldExcluded(t *testing.T) {
	meta, err := parseSchema[storedFieldDoc]()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meta.fields) != 1 {
		t.Fatalf("len(fields) = %d, want 1", len(meta.fields))
	}
	if meta.fields[0].Name != "cat" || meta.fields[0].Type != FieldNumeric {
		t.Errorf("fields[0] = %+v, want cat/numeric", meta.fields[0])
	}
	if len(meta.tagFields) != 1 {
		t.Fatalf("len(tagFields) = %d, want 1", len(meta.tagFields))
	}
	if meta.tagFields[0].name != "name" {
		t.Errorf("tagFields[0].name = %q, want name", meta.tagFields[0].name)
	}
}

func TestToDocument_StoredField(t *testing.T) {
	meta, err := parseSchema[storedFieldDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	doc := meta.toDocument(storedFieldDoc{ID: "s1", Name: "Starbucks", Cat: 42})
	if doc.Tags["name"] != "Starbucks" {
		t.Errorf("Tags[name] = %q, want Starbucks", doc.Tags["name"])
	}
	if doc.Numerics["cat"] != 42 {
		t.Errorf("Numerics[cat] = %f, want 42", doc.Numerics["cat"])
	}
}

func TestFromDocument_StoredField(t *testing.T) {
	meta, err := parseSchema[storedFieldDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	result := meta.fromDocument(Document{
		ID:       "s1",
		Tags:     map[string]string{"name": "Starbucks"},
		Numerics: map[string]float64{"cat": 42},
	})
	item, ok := result.(storedFieldDoc)
	if !ok {
		t.Fatalf("type assertion failed: got %T", result)
	}
	if item.Name != "Starbucks" {
		t.Errorf("Name = %q, want Starbucks", item.Name)
	}
	if item.Cat != 42 {
		t.Errorf("Cat = %d, want 42", item.Cat)
	}
}

func TestCollectionOptions_StoredFieldExcluded(t *testing.T) {
	meta, err := parseSchema[storedFieldDoc]()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg := &collectionConfig{}
	for _, o := range meta.collectionOptions() {
		o.applyCollection(cfg)
	}
	if len(cfg.fields) != 1 {
		t.Errorf("len(fields) = %d, want 1", len(cfg.fields))
	}
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
	item, ok := result.(uintDoc)
	if !ok {
		t.Fatalf("type assertion failed: got %T", result)
	}
	if item.Val != 42 {
		t.Errorf("Val = %d, want 42", item.Val)
	}
}
