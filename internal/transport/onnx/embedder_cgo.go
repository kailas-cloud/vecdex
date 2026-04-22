//go:build cgo

package onnx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	hftokenizer "github.com/sugarme/tokenizer"
	ort "github.com/yalue/onnxruntime_go"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

var (
	ortInitOnce sync.Once
	ortInitErr  error
)

// Embedder runs local ONNX embedding inference on CPU.
type Embedder struct {
	session            *ort.DynamicAdvancedSession
	tokenizer          *hftokenizer.Tokenizer
	modelPath          string
	maxLength          int
	provider           string
	model              string
	logger             *zap.Logger
	inputNames         []string
	outputNames        []string
	expectsTokenTypeID bool
	mu                 sync.Mutex
}

// Config holds the ONNX embedding provider settings.
type Config struct {
	ModelDir          string
	Model             string
	Dimensions        int
	MaxLength         int
	ExecutionProvider string
	Provider          string
	Logger            *zap.Logger
}

// NewEmbedder creates a local ONNX embedding provider.
func NewEmbedder(cfg *Config) (*Embedder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("onnx config is required")
	}
	paths, err := resolveModelPaths(cfg.ModelDir)
	if err != nil {
		return nil, err
	}
	encoderCfg, err := loadEncoderConfig(paths.configPath)
	if err != nil {
		return nil, err
	}
	if cfg.Dimensions > 0 && encoderCfg.HiddenSize > 0 && cfg.Dimensions != encoderCfg.HiddenSize {
		return nil, fmt.Errorf(
			"configured vector dimensions %d do not match model hidden_size %d",
			cfg.Dimensions, encoderCfg.HiddenSize,
		)
	}
	if err := initializeRuntime(); err != nil {
		return nil, err
	}
	if cfg.ExecutionProvider != "" && cfg.ExecutionProvider != "cpu" {
		return nil, fmt.Errorf("unsupported onnx execution provider %q", cfg.ExecutionProvider)
	}

	tokenizer, err := loadTokenizer(paths.tokenizerPath, cfg.MaxLength)
	if err != nil {
		return nil, err
	}

	inputInfo, outputInfo, err := ort.GetInputOutputInfo(paths.modelPath)
	if err != nil {
		return nil, fmt.Errorf("inspect onnx model IO: %w", err)
	}
	inputNames := make([]string, 0, len(inputInfo))
	outputNames := make([]string, 0, len(outputInfo))
	expectsTokenTypeID := false
	for _, info := range inputInfo {
		inputNames = append(inputNames, info.Name)
		if info.Name == "token_type_ids" {
			expectsTokenTypeID = true
		}
	}
	for _, info := range outputInfo {
		outputNames = append(outputNames, info.Name)
	}

	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create onnx session options: %w", err)
	}
	defer func() { _ = options.Destroy() }()

	options.SetIntraOpNumThreads(1)
	options.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(paths.modelPath, inputNames, outputNames, options)
	if err != nil {
		return nil, fmt.Errorf("create onnx session: %w", err)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Embedder{
		session:            session,
		tokenizer:          tokenizer,
		modelPath:          paths.modelPath,
		maxLength:          cfg.MaxLength,
		provider:           cfg.Provider,
		model:              cfg.Model,
		logger:             logger,
		inputNames:         inputNames,
		outputNames:        outputNames,
		expectsTokenTypeID: expectsTokenTypeID,
	}, nil
}

// Embed implements domain.Embedder.
func (e *Embedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	res, err := e.BatchEmbed(ctx, []string{text})
	if err != nil {
		return domain.EmbeddingResult{}, err
	}
	if len(res.Embeddings) == 0 {
		return domain.EmbeddingResult{}, fmt.Errorf("onnx embedder returned no embeddings")
	}
	return domain.EmbeddingResult{
		Embedding:    res.Embeddings[0],
		PromptTokens: res.PromptTokens,
		TotalTokens:  res.TotalTokens,
	}, nil
}

