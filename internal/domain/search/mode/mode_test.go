package mode

import "testing"

func TestIsValid(t *testing.T) {
	valid := []Mode{Hybrid, Semantic, Keyword}
	for _, m := range valid {
		if !m.IsValid() {
			t.Errorf("%q.IsValid() = false, want true", m)
		}
	}

	invalid := []Mode{"", "full-text", "vector", "HYBRID"}
	for _, m := range invalid {
		if m.IsValid() {
			t.Errorf("%q.IsValid() = true, want false", m)
		}
	}
}

func TestConstants(t *testing.T) {
	if Hybrid != "hybrid" {
		t.Errorf("Hybrid = %q", Hybrid)
	}
	if Semantic != "semantic" {
		t.Errorf("Semantic = %q", Semantic)
	}
	if Keyword != "keyword" {
		t.Errorf("Keyword = %q", Keyword)
	}
}
