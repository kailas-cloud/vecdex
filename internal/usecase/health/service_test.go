package health

import (
	"context"
	"errors"
	"testing"
)

// --- Mocks ---

type mockDBPinger struct {
	err error
}

func (m *mockDBPinger) Ping(_ context.Context) error { return m.err }

type mockEmbeddingChecker struct {
	err error
}

func (m *mockEmbeddingChecker) HealthCheck(_ context.Context) error { return m.err }

// --- Tests ---

func TestCheck_AllHealthy(t *testing.T) {
	svc := New(&mockDBPinger{}, &mockEmbeddingChecker{})
	r := svc.Check(context.Background())

	if r.Status != Healthy {
		t.Errorf("expected %q, got %q", Healthy, r.Status)
	}
	if r.Checks["database"] != CheckOK {
		t.Errorf("expected database %q, got %q", CheckOK, r.Checks["database"])
	}
	if r.Checks["embedding"] != CheckOK {
		t.Errorf("expected embedding %q, got %q", CheckOK, r.Checks["embedding"])
	}
}

func TestCheck_DBError(t *testing.T) {
	svc := New(&mockDBPinger{err: errors.New("conn refused")}, &mockEmbeddingChecker{})
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["database"] != CheckError {
		t.Errorf("expected database %q, got %q", CheckError, r.Checks["database"])
	}
	if r.Checks["embedding"] != CheckOK {
		t.Errorf("expected embedding %q, got %q", CheckOK, r.Checks["embedding"])
	}
}

func TestCheck_EmbeddingError(t *testing.T) {
	svc := New(&mockDBPinger{}, &mockEmbeddingChecker{err: errors.New("timeout")})
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["database"] != CheckOK {
		t.Errorf("expected database %q, got %q", CheckOK, r.Checks["database"])
	}
	if r.Checks["embedding"] != CheckError {
		t.Errorf("expected embedding %q, got %q", CheckError, r.Checks["embedding"])
	}
}

func TestCheck_BothFail(t *testing.T) {
	svc := New(
		&mockDBPinger{err: errors.New("db down")},
		&mockEmbeddingChecker{err: errors.New("emb down")},
	)
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["database"] != CheckError {
		t.Error("expected database error")
	}
	if r.Checks["embedding"] != CheckError {
		t.Error("expected embedding error")
	}
}

func TestCheck_NoEmbedding(t *testing.T) {
	svc := New(&mockDBPinger{}, nil)
	r := svc.Check(context.Background())

	if r.Status != Healthy {
		t.Errorf("expected %q, got %q", Healthy, r.Status)
	}
	if r.Checks["database"] != CheckOK {
		t.Errorf("expected database %q, got %q", CheckOK, r.Checks["database"])
	}
	if _, ok := r.Checks["embedding"]; ok {
		t.Error("embedding check should be absent when embedding is nil")
	}
}

func TestCheck_NoEmbedding_DBError(t *testing.T) {
	svc := New(&mockDBPinger{err: errors.New("fail")}, nil)
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["database"] != CheckError {
		t.Error("expected database error")
	}
	if _, ok := r.Checks["embedding"]; ok {
		t.Error("embedding check should be absent when embedding is nil")
	}
}
