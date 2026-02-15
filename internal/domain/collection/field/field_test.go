package field

import (
	"strings"
	"testing"
)

func TestNew_Valid(t *testing.T) {
	tests := []struct {
		name string
		ft   Type
	}{
		{"language", Tag},
		{"priority", Numeric},
		{"a", Tag},
		{strings.Repeat("x", 64), Numeric},
		{"with_underscore", Tag},
	}

	for _, tt := range tests {
		f, err := New(tt.name, tt.ft)
		if err != nil {
			t.Errorf("New(%q, %q) unexpected error: %v", tt.name, tt.ft, err)
			continue
		}
		if f.Name() != tt.name {
			t.Errorf("Name() = %q, want %q", f.Name(), tt.name)
		}
		if f.FieldType() != tt.ft {
			t.Errorf("Type() = %q, want %q", f.FieldType(), tt.ft)
		}
	}
}

func TestNew_EmptyName(t *testing.T) {
	_, err := New("", Tag)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want 'required'", err)
	}
}

func TestNew_NameTooLong(t *testing.T) {
	_, err := New(strings.Repeat("x", 65), Tag)
	if err == nil {
		t.Fatal("expected error for name too long")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q, want 'too long'", err)
	}
}

func TestNew_ReservedNames(t *testing.T) {
	reserved := []string{"id", "content", "score", "vector"}
	for _, name := range reserved {
		_, err := New(name, Tag)
		if err == nil {
			t.Errorf("expected error for reserved name %q", name)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("error for %q = %q, want 'reserved'", name, err)
		}
	}
}

func TestNew_InvalidType(t *testing.T) {
	_, err := New("valid_name", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	if !strings.Contains(err.Error(), "invalid field type") {
		t.Errorf("error = %q, want 'invalid field type'", err)
	}
}

func TestNew_EmptyType(t *testing.T) {
	_, err := New("valid_name", "")
	if err == nil {
		t.Fatal("expected error for empty type")
	}
}

func TestReconstruct(t *testing.T) {
	f := Reconstruct("anything", Tag)
	if f.Name() != "anything" {
		t.Errorf("Name() = %q, want %q", f.Name(), "anything")
	}
	if f.FieldType() != Tag {
		t.Errorf("Type() = %q, want %q", f.FieldType(), Tag)
	}
}

func TestReconstruct_SkipsValidation(t *testing.T) {
	// Reconstruct accepts reserved names without error
	f := Reconstruct("id", Tag)
	if f.Name() != "id" {
		t.Errorf("Reconstruct should skip validation, got Name() = %q", f.Name())
	}
}
