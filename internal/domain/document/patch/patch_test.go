package patch

import (
	"strings"
	"testing"
)

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }

func TestNew_ContentOnly(t *testing.T) {
	p, err := New(strPtr("new content"), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.HasContent() {
		t.Error("HasContent() = false, want true")
	}
	if *p.Content() != "new content" {
		t.Errorf("Content() = %q", *p.Content())
	}
	if p.Tags() != nil {
		t.Errorf("Tags() = %v, want nil", p.Tags())
	}
	if p.Numerics() != nil {
		t.Errorf("Numerics() = %v, want nil", p.Numerics())
	}
}

func TestNew_TagsOnly(t *testing.T) {
	tags := map[string]*string{"lang": strPtr("go")}
	p, err := New(nil, tags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.HasContent() {
		t.Error("HasContent() = true, want false")
	}
	if p.Content() != nil {
		t.Error("Content() should be nil")
	}
	if len(p.Tags()) != 1 {
		t.Errorf("Tags() len = %d, want 1", len(p.Tags()))
	}
}

func TestNew_NumericsOnly(t *testing.T) {
	nums := map[string]*float64{"score": floatPtr(0.9)}
	p, err := New(nil, nil, nums)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Numerics()) != 1 {
		t.Errorf("Numerics() len = %d", len(p.Numerics()))
	}
}

func TestNew_AllFields(t *testing.T) {
	p, err := New(strPtr("text"), map[string]*string{"k": nil}, map[string]*float64{"n": nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.HasContent() {
		t.Error("HasContent() = false")
	}
}

func TestNew_EmptyPatch(t *testing.T) {
	_, err := New(nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty patch")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_EmptyMaps(t *testing.T) {
	_, err := New(nil, map[string]*string{}, map[string]*float64{})
	if err == nil {
		t.Fatal("expected error for empty maps")
	}
}

func TestNew_ContentTooLarge(t *testing.T) {
	big := strings.Repeat("x", MaxContentSize+1)
	_, err := New(&big, nil, nil)
	if err == nil {
		t.Fatal("expected error for content too large")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_ContentAtMaxSize(t *testing.T) {
	s := strings.Repeat("x", MaxContentSize)
	_, err := New(&s, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for max size content: %v", err)
	}
}
