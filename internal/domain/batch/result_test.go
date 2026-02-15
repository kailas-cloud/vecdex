package batch

import (
	"errors"
	"testing"
)

func TestNewOK(t *testing.T) {
	r := NewOK("doc-1")
	if r.ID() != "doc-1" {
		t.Errorf("ID() = %q", r.ID())
	}
	if r.Status() != StatusOK {
		t.Errorf("Status() = %q, want %q", r.Status(), StatusOK)
	}
	if r.Err() != nil {
		t.Errorf("Err() = %v, want nil", r.Err())
	}
}

func TestNewError(t *testing.T) {
	err := errors.New("something failed")
	r := NewError("doc-2", err)
	if r.ID() != "doc-2" {
		t.Errorf("ID() = %q", r.ID())
	}
	if r.Status() != StatusError {
		t.Errorf("Status() = %q, want %q", r.Status(), StatusError)
	}
	if !errors.Is(r.Err(), err) {
		t.Errorf("Err() = %v, want %v", r.Err(), err)
	}
}

func TestStatusConstants(t *testing.T) {
	if StatusOK != "ok" {
		t.Errorf("StatusOK = %q", StatusOK)
	}
	if StatusError != "error" {
		t.Errorf("StatusError = %q", StatusError)
	}
}
