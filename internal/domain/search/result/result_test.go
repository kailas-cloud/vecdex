package result

import "testing"

func TestNew(t *testing.T) {
	tags := map[string]string{"lang": "go"}
	nums := map[string]float64{"score": 0.9}
	vec := []float32{0.1, 0.2}

	r := New("doc-1", 0.95, "hello", tags, nums, vec)

	if r.ID() != "doc-1" {
		t.Errorf("ID() = %q", r.ID())
	}
	if r.Score() != 0.95 {
		t.Errorf("Score() = %f", r.Score())
	}
	if r.Content() != "hello" {
		t.Errorf("Content() = %q", r.Content())
	}
	if r.Tags()["lang"] != "go" {
		t.Errorf("Tags() = %v", r.Tags())
	}
	if r.Numerics()["score"] != 0.9 {
		t.Errorf("Numerics() = %v", r.Numerics())
	}
	if len(r.Vector()) != 2 {
		t.Errorf("Vector() len = %d", len(r.Vector()))
	}
}

func TestNew_NilFields(t *testing.T) {
	r := New("id", 0, "", nil, nil, nil)
	if r.Tags() != nil {
		t.Errorf("Tags() = %v, want nil", r.Tags())
	}
	if r.Numerics() != nil {
		t.Errorf("Numerics() = %v, want nil", r.Numerics())
	}
	if r.Vector() != nil {
		t.Errorf("Vector() = %v, want nil", r.Vector())
	}
}
