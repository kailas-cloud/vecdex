package metrics

import "testing"

func TestNew(t *testing.T) {
	m := New(100, 50000, 25)
	if m.EmbeddingRequests() != 100 {
		t.Errorf("EmbeddingRequests() = %d", m.EmbeddingRequests())
	}
	if m.Tokens() != 50000 {
		t.Errorf("Tokens() = %d", m.Tokens())
	}
	if m.CostMillidollars() != 25 {
		t.Errorf("CostMillidollars() = %d", m.CostMillidollars())
	}
}

func TestNew_Zero(t *testing.T) {
	m := New(0, 0, 0)
	if m.EmbeddingRequests() != 0 || m.Tokens() != 0 || m.CostMillidollars() != 0 {
		t.Error("zero metrics should have zero values")
	}
}
