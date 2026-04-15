package health

import (
	"context"
	"errors"
	"testing"
)

// --- Mocks ---

type mockValkeyPinger struct {
	err error
}

func (m *mockValkeyPinger) Ping(_ context.Context) error { return m.err }

type mockEmbeddingChecker struct {
	err error
}

func (m *mockEmbeddingChecker) HealthCheck(_ context.Context) error { return m.err }

// --- Tests ---

func TestCheck_AllHealthy(t *testing.T) {
	svc := New(&mockValkeyPinger{}, &mockEmbeddingChecker{})
	r := svc.Check(context.Background())

	if r.Status != Healthy {
		t.Errorf("expected %q, got %q", Healthy, r.Status)
	}
	if r.Checks["valkey"] != CheckOK {
		t.Errorf("expected valkey %q, got %q", CheckOK, r.Checks["valkey"])
	}
	if r.Checks["embedding"] != CheckOK {
		t.Errorf("expected embedding %q, got %q", CheckOK, r.Checks["embedding"])
	}
}

func TestCheck_DBError(t *testing.T) {
	svc := New(&mockValkeyPinger{err: errors.New("conn refused")}, &mockEmbeddingChecker{})
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["valkey"] != CheckError {
		t.Errorf("expected valkey %q, got %q", CheckError, r.Checks["valkey"])
	}
	if r.Checks["embedding"] != CheckOK {
		t.Errorf("expected embedding %q, got %q", CheckOK, r.Checks["embedding"])
	}
}

func TestCheck_EmbeddingError(t *testing.T) {
	svc := New(&mockValkeyPinger{}, &mockEmbeddingChecker{err: errors.New("timeout")})
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["valkey"] != CheckOK {
		t.Errorf("expected valkey %q, got %q", CheckOK, r.Checks["valkey"])
	}
	if r.Checks["embedding"] != CheckError {
		t.Errorf("expected embedding %q, got %q", CheckError, r.Checks["embedding"])
	}
}

func TestCheck_BothFail(t *testing.T) {
	svc := New(
		&mockValkeyPinger{err: errors.New("valkey down")},
		&mockEmbeddingChecker{err: errors.New("emb down")},
	)
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["valkey"] != CheckError {
		t.Error("expected valkey error")
	}
	if r.Checks["embedding"] != CheckError {
		t.Error("expected embedding error")
	}
}

func TestCheck_NoEmbedding(t *testing.T) {
	svc := New(&mockValkeyPinger{}, nil)
	r := svc.Check(context.Background())

	if r.Status != Healthy {
		t.Errorf("expected %q, got %q", Healthy, r.Status)
	}
	if r.Checks["valkey"] != CheckOK {
		t.Errorf("expected valkey %q, got %q", CheckOK, r.Checks["valkey"])
	}
	if _, ok := r.Checks["embedding"]; ok {
		t.Error("embedding check should be absent when embedding is nil")
	}
}

func TestCheck_NoEmbedding_DBError(t *testing.T) {
	svc := New(&mockValkeyPinger{err: errors.New("fail")}, nil)
	r := svc.Check(context.Background())

	if r.Status != Degraded {
		t.Errorf("expected %q, got %q", Degraded, r.Status)
	}
	if r.Checks["valkey"] != CheckError {
		t.Error("expected valkey error")
	}
	if _, ok := r.Checks["embedding"]; ok {
		t.Error("embedding check should be absent when embedding is nil")
	}
}
