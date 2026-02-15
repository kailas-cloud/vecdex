package db

import (
	"strings"
	"testing"
)

func TestIndexBuilder_Simple(t *testing.T) {
	idx := NewIndex("test-idx").
		Prefix("doc:").
		Tag("category").
		Numeric("price").
		MustBuild()

	if err := idx.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Name != "test-idx" {
		t.Errorf("name = %q, want test-idx", idx.Name)
	}
	if idx.StorageType != StorageHash {
		t.Errorf("storage = %q, want HASH", idx.StorageType)
	}
	if len(idx.Fields) != 2 {
		t.Fatalf("fields count = %d, want 2", len(idx.Fields))
	}
	if idx.Fields[0].Name != "category" || idx.Fields[0].Type != IndexFieldTag {
		t.Errorf("field[0] = %+v, want category TAG", idx.Fields[0])
	}
	if idx.Fields[1].Name != "price" || idx.Fields[1].Type != IndexFieldNumeric {
		t.Errorf("field[1] = %+v, want price NUMERIC", idx.Fields[1])
	}
}

func TestIndexBuilder_VectorFlat(t *testing.T) {
	idx := NewIndex("vec-idx").
		Prefix("emb:").
		VectorFlat("embedding", 1536, DistanceCosine, 0).
		MustBuild()

	if len(idx.Fields) != 1 {
		t.Fatalf("fields count = %d, want 1", len(idx.Fields))
	}
	f := idx.Fields[0]
	if f.VectorAlgo != VectorFlat {
		t.Errorf("algo = %q, want FLAT", f.VectorAlgo)
	}
	if f.VectorDim != 1536 {
		t.Errorf("dim = %d, want 1536", f.VectorDim)
	}
	if f.VectorDistance != DistanceCosine {
		t.Errorf("distance = %q, want COSINE", f.VectorDistance)
	}
}

func TestIndexBuilder_VectorHNSW(t *testing.T) {
	idx := NewIndex("hnsw-idx").
		Prefix("doc:").
		Tag("type").
		VectorHNSW("vec", 768, DistanceL2, 32, 400).
		MustBuild()

	if len(idx.Fields) != 2 {
		t.Fatalf("fields count = %d, want 2", len(idx.Fields))
	}
	f := idx.Fields[1]
	if f.VectorAlgo != VectorHNSW {
		t.Errorf("algo = %q, want HNSW", f.VectorAlgo)
	}
	if f.VectorDim != 768 {
		t.Errorf("dim = %d, want 768", f.VectorDim)
	}
	if f.VectorDistance != DistanceL2 {
		t.Errorf("distance = %q, want L2", f.VectorDistance)
	}
	if f.VectorM != 32 {
		t.Errorf("M = %d, want 32", f.VectorM)
	}
	if f.VectorEFConstruct != 400 {
		t.Errorf("EF = %d, want 400", f.VectorEFConstruct)
	}
}

func TestIndexBuilder_JSON(t *testing.T) {
	idx := NewIndex("json-idx").
		OnJSON().
		Prefix("$.").
		Text("content").
		MustBuild()

	if idx.StorageType != StorageJSON {
		t.Errorf("storage = %q, want JSON", idx.StorageType)
	}
}

func TestIndexBuilder_TagOptions(t *testing.T) {
	idx := NewIndex("tag-idx").
		Prefix("t:").
		TagWithOpts("tags", "|", true).
		MustBuild()

	f := idx.Fields[0]
	if f.TagSeparator != "|" {
		t.Errorf("separator = %q, want |", f.TagSeparator)
	}
	if !f.TagCaseSensitive {
		t.Error("expected TagCaseSensitive=true")
	}
}

func TestIndexBuilder_MultiplePrefixes(t *testing.T) {
	idx := NewIndex("multi-idx").
		Prefix("a:", "b:", "c:").
		Tag("x").
		MustBuild()

	if len(idx.Prefixes) != 3 {
		t.Errorf("prefix count = %d, want 3", len(idx.Prefixes))
	}
}

func TestIndexBuilder_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		builder func() (*IndexDefinition, error)
		wantErr string
	}{
		{
			name: "empty name",
			builder: func() (*IndexDefinition, error) {
				return NewIndex("").Tag("x").Build()
			},
			wantErr: "index name is required",
		},
		{
			name: "no fields",
			builder: func() (*IndexDefinition, error) {
				return NewIndex("idx").Build()
			},
			wantErr: "at least one field",
		},
		{
			name: "vector without dim",
			builder: func() (*IndexDefinition, error) {
				return NewIndex("idx").Vector("v", 0, VectorFlat, DistanceCosine).Build()
			},
			wantErr: "positive DIM",
		},
		{
			name: "invalid characters",
			builder: func() (*IndexDefinition, error) {
				return NewIndex("idx with spaces").Tag("x").Build()
			},
			wantErr: "invalid characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.builder()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("got error %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestIndexDefinition_String(t *testing.T) {
	idx := NewIndex("my-idx").
		Prefix("doc:").
		Tag("cat").
		Vector("vec", 512, VectorFlat, DistanceCosine).
		MustBuild()

	s := idx.String()
	if !strings.HasPrefix(s, "FT.CREATE ") {
		t.Errorf("expected FT.CREATE prefix, got %q", s)
	}
	if !strings.Contains(s, "my-idx") {
		t.Error("missing index name in string output")
	}
}

func TestIndexBuilder_Alias(t *testing.T) {
	idx := &IndexDefinition{
		Name:     "alias-idx",
		Prefixes: []string{"a:"},
		Fields: []IndexField{
			{Name: "$.field", Alias: "field", Type: IndexFieldTag},
		},
	}

	if err := idx.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Fields[0].Alias != "field" {
		t.Errorf("alias = %q, want field", idx.Fields[0].Alias)
	}
}

func TestIndexBuilder_DuplicateFields(t *testing.T) {
	idx := &IndexDefinition{
		Name: "dup-idx",
		Fields: []IndexField{
			{Name: "field1", Type: IndexFieldTag},
			{Name: "field1", Type: IndexFieldNumeric},
		},
	}

	if err := idx.Validate(); err == nil {
		t.Fatal("expected error for duplicate fields")
	}
}
