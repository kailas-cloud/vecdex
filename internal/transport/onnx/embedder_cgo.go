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
	errOrtInit  error
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

type preparedResources struct {
	paths              modelPaths
	tokenizer          *hftokenizer.Tokenizer
	inputNames         []string
	outputNames        []string
	expectsTokenTypeID bool
}

type modelIO struct {
	inputNames         []string
	outputNames        []string
	expectsTokenTypeID bool
}

// NewEmbedder creates a local ONNX embedding provider.
func NewEmbedder(cfg *Config) (*Embedder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("onnx config is required")
	}
	resources, err := prepareModelResources(cfg)
	if err != nil {
		return nil, err
	}
	session, err := newSession(resources.paths.modelPath, resources.inputNames, resources.outputNames)
	if err != nil {
		return nil, err
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Embedder{
		session:            session,
		tokenizer:          resources.tokenizer,
		modelPath:          resources.paths.modelPath,
		maxLength:          cfg.MaxLength,
		provider:           cfg.Provider,
		model:              cfg.Model,
		logger:             logger,
		inputNames:         resources.inputNames,
		outputNames:        resources.outputNames,
		expectsTokenTypeID: resources.expectsTokenTypeID,
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
	e.mu.Lock()
	defer e.mu.Unlock()

	encoded, embeddings, duration, err := e.runBatch(texts)
	if err != nil {
		return domain.BatchEmbeddingResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.BatchEmbeddingResult{}, fmt.Errorf("batch embed canceled: %w", err)
	}
	e.recordSuccess(duration, encoded.totalTokens)
	return domain.BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: encoded.totalTokens,
		TotalTokens:  encoded.totalTokens,
	}, nil
}

func (e *Embedder) runBatch(texts []string) (encodedBatch, [][]float32, time.Duration, error) {
	start := time.Now()

	encoded, err := encodeTexts(e.tokenizer, texts)
	if err != nil {
		e.recordError("tokenizer_error")
		return encodedBatch{}, nil, 0, err
	}
	if encoded.batchSize == 0 {
		return encodedBatch{}, nil, 0, nil
	}

	outputValues, cleanup, err := e.runSession(&encoded)
	if err != nil {
		return encodedBatch{}, nil, 0, err
	}
	defer cleanup()

	embeddings, err := extractEmbeddings(outputValues, encoded.attentionMask)
	if err != nil {
		e.recordError("output_decode_error")
		return encodedBatch{}, nil, 0, err
	}
	return encoded, embeddings, time.Since(start), nil
}

func (e *Embedder) runSession(encoded *encodedBatch) ([]ort.Value, func(), error) {
	inputValues, cleanupInputs, err := e.newInputValues(encoded)
	if err != nil {
		e.recordError("input_tensor_error")
		return nil, nil, err
	}

	outputValues := make([]ort.Value, len(e.outputNames))
	if err := e.session.Run(inputValues, outputValues); err != nil {
		cleanupInputs()
		e.recordError("runtime_error")
		return nil, nil, fmt.Errorf("run onnx session: %w", err)
	}

	cleanup := func() {
		destroyValues(outputValues)
		cleanupInputs()
	}
	return outputValues, cleanup, nil
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
		errOrtInit = ort.InitializeEnvironment()
	})
	if errOrtInit != nil {
		return fmt.Errorf("initialize onnx runtime: %w", errOrtInit)
	}
	return nil
}

func prepareModelResources(cfg *Config) (preparedResources, error) {
	if cfg.ExecutionProvider != "" && cfg.ExecutionProvider != "cpu" {
		return preparedResources{}, fmt.Errorf(
			"unsupported onnx execution provider %q",
			cfg.ExecutionProvider,
		)
	}
	paths, err := resolveModelPaths(cfg.ModelDir)
	if err != nil {
		return preparedResources{}, err
	}
	if err := validateDimensions(cfg, paths.configPath); err != nil {
		return preparedResources{}, err
	}
	if err := initializeRuntime(); err != nil {
		return preparedResources{}, err
	}
	tokenizer, err := loadTokenizer(paths.tokenizerPath, cfg.MaxLength)
	if err != nil {
		return preparedResources{}, err
	}
	io, err := inspectModelIO(paths.modelPath)
	if err != nil {
		return preparedResources{}, err
	}
	return preparedResources{
		paths:              paths,
		tokenizer:          tokenizer,
		inputNames:         io.inputNames,
		outputNames:        io.outputNames,
		expectsTokenTypeID: io.expectsTokenTypeID,
	}, nil
}

