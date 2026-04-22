package onnx

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	hftokenizer "github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

const (
	defaultPadToken = "[PAD]"
	modelFileName   = "model.onnx"
	tokenizerFile   = "tokenizer.json"
	configFile      = "config.json"
)

type encoderConfig struct {
	HiddenSize int `json:"hidden_size"`
}

type modelPaths struct {
	modelPath     string
	tokenizerPath string
	configPath    string
}

type encodedBatch struct {
	inputIDs      []int64
	attentionMask []int64
	tokenTypeIDs  []int64
	batchSize     int
	sequenceLen   int
	totalTokens   int
}

func resolveModelPaths(modelDir string) (modelPaths, error) {
	paths := modelPaths{
		modelPath:     filepath.Join(modelDir, "onnx", modelFileName),
		tokenizerPath: filepath.Join(modelDir, tokenizerFile),
		configPath:    filepath.Join(modelDir, configFile),
	}
	for _, path := range []string{paths.modelPath, paths.tokenizerPath, paths.configPath} {
		if _, err := os.Stat(path); err != nil {
			return modelPaths{}, fmt.Errorf("required model asset %q: %w", path, err)
		}
	}
	return paths, nil
}

func loadEncoderConfig(path string) (encoderConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return encoderConfig{}, fmt.Errorf("read encoder config: %w", err)
	}
	var cfg encoderConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return encoderConfig{}, fmt.Errorf("decode encoder config: %w", err)
	}
	return cfg, nil
}

func loadTokenizer(tokenizerPath string, maxLength int) (*hftokenizer.Tokenizer, error) {
	tk, err := pretrained.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}
	padID, ok := tk.TokenToId(defaultPadToken)
	if !ok {
		return nil, fmt.Errorf("pad token %q not found in tokenizer", defaultPadToken)
	}

	tk.WithTruncation(&hftokenizer.TruncationParams{
		MaxLength: maxLength,
		Strategy:  hftokenizer.LongestFirst,
	})

	paddingStrategy := hftokenizer.NewPaddingStrategy(hftokenizer.WithFixed(maxLength))
	tk.WithPadding(&hftokenizer.PaddingParams{
		Strategy:  *paddingStrategy,
		Direction: hftokenizer.Right,
		PadId:     padID,
		PadTypeId: 0,
		PadToken:  defaultPadToken,
	})
	return tk, nil
}

func encodeTexts(tk *hftokenizer.Tokenizer, texts []string) (encodedBatch, error) {
	if len(texts) == 0 {
		return encodedBatch{}, nil
	}

	inputs := make([]hftokenizer.EncodeInput, len(texts))
	for i, text := range texts {
		inputs[i] = hftokenizer.NewSingleEncodeInput(hftokenizer.NewInputSequence(text))
	}

	encodings, err := tk.EncodeBatch(inputs, true)
	if err != nil {
		return encodedBatch{}, fmt.Errorf("encode batch: %w", err)
	}
	return flattenEncodings(encodings)
}

func flattenEncodings(encodings []hftokenizer.Encoding) (encodedBatch, error) {
	if len(encodings) == 0 {
		return encodedBatch{}, nil
	}
	seqLen := len(encodings[0].Ids)
	if seqLen == 0 {
		return encodedBatch{}, fmt.Errorf("empty tokenized sequence")
	}

	batch := encodedBatch{
		inputIDs:      make([]int64, 0, len(encodings)*seqLen),
		attentionMask: make([]int64, 0, len(encodings)*seqLen),
		tokenTypeIDs:  make([]int64, 0, len(encodings)*seqLen),
		batchSize:     len(encodings),
		sequenceLen:   seqLen,
	}

	for i, encoding := range encodings {
		if len(encoding.Ids) != seqLen || len(encoding.AttentionMask) != seqLen {
			return encodedBatch{}, fmt.Errorf("encoding %d has inconsistent sequence length", i)
		}
		for j := 0; j < seqLen; j++ {
			batch.inputIDs = append(batch.inputIDs, int64(encoding.Ids[j]))
			mask := int64(encoding.AttentionMask[j])
			batch.attentionMask = append(batch.attentionMask, mask)
			batch.totalTokens += int(mask)

			typeID := int64(0)
			if j < len(encoding.TypeIds) {
				typeID = int64(encoding.TypeIds[j])
			}
			batch.tokenTypeIDs = append(batch.tokenTypeIDs, typeID)
		}
	}
	return batch, nil
}

func vectorRowsFrom3D(lastHidden []float32, attentionMask []int64, batchSize, seqLen, hiddenSize int) ([][]float32, error) {
	if batchSize <= 0 || seqLen <= 0 || hiddenSize <= 0 {
		return nil, fmt.Errorf("invalid output shape: [%d %d %d]", batchSize, seqLen, hiddenSize)
	}
	if len(lastHidden) != batchSize*seqLen*hiddenSize {
		return nil, fmt.Errorf(
			"last_hidden_state size mismatch: got %d, want %d",
			len(lastHidden), batchSize*seqLen*hiddenSize,
		)
	}
	if len(attentionMask) != batchSize*seqLen {
		return nil, fmt.Errorf(
			"attention_mask size mismatch: got %d, want %d",
			len(attentionMask), batchSize*seqLen,
		)
	}

	vectors := make([][]float32, batchSize)
	for batch := range batchSize {
		sum := make([]float64, hiddenSize)
		var tokenCount float64

		for token := range seqLen {
			mask := attentionMask[batch*seqLen+token]
			if mask == 0 {
				continue
			}
			tokenCount += float64(mask)
			offset := (batch*seqLen + token) * hiddenSize
			for dim := range hiddenSize {
				sum[dim] += float64(lastHidden[offset+dim])
			}
		}

		if tokenCount == 0 {
			return nil, fmt.Errorf("sample %d has zero non-padding tokens", batch)
		}

		vector := make([]float32, hiddenSize)
		for dim := range hiddenSize {
			vector[dim] = float32(sum[dim] / tokenCount)
		}
		normalizeVector(vector)
		vectors[batch] = vector
	}
	return vectors, nil
}

func vectorRowsFrom2D(data []float32, batchSize, hiddenSize int) ([][]float32, error) {
	if batchSize <= 0 || hiddenSize <= 0 {
		return nil, fmt.Errorf("invalid output shape: [%d %d]", batchSize, hiddenSize)
	}
	if len(data) != batchSize*hiddenSize {
		return nil, fmt.Errorf("sentence_embedding size mismatch: got %d, want %d", len(data), batchSize*hiddenSize)
	}

	vectors := make([][]float32, batchSize)
	for batch := range batchSize {
		start := batch * hiddenSize
		vector := append([]float32(nil), data[start:start+hiddenSize]...)
		normalizeVector(vector)
		vectors[batch] = vector
	}
	return vectors, nil
}

func normalizeVector(vector []float32) {
	var normSq float64
	for _, value := range vector {
		normSq += float64(value * value)
	}
	if normSq == 0 {
		return
	}
	invNorm := 1 / math.Sqrt(normSq)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) * invNorm)
	}
}
