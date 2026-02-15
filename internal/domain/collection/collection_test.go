package collection

import (
	"strings"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

func makeField(t *testing.T, name string, ft field.Type) field.Field {
	t.Helper()
	f, err := field.New(name, ft)
	if err != nil {
		t.Fatalf("field.New(%q, %q): %v", name, ft, err)
	}
	return f
}

func TestNew_Valid(t *testing.T) {
	f := makeField(t, "language", field.Tag)
	before := time.Now().UnixMilli()

	col, err := New("my-collection", []field.Field{f}, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := time.Now().UnixMilli()

	if col.Name() != "my-collection" {
		t.Errorf("Name() = %q, want %q", col.Name(), "my-collection")
	}
	if col.VectorDim() != 1024 {
		t.Errorf("VectorDim() = %d, want 1024", col.VectorDim())
	}
	if len(col.Fields()) != 1 {
		t.Errorf("Fields() len = %d, want 1", len(col.Fields()))
	}
	if col.CreatedAt() < before || col.CreatedAt() > after {
		t.Errorf("CreatedAt() = %d, want between %d and %d", col.CreatedAt(), before, after)
	}
}

func TestNew_NoFields(t *testing.T) {
	col, err := New("empty", nil, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(col.Fields()) != 0 {
		t.Errorf("Fields() len = %d, want 0", len(col.Fields()))
	}
}

func TestNew_EmptyName(t *testing.T) {
	_, err := New("", nil, 1024)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want 'required'", err)
	}
}

func TestNew_NameTooLong(t *testing.T) {
	_, err := New(strings.Repeat("a", 65), nil, 1024)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q, want 'too long'", err)
	}
}

func TestNew_InvalidNameChars(t *testing.T) {
	names := []string{"has space", "слово", "col.name", "col/name", "col@name"}
	for _, name := range names {
		_, err := New(name, nil, 1024)
		if err == nil {
			t.Errorf("expected error for name %q", name)
		}
	}
}

func TestNew_ValidNameChars(t *testing.T) {
	names := []string{"abc", "ABC-123", "with_underscore", "a-b-c", "X"}
	for _, name := range names {
		_, err := New(name, nil, 1024)
		if err != nil {
			t.Errorf("New(%q) unexpected error: %v", name, err)
		}
	}
}

func TestNew_ZeroVectorDim(t *testing.T) {
	_, err := New("col", nil, 0)
	if err == nil {
		t.Fatal("expected error for zero vector dim")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error = %q, want 'positive'", err)
	}
}

func TestNew_NegativeVectorDim(t *testing.T) {
	_, err := New("col", nil, -1)
	if err == nil {
		t.Fatal("expected error for negative vector dim")
	}
}

func TestNew_TooManyFields(t *testing.T) {
	fields := make([]field.Field, 65)
	for i := range fields {
		fields[i] = field.Reconstruct("f"+strings.Repeat("x", 2)+string(rune('a'+i%26))+string(rune('0'+i/26)), field.Tag)
	}
	_, err := New("col", fields, 1024)
	if err == nil {
		t.Fatal("expected error for too many fields")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("error = %q, want 'too many'", err)
	}
}

func TestNew_DuplicateFieldNames(t *testing.T) {
	f1 := field.Reconstruct("lang", field.Tag)
	f2 := field.Reconstruct("lang", field.Numeric)
	_, err := New("col", []field.Field{f1, f2}, 1024)
	if err == nil {
		t.Fatal("expected error for duplicate field names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error = %q, want 'duplicate'", err)
	}
}

func TestNew_MaxFields(t *testing.T) {
	fields := make([]field.Field, 64)
	for i := range fields {
		fields[i] = field.Reconstruct("f_"+string(rune('a'+i%26))+string(rune('a'+i/26)), field.Tag)
	}
	_, err := New("col", fields, 1024)
	if err != nil {
		t.Fatalf("unexpected error for 64 fields: %v", err)
	}
}

func TestReconstruct(t *testing.T) {
	f := field.Reconstruct("lang", field.Tag)
	col := Reconstruct("old-col", []field.Field{f}, 768, 1700000000000, 1)

	if col.Name() != "old-col" {
		t.Errorf("Name() = %q", col.Name())
	}
	if col.VectorDim() != 768 {
		t.Errorf("VectorDim() = %d", col.VectorDim())
	}
	if col.CreatedAt() != 1700000000000 {
		t.Errorf("CreatedAt() = %d", col.CreatedAt())
	}
}

func TestHasField(t *testing.T) {
	f1 := field.Reconstruct("language", field.Tag)
	f2 := field.Reconstruct("priority", field.Numeric)
	col := Reconstruct("col", []field.Field{f1, f2}, 1024, 0, 1)

	if !col.HasField("language", field.Tag) {
		t.Error("HasField(language, tag) = false, want true")
	}
	if !col.HasField("priority", field.Numeric) {
		t.Error("HasField(priority, numeric) = false, want true")
	}
	// Wrong type
	if col.HasField("language", field.Numeric) {
		t.Error("HasField(language, numeric) = true, want false")
	}
	// Non-existent field
	if col.HasField("missing", field.Tag) {
		t.Error("HasField(missing, tag) = true, want false")
	}
}

func TestFieldByName(t *testing.T) {
	f1 := field.Reconstruct("language", field.Tag)
	col := Reconstruct("col", []field.Field{f1}, 1024, 0, 1)

	found, ok := col.FieldByName("language")
	if !ok {
		t.Fatal("FieldByName(language) not found")
	}
	if found.Name() != "language" || found.FieldType() != field.Tag {
		t.Errorf("found = (%q, %q)", found.Name(), found.FieldType())
	}

	_, ok = col.FieldByName("missing")
	if ok {
		t.Error("FieldByName(missing) found, want not found")
	}
}
