package document

import (
	"strings"
	"testing"
)

func TestNew_Valid(t *testing.T) {
	tags := map[string]string{"lang": "go"}
	nums := map[string]float64{"priority": 1.5}

	doc, err := New("doc-1", "hello world", tags, nums)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID() != "doc-1" {
		t.Errorf("ID() = %q", doc.ID())
	}
	if doc.Content() != "hello world" {
		t.Errorf("Content() = %q", doc.Content())
	}
	if doc.Tags()["lang"] != "go" {
		t.Errorf("Tags() = %v", doc.Tags())
	}
	if doc.Numerics()["priority"] != 1.5 {
		t.Errorf("Numerics() = %v", doc.Numerics())
	}
	if doc.Vector() != nil {
		t.Errorf("Vector() should be nil for new document")
	}
}

func TestNew_NilMaps(t *testing.T) {
	doc, err := New("doc-1", "content", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Tags() != nil {
		t.Errorf("Tags() = %v, want nil", doc.Tags())
	}
	if doc.Numerics() != nil {
		t.Errorf("Numerics() = %v, want nil", doc.Numerics())
	}
}

func TestNew_ClonesMaps(t *testing.T) {
	tags := map[string]string{"k": "v"}
	nums := map[string]float64{"n": 1.0}

	doc, _ := New("doc-1", "content", tags, nums)

	// Mutating original maps must not affect the document
	tags["k"] = "mutated"
	nums["n"] = 999

	if doc.Tags()["k"] != "v" {
		t.Error("Tags mutation leaked into document")
	}
	if doc.Numerics()["n"] != 1.0 {
		t.Error("Numerics mutation leaked into document")
	}
}

func TestNew_EmptyID(t *testing.T) {
	_, err := New("", "content", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestNew_IDTooLong(t *testing.T) {
	_, err := New(strings.Repeat("a", 257), "content", nil, nil)
	if err == nil {
		t.Fatal("expected error for ID too long")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_InvalidIDChars(t *testing.T) {
	ids := []string{"has space", "слово", "doc.id", "doc/id"}
	for _, id := range ids {
		_, err := New(id, "content", nil, nil)
		if err == nil {
			t.Errorf("expected error for ID %q", id)
		}
	}
}

func TestNew_ReservedIDs(t *testing.T) {
	for _, id := range []string{"search", "collections"} {
		_, err := New(id, "content", nil, nil)
		if err == nil {
			t.Errorf("expected error for reserved ID %q", id)
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("error for %q = %q, want 'reserved'", id, err)
		}
	}
}

func TestNew_EmptyContent(t *testing.T) {
	_, err := New("doc-1", "", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_ContentTooLarge(t *testing.T) {
	_, err := New("doc-1", strings.Repeat("x", MaxContentSize+1), nil, nil)
	if err == nil {
		t.Fatal("expected error for content too large")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_ContentAtMaxSize(t *testing.T) {
	_, err := New("doc-1", strings.Repeat("x", MaxContentSize), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for content at max size: %v", err)
	}
}

func TestWithVector(t *testing.T) {
	doc, _ := New("doc-1", "content", nil, nil)
	vec := []float32{0.1, 0.2, 0.3}

	doc2 := doc.WithVector(vec)

	if doc.Vector() != nil {
		t.Error("original document should not have vector")
	}
	if len(doc2.Vector()) != 3 {
		t.Errorf("WithVector doc has %d elements", len(doc2.Vector()))
	}
	if doc2.ID() != "doc-1" {
		t.Error("WithVector should preserve ID")
	}
}

func TestReconstruct(t *testing.T) {
	vec := []float32{1.0, 2.0}
	doc := Reconstruct("id", "text", map[string]string{"k": "v"}, map[string]float64{"n": 1}, vec, 3)

	if doc.ID() != "id" {
		t.Errorf("ID() = %q", doc.ID())
	}
	if doc.Content() != "text" {
		t.Errorf("Content() = %q", doc.Content())
	}
	if len(doc.Vector()) != 2 {
		t.Errorf("Vector() len = %d", len(doc.Vector()))
	}
}

func TestReconstruct_SkipsValidation(t *testing.T) {
	// Reconstruct accepts reserved IDs
	doc := Reconstruct("search", "", nil, nil, nil, 0)
	if doc.ID() != "search" {
		t.Errorf("Reconstruct should skip validation")
	}
}
