package onnx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const benchmarkQueryUnit = "semantic vector search for local restaurants beaches museums and coffee shops in paphos cyprus"

type benchmarkQueryCase struct {
	name   string
	text   string
	tokens int
}

func BenchmarkEncodeTextsByQueryLength(b *testing.B) {
	tk, maxLength := loadMiniLMBenchmarkTokenizer(b)
	cases := buildBenchmarkQueryCases(b, tk, maxLength)

	b.ReportAllocs()

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			texts := []string{tc.text}
			b.ReportMetric(float64(tc.tokens), "tokens/op")
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := encodeTexts(tk, texts); err != nil {
					b.Fatalf("encodeTexts() error: %v", err)
				}
			}
		})
	}
}

func BenchmarkEmbedByQueryLength(b *testing.B) {
	modelDir := miniLMBenchmarkModelDir(b)
	maxLength := 256

	embedder, err := NewEmbedder(&Config{
		ModelDir:          modelDir,
		Model:             "sentence-transformers/all-MiniLM-L6-v2",
		Dimensions:        384,
		MaxLength:         maxLength,
		ExecutionProvider: "cpu",
		Provider:          "local_onnx",
	})
	if err != nil {
		b.Skipf("onnx embedder is unavailable for benchmark: %v", err)
	}

	tk, err := loadTokenizer(filepath.Join(modelDir, tokenizerFile), maxLength)
	if err != nil {
		b.Fatalf("loadTokenizer() error: %v", err)
	}

	cases := buildBenchmarkQueryCases(b, tk, maxLength)
	ctx := context.Background()

	b.ReportAllocs()

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportMetric(float64(tc.tokens), "tokens/op")

			// Warm up tokenizer + runtime before the timed section.
			if _, err := embedder.Embed(ctx, tc.text); err != nil {
				b.Fatalf("warmup Embed() error: %v", err)
			}

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				res, err := embedder.Embed(ctx, tc.text)
				if err != nil {
					b.Fatalf("Embed() error: %v", err)
				}
				if len(res.Embedding) != 384 {
					b.Fatalf("embedding length = %d, want 384", len(res.Embedding))
				}
			}
		})
	}
}

func loadMiniLMBenchmarkTokenizer(b testing.TB) (textTokenizer, int) {
	b.Helper()

	modelDir := miniLMBenchmarkModelDir(b)
	const maxLength = 256

	tk, err := loadTokenizer(filepath.Join(modelDir, tokenizerFile), maxLength)
	if err != nil {
		b.Fatalf("loadTokenizer() error: %v", err)
	}

	return tk, maxLength
}

func buildBenchmarkQueryCases(b testing.TB, tk textTokenizer, maxLength int) []benchmarkQueryCase {
	b.Helper()

	specs := []struct {
		name   string
		repeat int
	}{
		{name: "short", repeat: 1},
		{name: "medium", repeat: 4},
		{name: "long", repeat: 16},
		{name: "xlong", repeat: 64},
	}

	cases := make([]benchmarkQueryCase, 0, len(specs))
	for _, spec := range specs {
		text := strings.TrimSpace(strings.Repeat(benchmarkQueryUnit+" ", spec.repeat))
		encoded, err := encodeTexts(tk, []string{text})
		if err != nil {
			b.Fatalf("encodeTexts(%s) error: %v", spec.name, err)
		}

		name := fmt.Sprintf("%s_tokens_%d", spec.name, encoded.totalTokens)
		if encoded.totalTokens >= maxLength {
			name += "_truncated"
		}

		cases = append(cases, benchmarkQueryCase{
			name:   name,
			text:   text,
			tokens: encoded.totalTokens,
		})
	}

	return cases
}

func miniLMBenchmarkModelDir(b testing.TB) string {
	b.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		b.Fatal("runtime.Caller() failed")
	}

	modelDir := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "models", "all-MiniLM-L6-v2")
	modelDir = filepath.Clean(modelDir)

	if _, err := os.Stat(modelDir); err != nil {
		b.Skipf("benchmark model directory is unavailable: %v", err)
	}

	return modelDir
}