func validateDimensions(cfg *Config, configPath string) error {
	encoderCfg, err := loadEncoderConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.Dimensions > 0 && encoderCfg.HiddenSize > 0 && cfg.Dimensions != encoderCfg.HiddenSize {
		return fmt.Errorf(
			"configured vector dimensions %d do not match model hidden_size %d",
			cfg.Dimensions, encoderCfg.HiddenSize,
		)
	}
	return nil
}

func inspectModelIO(modelPath string) (result modelIO, err error) {
	inputInfo, outputInfo, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return modelIO{}, fmt.Errorf("inspect onnx model IO: %w", err)
	}
	result.inputNames = make([]string, 0, len(inputInfo))
	result.outputNames = make([]string, 0, len(outputInfo))
	for _, info := range inputInfo {
		result.inputNames = append(result.inputNames, info.Name)
		if info.Name == "token_type_ids" {
			result.expectsTokenTypeID = true
		}
	}
	for _, info := range outputInfo {
		result.outputNames = append(result.outputNames, info.Name)
	}
	return result, nil
}

func newSession(modelPath string, inputNames, outputNames []string) (*ort.DynamicAdvancedSession, error) {
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create onnx session options: %w", err)
	}
	defer func() { _ = options.Destroy() }()

	if err := options.SetIntraOpNumThreads(1); err != nil {
		return nil, fmt.Errorf("set intra-op threads: %w", err)
	}
	if err := options.SetInterOpNumThreads(1); err != nil {
		return nil, fmt.Errorf("set inter-op threads: %w", err)
	}

	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, options)
	if err != nil {
		return nil, fmt.Errorf("create onnx session: %w", err)
	}
	return session, nil
}

func (e *Embedder) newInputValues(encoded *encodedBatch) ([]ort.Value, func(), error) {
	shape := ort.NewShape(int64(encoded.batchSize), int64(encoded.sequenceLen))
	inputIDs, err := ort.NewTensor(shape, encoded.inputIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("create input_ids tensor: %w", err)
	}

	attentionMask, err := ort.NewTensor(shape, encoded.attentionMask)
	if err != nil {
		_ = inputIDs.Destroy()
		return nil, nil, fmt.Errorf("create attention_mask tensor: %w", err)
	}

	var tokenTypeIDs *ort.Tensor[int64]
	if e.expectsTokenTypeID {
		tokenTypeIDs, err = ort.NewTensor(shape, encoded.tokenTypeIDs)
		if err != nil {
			_ = attentionMask.Destroy()
			_ = inputIDs.Destroy()
			return nil, nil, fmt.Errorf("create token_type_ids tensor: %w", err)
		}
	}

	inputValues, err := e.buildInputValues(inputIDs, attentionMask, tokenTypeIDs)
	if err != nil {
		if tokenTypeIDs != nil {
			_ = tokenTypeIDs.Destroy()
		}
		_ = attentionMask.Destroy()
		_ = inputIDs.Destroy()
		return nil, nil, err
	}

	cleanup := func() {
		if tokenTypeIDs != nil {
			_ = tokenTypeIDs.Destroy()
		}
		_ = attentionMask.Destroy()
		_ = inputIDs.Destroy()
	}
	return inputValues, cleanup, nil
}

func (e *Embedder) buildInputValues(
	inputIDs *ort.Tensor[int64],
	attentionMask *ort.Tensor[int64],
	tokenTypeIDs *ort.Tensor[int64],
) ([]ort.Value, error) {
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
			return nil, fmt.Errorf("unsupported onnx input %q", name)
		}
	}
	return inputValues, nil
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
