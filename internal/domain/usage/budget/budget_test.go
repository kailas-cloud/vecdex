package budget

import "testing"

func TestNew(t *testing.T) {
	b := New(1000000, 615800, false, 1700000000000)
	if b.TokensLimit() != 1000000 {
		t.Errorf("TokensLimit() = %d", b.TokensLimit())
	}
	if b.TokensRemaining() != 615800 {
		t.Errorf("TokensRemaining() = %d", b.TokensRemaining())
	}
	if b.IsExhausted() {
		t.Error("Exhausted() = true, want false")
	}
	if b.ResetsAt() != 1700000000000 {
		t.Errorf("ResetsAt() = %d", b.ResetsAt())
	}
}

func TestNew_Exhausted(t *testing.T) {
	b := New(1000, 0, true, 0)
	if !b.IsExhausted() {
		t.Error("Exhausted() = false, want true")
	}
	if b.TokensRemaining() != 0 {
		t.Errorf("TokensRemaining() = %d", b.TokensRemaining())
	}
}