// BatchEmbed implements domain.BatchEmbedder.
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}

	start := time.Now()

	e.mu.Lock()
	defer e.mu.Unlock()

	encoded, err := encodeTexts(e.tokenizer, texts)
	if err != nil {
		e.recordError("tokenizer_error")
		return domain.BatchEmbeddingResult{}, err
	}
	if encoded.batchSize == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}

	shape := ort.NewShape(int64(encoded.batchSize), int64(encoded.sequenceLen))
	inputIDs, err := ort.NewTensor(shape, encoded.inputIDs)
	if err != nil {
		e.recordError("input_tensor_error")
		return domain.BatchEmbeddingResult{}, fmt.Errorf("create input_ids tensor: %w", err)
	}
	defer func() { _ = inputIDs.Destroy() }()

	attentionMask, err := ort.NewTensor(shape, encoded.attentionMask)
	if err != nil {
		e.recordError("input_tensor_error")
		return domain.BatchEmbeddingResult{}, fmt.Errorf("create attention_mask tensor: %w", err)
	}
	defer func() { _ = attentionMask.Destroy() }()

	var tokenTypeIDs *ort.Tensor[int64]
	if e.expectsTokenTypeID {
		tokenTypeIDs, err = ort.NewTensor(shape, encoded.tokenTypeIDs)
		if err != nil {
			e.recordError("input_tensor_error")
			return domain.BatchEmbeddingResult{}, fmt.Errorf("create token_type_ids tensor: %w", err)
		}
		defer func() { _ = tokenTypeIDs.Destroy() }()
	}

	inputValues := make([]ort.Value, 0, len(e.inputNames))
	for _, name := range e.inputNames {
		switch name {
		case "input_ids":
			inputValues = append(inputValues, inputIDs)
		case "attention_mask":
			inputValues = append(inputValues, attentionMask)
		case "token_type_ids":
			inputValues = append(inputValues, tokenTypeIDs)
		default:
			e.recordError("unsupported_input")
			return domain.BatchEmbeddingResult{}, fmt.Errorf("unsupported onnx input %q", name)
		}
	}

	outputValues := make([]ort.Value, len(e.outputNames))
	if err := e.session.Run(inputValues, outputValues); err != nil {
		e.recordError("runtime_error")
		return domain.BatchEmbeddingResult{}, fmt.Errorf("run onnx session: %w", err)
	}
	defer destroyValues(outputValues)

	embeddings, err := extractEmbeddings(outputValues, encoded.attentionMask)
	if err != nil {
		e.recordError("output_decode_error")
		return domain.BatchEmbeddingResult{}, err
	}

	duration := time.Since(start)
	e.recordSuccess(duration, encoded.totalTokens)

	select {
	case <-ctx.Done():
		return domain.BatchEmbeddingResult{}, ctx.Err()
	default:
	}

	return domain.BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: encoded.totalTokens,
		TotalTokens:  encoded.totalTokens,
	}, nil
}

// HealthCheck verifies local model availability and a short inference smoke test.
func (e *Embedder) HealthCheck(ctx context.Context) error {
	_, err := e.BatchEmbed(ctx, []string{"vecdex health check"})
	if err != nil {
		return fmt.Errorf("onnx health check: %w", err)
	}
	return nil
}

func (e *Embedder) recordSuccess(duration time.Duration, totalTokens int) {
	metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, e.model, "success").Inc()
	metrics.EmbeddingRequestDuration.WithLabelValues(e.provider, e.model).Observe(duration.Seconds())
	metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, e.model, "prompt").Add(float64(totalTokens))
	metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, e.model, "total").Add(float64(totalTokens))
}

func (e *Embedder) recordError(errorType string) {
	metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, e.model, "error").Inc()
	metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, e.model, errorType).Inc()
}

func initializeRuntime() error {
	ortInitOnce.Do(func() {
		if sharedLib := resolveSharedLibraryPath(); sharedLib != "" {
			ort.SetSharedLibraryPath(sharedLib)
		}
		ortInitErr = ort.InitializeEnvironment()
	})
	if ortInitErr != nil {
		return fmt.Errorf("initialize onnx runtime: %w", ortInitErr)
	}
	return nil
}

func resolveSharedLibraryPath() string {
	if sharedLib := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY"); sharedLib != "" {
		return sharedLib
	}
	baseDir := os.Getenv("ONNXRUNTIME_DIR")
	if baseDir == "" {
		return ""
	}

	libName := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		libName = "libonnxruntime.dylib"
	}
	return filepath.Join(baseDir, "lib", libName)
}

func destroyValues(values []ort.Value) {
	for _, value := range values {
		if value != nil {
			_ = value.Destroy()
		}
	}
}

func extractEmbeddings(outputs []ort.Value, attentionMask []int64) ([][]float32, error) {
	for _, output := range outputs {
		tensor, ok := output.(*ort.Tensor[float32])
		if !ok {
			continue
		}

		shape := tensor.GetShape()
		switch len(shape) {
		case 2:
			return vectorRowsFrom2D(tensor.GetData(), int(shape[0]), int(shape[1]))
		case 3:
			return vectorRowsFrom3D(
				tensor.GetData(),
				attentionMask,
				int(shape[0]),
				int(shape[1]),
				int(shape[2]),
			)
		default:
			continue
		}
	}
	return nil, fmt.Errorf("no float32 tensor output found in onnx model response")
}
