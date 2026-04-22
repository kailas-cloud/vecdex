package onnx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	hftokenizer "github.com/sugarme/tokenizer"
)

const testTokenizerJSON = `{
  "version":"1.0",
  "added_tokens":[
    {"id":0,"special":true,"content":"[PAD]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":1,"special":true,"content":"[UNK]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":2,"special":true,"content":"[CLS]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":3,"special":true,"content":"[SEP]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":4,"special":true,"content":"[MASK]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false}
  ],
  "normalizer":{
    "type":"BertNormalizer",
    "clean_text":true,
    "handle_chinese_chars":true,
    "strip_accents":null,
    "lowercase":true
  },
  "pre_tokenizer":{"type":"BertPreTokenizer"},
  "post_processor":{
    "type":"TemplateProcessing",
    "single":[
      {"SpecialToken":{"id":"[CLS]","type_id":0}},
      {"Sequence":{"id":"A","type_id":0}},
      {"SpecialToken":{"id":"[SEP]","type_id":0}}
    ],
    "pair":[
      {"SpecialToken":{"id":"[CLS]","type_id":0}},
      {"Sequence":{"id":"A","type_id":0}},
      {"SpecialToken":{"id":"[SEP]","type_id":0}},
      {"Sequence":{"id":"B","type_id":1}},
      {"SpecialToken":{"id":"[SEP]","type_id":1}}
    ],
    "special_tokens":{
      "[CLS]":{"id":"[CLS]","ids":[2],"tokens":["[CLS]"]},
      "[SEP]":{"id":"[SEP]","ids":[3],"tokens":["[SEP]"]}
    }
  },
  "decoder":{"type":"WordPiece","prefix":"##","cleanup":true},
  "model":{
    "type":"WordPiece",
    "unk_token":"[UNK]",
    "continuing_subword_prefix":"##",
    "max_input_chars_per_word":100,
    "vocab":{
      "[PAD]":0,
      "[UNK]":1,
      "[CLS]":2,
      "[SEP]":3,
      "[MASK]":4,
      "hello":5,
      "world":6,
      "small":7,
      "local":8,
      "model":9
    }
  }
}`

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
		t.Fatalf(
			"unexpected flattened tensor sizes: ids=%d mask=%d type=%d",
			len(batch.inputIDs),
			len(batch.attentionMask),
			len(batch.tokenTypeIDs),
		)
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

func TestResolveModelPaths(t *testing.T) {
	modelDir := writeTestModelDir(t)

	paths, err := resolveModelPaths(modelDir)
	if err != nil {
		t.Fatalf("resolveModelPaths() error = %v", err)
	}
	if !strings.HasSuffix(paths.modelPath, filepath.Join("onnx", modelFileName)) {
		t.Fatalf("unexpected modelPath: %s", paths.modelPath)
	}
	if !strings.HasSuffix(paths.tokenizerPath, tokenizerFile) {
		t.Fatalf("unexpected tokenizerPath: %s", paths.tokenizerPath)
	}
	if !strings.HasSuffix(paths.configPath, configFile) {
		t.Fatalf("unexpected configPath: %s", paths.configPath)
	}
}

func TestResolveModelPathsMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveModelPaths(dir); err == nil {
		t.Fatal("resolveModelPaths() expected error for missing assets")
	}
}

func TestLoadTokenizerAndEncodeTexts(t *testing.T) {
	modelDir := writeTestModelDir(t)
	tokenizerPath := filepath.Join(modelDir, tokenizerFile)

	tk, err := loadTokenizer(tokenizerPath, 8)
	if err != nil {
		t.Fatalf("loadTokenizer() error = %v", err)
	}

	batch, err := encodeTexts(tk, []string{"hello world", "small local model"})
	if err != nil {
		t.Fatalf("encodeTexts() error = %v", err)
	}
	if batch.batchSize != 2 {
		t.Fatalf("batchSize = %d, want 2", batch.batchSize)
	}
	if batch.sequenceLen != 8 {
		t.Fatalf("sequenceLen = %d, want 8", batch.sequenceLen)
	}
	if batch.totalTokens <= 0 {
		t.Fatalf("totalTokens = %d, want > 0", batch.totalTokens)
	}
}

func TestVectorRowsFrom2D(t *testing.T) {
	vectors, err := vectorRowsFrom2D([]float32{
		3, 4,
		5, 12,
	}, 2, 2)
	if err != nil {
		t.Fatalf("vectorRowsFrom2D() error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("len(vectors) = %d, want 2", len(vectors))
	}
	for i, vector := range vectors {
		var norm float32
		for _, value := range vector {
			norm += value * value
		}
		if norm < 0.99 || norm > 1.01 {
			t.Fatalf("vector %d not normalized: %f", i, norm)
		}
	}
}

func TestValidate3DShapeErrors(t *testing.T) {
	if err := validate3DShape(nil, nil, 0, 2, 3); err == nil {
		t.Fatal("validate3DShape() expected invalid shape error")
	}
	if err := validate3DShape(make([]float32, 3), make([]int64, 2), 1, 2, 3); err == nil {
		t.Fatal("validate3DShape() expected lastHidden size mismatch")
	}
	if err := validate3DShape(make([]float32, 6), make([]int64, 1), 1, 2, 3); err == nil {
		t.Fatal("validate3DShape() expected attentionMask size mismatch")
	}
}

func TestResolveSharedLibraryPath(t *testing.T) {
	t.Setenv("ONNXRUNTIME_SHARED_LIBRARY", "/tmp/libonnxruntime.custom")
	t.Setenv("ONNXRUNTIME_DIR", "/tmp/ignored")
	if got := resolveSharedLibraryPath(); got != "/tmp/libonnxruntime.custom" {
		t.Fatalf("resolveSharedLibraryPath() = %q, want explicit shared library", got)
	}

	t.Setenv("ONNXRUNTIME_SHARED_LIBRARY", "")
	t.Setenv("ONNXRUNTIME_DIR", "/tmp/onnxruntime")
	got := resolveSharedLibraryPath()
	if !strings.Contains(got, filepath.Join("lib", "libonnxruntime")) {
		t.Fatalf("resolveSharedLibraryPath() = %q, want ONNXRUNTIME_DIR-based path", got)
	}
}

func writeTestModelDir(t *testing.T) string {
	t.Helper()

	modelDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(modelDir, "onnx"), 0o755); err != nil {
		t.Fatalf("mkdir onnx dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, configFile), []byte(`{"hidden_size":384}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, tokenizerFile), []byte(testTokenizerJSON), 0o600); err != nil {
		t.Fatalf("write tokenizer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "onnx", modelFileName), []byte("stub"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return modelDir
}
