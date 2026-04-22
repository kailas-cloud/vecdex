package onnx

import (
	"os"
	"path/filepath"
	"testing"

	hftokenizer "github.com/sugarme/tokenizer"
)

func TestFlattenEncodings(t *testing.T) {
	encodings := []hftokenizer.Encoding{
		{
			Ids:           []int{101, 2000, 102, 0},
			TypeIds:       []int{0, 0, 0, 0},
			AttentionMask: []int{1, 1, 1, 0},
		},
		{
			Ids:           []int{101, 2023, 102, 0},
			TypeIds:       []int{0, 0, 0, 0},
			AttentionMask: []int{1, 1, 1, 0},
		},
	}

	batch, err := flattenEncodings(encodings)
	if err != nil {
		t.Fatalf("flattenEncodings() error = %v", err)
	}

	if batch.batchSize != 2 {
		t.Fatalf("batchSize = %d, want 2", batch.batchSize)
	}
	if batch.sequenceLen != 4 {
		t.Fatalf("sequenceLen = %d, want 4", batch.sequenceLen)
	}
	if batch.totalTokens != 6 {
		t.Fatalf("totalTokens = %d, want 6", batch.totalTokens)
	}
	if len(batch.inputIDs) != 8 || len(batch.attentionMask) != 8 || len(batch.tokenTypeIDs) != 8 {
		t.Fatalf("unexpected flattened tensor sizes: ids=%d mask=%d type=%d", len(batch.inputIDs), len(batch.attentionMask), len(batch.tokenTypeIDs))
	}
}

func TestVectorRowsFrom3D(t *testing.T) {
	lastHidden := []float32{
		1, 0, 0,
		0, 1, 0,
		2, 0, 0,
		0, 2, 0,
	}
	mask := []int64{
		1, 1,
		1, 0,
	}

	vectors, err := vectorRowsFrom3D(lastHidden, mask, 2, 2, 3)
	if err != nil {
		t.Fatalf("vectorRowsFrom3D() error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("len(vectors) = %d, want 2", len(vectors))
	}
	if len(vectors[0]) != 3 || len(vectors[1]) != 3 {
		t.Fatalf("unexpected vector sizes: %d %d", len(vectors[0]), len(vectors[1]))
	}

	var norm0, norm1 float32
	for _, v := range vectors[0] {
		norm0 += v * v
	}
	for _, v := range vectors[1] {
		norm1 += v * v
	}
	if norm0 < 0.99 || norm0 > 1.01 {
		t.Fatalf("first vector not normalized: %f", norm0)
	}
	if norm1 < 0.99 || norm1 > 1.01 {
		t.Fatalf("second vector not normalized: %f", norm1)
	}
}

func TestLoadEncoderConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, configFile)
	if err := os.WriteFile(path, []byte(`{"hidden_size":384}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadEncoderConfig(path)
	if err != nil {
		t.Fatalf("loadEncoderConfig() error = %v", err)
	}
	if cfg.HiddenSize != 384 {
		t.Fatalf("HiddenSize = %d, want 384", cfg.HiddenSize)
	}
}
