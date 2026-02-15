package collection

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// --- Mocks ---

type mockRepo struct {
	created    domcol.Collection
	getResult  domcol.Collection
	listResult []domcol.Collection
	createErr  error
	getErr     error
	listErr    error
	deleteErr  error
}

func (m *mockRepo) Create(_ context.Context, col domcol.Collection) error {
	m.created = col
	return m.createErr
}

func (m *mockRepo) Get(_ context.Context, _ string) (domcol.Collection, error) {
	return m.getResult, m.getErr
}

func (m *mockRepo) List(_ context.Context) ([]domcol.Collection, error) {
	return m.listResult, m.listErr
}

func (m *mockRepo) Delete(_ context.Context, _ string) error {
	return m.deleteErr
}

func makeField(t *testing.T, name string, ft field.Type) field.Field {
	t.Helper()
	f, err := field.New(name, ft)
	if err != nil {
		t.Fatalf("field.New: %v", err)
	}
	return f
}

func makeCollection(t *testing.T, name string) domcol.Collection {
	t.Helper()
	col, err := domcol.New(name, nil, 1024)
	if err != nil {
		t.Fatalf("domcol.New: %v", err)
	}
	return col
}

// --- Tests ---

func TestCreate_Success(t *testing.T) {
	repo := &mockRepo{}
	svc := New(repo, 1024)

	col, err := svc.Create(context.Background(), "test-col", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name() != "test-col" {
		t.Errorf("expected name 'test-col', got %q", col.Name())
	}
	if col.VectorDim() != 1024 {
		t.Errorf("expected vectorDim 1024, got %d", col.VectorDim())
	}
}

func TestCreate_WithFields(t *testing.T) {
	repo := &mockRepo{}
	svc := New(repo, 1024)

	fields := []field.Field{makeField(t, "category", field.Tag), makeField(t, "rating", field.Numeric)}
	col, err := svc.Create(context.Background(), "test-col", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(col.Fields()) != 2 {
		t.Errorf("expected 2 fields, got %d", len(col.Fields()))
	}
}

func TestCreate_InvalidSchema(t *testing.T) {
	repo := &mockRepo{}
	svc := New(repo, 1024)

	// Empty name is an invalid schema
	_, err := svc.Create(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestCreate_RepoError(t *testing.T) {
	repoErr := errors.New("valkey: connection refused")
	repo := &mockRepo{createErr: repoErr}
	svc := New(repo, 1024)

	_, err := svc.Create(context.Background(), "test-col", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, repoErr) {
		t.Errorf("expected repo error wrapped, got %v", err)
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	repo := &mockRepo{createErr: domain.ErrAlreadyExists}
	svc := New(repo, 1024)

	_, err := svc.Create(context.Background(), "test-col", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestGet_Success(t *testing.T) {
	expected := makeCollection(t, "test-col")
	repo := &mockRepo{getResult: expected}
	svc := New(repo, 1024)

	col, err := svc.Get(context.Background(), "test-col")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name() != "test-col" {
		t.Errorf("expected name 'test-col', got %q", col.Name())
	}
}

func TestGet_NotFound(t *testing.T) {
	repo := &mockRepo{getErr: domain.ErrNotFound}
	svc := New(repo, 1024)

	_, err := svc.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestList_Success(t *testing.T) {
	cols := []domcol.Collection{makeCollection(t, "a"), makeCollection(t, "b")}
	repo := &mockRepo{listResult: cols}
	svc := New(repo, 1024)

	result, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 collections, got %d", len(result))
	}
}

func TestList_Empty(t *testing.T) {
	repo := &mockRepo{listResult: []domcol.Collection{}}
	svc := New(repo, 1024)

	result, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 collections, got %d", len(result))
	}
}

func TestDelete_Success(t *testing.T) {
	repo := &mockRepo{}
	svc := New(repo, 1024)

	if err := svc.Delete(context.Background(), "test-col"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	repo := &mockRepo{deleteErr: domain.ErrNotFound}
	svc := New(repo, 1024)

	err := svc.Delete(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
