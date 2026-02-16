package chi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
	usageuc "github.com/kailas-cloud/vecdex/internal/usecase/usage"

	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
)

const maxBatchSize = 100

// errorHandler tries to handle a domain error. Returns true if handled.
type errorHandler func(w http.ResponseWriter, err error, msg string) bool

// Server implements generated.ServerInterface for the oapi-codegen chi router.
type Server struct {
	gen.Unimplemented
	collections   *collectionuc.Service
	documents     *documentuc.Service
	search        *searchuc.Service
	batch         *batchuc.Service
	usage         *usageuc.Service
	health        *healthuc.Service
	logger        *zap.Logger
	errorHandlers []errorHandler
}

var _ gen.ServerInterface = (*Server)(nil)

// NewServer creates an HTTP API server.
func NewServer(
	collections *collectionuc.Service,
	documents *documentuc.Service,
	search *searchuc.Service,
	batch *batchuc.Service,
	usage *usageuc.Service,
	health *healthuc.Service,
	logger *zap.Logger,
) *Server {
	s := &Server{
		collections: collections,
		documents:   documents,
		search:      search,
		batch:       batch,
		usage:       usage,
		health:      health,
		logger:      logger,
	}
	s.errorHandlers = []errorHandler{
		revisionConflictHandler,
		sentinelHandler(domain.ErrNotFound, http.StatusNotFound, gen.ErrorResponseCodeCollectionNotFound),
		sentinelHandler(domain.ErrDocumentNotFound, http.StatusNotFound, gen.ErrorResponseCodeDocumentNotFound),
		sentinelHandler(domain.ErrAlreadyExists, http.StatusConflict, gen.ErrorResponseCodeCollectionAlreadyExists),
		sentinelHandler(domain.ErrVectorDimMismatch, http.StatusBadRequest, gen.ErrorResponseCodeVectorDimMismatch),
		sentinelHandler(domain.ErrInvalidSchema, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed),
		sentinelHandler(domain.ErrRateLimited, http.StatusTooManyRequests, gen.ErrorResponseCodeRateLimited),
		sentinelHandler(domain.ErrEmbeddingQuotaExceeded,
			http.StatusPaymentRequired, gen.ErrorResponseCodeEmbeddingQuotaExceeded),
		sentinelHandler(domain.ErrEmbeddingProviderError,
			http.StatusBadGateway, gen.ErrorResponseCodeEmbeddingProviderError),
		sentinelHandler(domain.ErrKeywordSearchNotSupported,
			http.StatusNotImplemented, gen.ErrorResponseCodeKeywordSearchNotSupported),
		sentinelHandler(domain.ErrNotImplemented, http.StatusNotImplemented, gen.ErrorResponseCodeNotImplemented),
	}
	return s
}

// CreateCollection handles POST /collections.
func (s *Server) CreateCollection(w http.ResponseWriter, r *http.Request, params gen.CreateCollectionParams) {
	var req gen.CreateCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, "Collection name is required")
		return
	}

	fields, err := fieldsFromGen(req.Fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, err.Error())
		return
	}

	col, err := s.collections.Create(r.Context(), req.Name, fields)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, collectionToGen(col))
}

// ListCollections handles GET /collections.
func (s *Server) ListCollections(w http.ResponseWriter, r *http.Request, params gen.ListCollectionsParams) {
	cols, err := s.collections.List(r.Context())
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	items := make([]gen.Collection, len(cols))
	for i, c := range cols {
		items[i] = collectionToGen(c)
	}

	resp := paginateCollections(items, params.Cursor, params.Limit)
	writeJSON(w, http.StatusOK, resp)
}

func paginateCollections(items []gen.Collection, cursor *string, limitPtr *int) gen.CollectionCursorListResponse {
	limit := 20
	if limitPtr != nil {
		limit = *limitPtr
	}

	startIdx := 0
	if cursor != nil && *cursor != "" {
		for i, item := range items {
			if item.Name == *cursor {
				startIdx = i + 1
				break
			}
		}
	}

	if startIdx > len(items) {
		startIdx = len(items)
	}
	end := startIdx + limit
	if end > len(items) {
		end = len(items)
	}

	page := items[startIdx:end]
	hasMore := end < len(items)

	resp := gen.CollectionCursorListResponse{
		Items:   page,
		HasMore: hasMore,
	}
	if hasMore && len(page) > 0 {
		c := page[len(page)-1].Name
		resp.NextCursor = &c
	}
	return resp
}

// GetCollection handles GET /collections/{collection}.
func (s *Server) GetCollection(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.GetCollectionParams,
) {
	col, err := s.collections.Get(r.Context(), collection)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	resp := collectionToGen(col)

	count, err := s.documents.Count(r.Context(), collection)
	if err == nil {
		resp.DocumentCount = &count
	}

	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(col.Revision())))
	writeJSON(w, http.StatusOK, resp)
}

// DeleteCollection handles DELETE /collections/{collection}.
func (s *Server) DeleteCollection(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.DeleteCollectionParams,
) {
	if err := s.collections.Delete(r.Context(), collection); err != nil {
		s.handleDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpsertDocument handles PUT /collections/{collection}/documents/{id}.
func (s *Server) UpsertDocument(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	id gen.DocumentId,
	params gen.UpsertDocumentParams,
) {
	var req gen.UpsertDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	doc, err := documentFromUpsert(id, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, err.Error())
		return
	}

	ctx, usage := domain.NewContextWithUsage(r.Context())
	created, err := s.documents.Upsert(ctx, collection, &doc)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", fmt.Sprintf("/api/v1/collections/%s/documents/%s", collection, id))
	}
	setEmbeddingHeaders(w, usage)

	writeJSON(w, status, documentToGen(&doc))
}

// GetDocument handles GET /collections/{collection}/documents/{id}.
func (s *Server) GetDocument(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	id gen.DocumentId,
	params gen.GetDocumentParams,
) {
	doc, err := s.documents.Get(r.Context(), collection, id)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(doc.Revision())))
	writeJSON(w, http.StatusOK, documentToGen(&doc))
}

// DeleteDocument handles DELETE /collections/{collection}/documents/{id}.
func (s *Server) DeleteDocument(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	id gen.DocumentId,
	params gen.DeleteDocumentParams,
) {
	if err := s.documents.Delete(r.Context(), collection, id); err != nil {
		s.handleDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListDocuments handles GET /collections/{collection}/documents.
func (s *Server) ListDocuments(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.ListDocumentsParams,
) {
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	limit := 20
	if params.Limit != nil {
		limit = *params.Limit
	}

	docs, nextCursor, err := s.documents.List(r.Context(), collection, cursor, limit)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	items := make([]gen.DocumentResponse, len(docs))
	for i, d := range docs {
		items[i] = documentToGen(&d)
	}

	resp := gen.DocumentCursorListResponse{
		Items:   items,
		HasMore: nextCursor != "",
	}
	if nextCursor != "" {
		resp.NextCursor = &nextCursor
	}

	writeJSON(w, http.StatusOK, resp)
}

// PatchDocument handles PATCH /collections/{collection}/documents/{id}.
func (s *Server) PatchDocument(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	id gen.DocumentId,
	params gen.PatchDocumentParams,
) {
	var req gen.PatchDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	p, err := patchFromGen(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, err.Error())
		return
	}

	ctx, usage := domain.NewContextWithUsage(r.Context())
	doc, err := s.documents.Patch(ctx, collection, id, p)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	setEmbeddingHeaders(w, usage)
	writeJSON(w, http.StatusOK, documentToGen(&doc))
}

// SearchDocuments handles POST /collections/{collection}/search.
func (s *Server) SearchDocuments(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.SearchDocumentsParams,
) {
	var req gen.SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	searchReq, err := searchRequestFromGen(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, err.Error())
		return
	}

	ctx, usage := domain.NewContextWithUsage(r.Context())
	results, err := s.search.Search(ctx, collection, &searchReq)
	if err != nil {
		s.handleDomainError(w, err)
		return
	}

	items := make([]gen.SearchResultItem, len(results))
	for i := range results {
		items[i] = searchResultToGen(&results[i])
	}

	limit := len(items)
	if req.Limit != nil {
		limit = *req.Limit
	}

	setEmbeddingHeaders(w, usage)
	writeJSON(w, http.StatusOK, gen.SearchResultListResponse{
		Items: items,
		Limit: limit,
		Total: len(items),
	})
}

// BatchUpsert handles POST /collections/{collection}/documents/batch.
func (s *Server) BatchUpsert(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.BatchUpsertParams,
) {
	var req gen.BatchUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Documents) == 0 || len(req.Documents) > maxBatchSize {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed,
			fmt.Sprintf("documents count must be between 1 and %d", maxBatchSize))
		return
	}

	docs := make([]domdoc.Document, 0, len(req.Documents))
	for _, item := range req.Documents {
		doc, err := batchItemToDoc(item)
		if err != nil {
			writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed, err.Error())
			return
		}
		docs = append(docs, doc)
	}

	results := s.batch.Upsert(r.Context(), collection, docs)

	succeeded, failed := 0, 0
	items := make([]gen.BatchResultItem, len(results))
	for i, res := range results {
		items[i] = batchResultToGen(res)
		if res.Status() == dombatch.StatusOK {
			succeeded++
		} else {
			failed++
		}
	}

	writeJSON(w, http.StatusOK, gen.BatchUpsertResponse{
		Items:     items,
		Succeeded: succeeded,
		Failed:    failed,
	})
}

// BatchDelete handles DELETE /collections/{collection}/documents/batch.
func (s *Server) BatchDelete(
	w http.ResponseWriter,
	r *http.Request,
	collection gen.CollectionName,
	params gen.BatchDeleteParams,
) {
	var req gen.BatchDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Ids) == 0 || len(req.Ids) > maxBatchSize {
		writeError(w, http.StatusBadRequest, gen.ErrorResponseCodeValidationFailed,
			fmt.Sprintf("ids count must be between 1 and %d", maxBatchSize))
		return
	}

	results := s.batch.Delete(r.Context(), collection, req.Ids)

	succeeded, failed := 0, 0
	items := make([]gen.BatchResultItem, len(results))
	for i, res := range results {
		items[i] = batchResultToGen(res)
		if res.Status() == dombatch.StatusOK {
			succeeded++
		} else {
			failed++
		}
	}

	writeJSON(w, http.StatusOK, gen.BatchDeleteResponse{
		Items:     items,
		Succeeded: succeeded,
		Failed:    failed,
	})
}

// GetUsage handles GET /usage.
func (s *Server) GetUsage(w http.ResponseWriter, r *http.Request, params gen.GetUsageParams) {
	period := domusage.PeriodMonth
	if params.Period != nil {
		switch *params.Period {
		case gen.GetUsageParamsPeriodDay:
			period = domusage.PeriodDay
		case gen.GetUsageParamsPeriodTotal:
			period = domusage.PeriodTotal
		}
	}

	report := s.usage.GetReport(r.Context(), period)

	isExhausted := report.Budget().IsExhausted()
	resp := gen.UsageResponse{
		Period: gen.UsageResponsePeriod(report.Period()),
		Usage: gen.UsageMetrics{
			EmbeddingRequests: report.Metrics().EmbeddingRequests(),
			Tokens:            report.Metrics().Tokens(),
		},
		Budget: gen.BudgetStatus{
			TokensLimit:     report.Budget().TokensLimit(),
			TokensRemaining: report.Budget().TokensRemaining(),
			IsExhausted:     &isExhausted,
		},
	}

	if report.Metrics().CostMillidollars() > 0 {
		cost := report.Metrics().CostMillidollars()
		resp.Usage.CostMillidollars = &cost
	}

	if report.PeriodStart() > 0 {
		start := time.UnixMilli(report.PeriodStart()).UTC()
		end := time.UnixMilli(report.PeriodEnd()).UTC()
		resp.PeriodStartAt = &start
		resp.PeriodEndAt = &end
	}

	if report.Budget().ResetsAt() > 0 {
		resetsAt := time.UnixMilli(report.Budget().ResetsAt()).UTC()
		resp.Budget.ResetsAt = &resetsAt
	}

	if params.Collection != nil {
		resp.Collection = params.Collection
	}

	writeJSON(w, http.StatusOK, resp)
}

// HealthCheck handles GET /health.
func (s *Server) HealthCheck(w http.ResponseWriter, r *http.Request) {
	report := s.health.Check(r.Context())

	checks := make(map[string]gen.HealthResponseChecks)
	for k, v := range report.Checks {
		checks[k] = gen.HealthResponseChecks(v)
	}

	status := gen.HealthResponseStatus(report.Status)
	httpStatus := http.StatusOK
	if report.Status != healthuc.Healthy {
		httpStatus = http.StatusServiceUnavailable
	}

	writeJSON(w, httpStatus, gen.HealthResponse{
		Status: status,
		Checks: checks,
	})
}

// Metrics handles GET /metrics.
func (s *Server) Metrics(w http.ResponseWriter, r *http.Request) {
	promhttp.Handler().ServeHTTP(w, r)
}

func setEmbeddingHeaders(w http.ResponseWriter, usage *domain.EmbeddingUsage) {
	if usage != nil && usage.Used {
		w.Header().Set("X-Embedding-Tokens", strconv.Itoa(usage.TotalTokens))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code gen.ErrorResponseCode, message string) {
	writeJSON(w, status, gen.ErrorResponse{
		Code:    code,
		Message: message,
	})
}

// safeDomainMessage returns a sentinel error message for the client without exposing internals.
func safeDomainMessage(err error) string {
	sentinels := []error{
		domain.ErrNotFound,
		domain.ErrDocumentNotFound,
		domain.ErrAlreadyExists,
		domain.ErrRevisionConflict,
		domain.ErrVectorDimMismatch,
		domain.ErrInvalidSchema,
		domain.ErrRateLimited,
		domain.ErrEmbeddingQuotaExceeded,
		domain.ErrEmbeddingProviderError,
		domain.ErrKeywordSearchNotSupported,
		domain.ErrNotImplemented,
	}
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	return "internal error"
}

// sentinelHandler returns an errorHandler that matches a single sentinel error.
func sentinelHandler(sentinel error, status int, code gen.ErrorResponseCode) errorHandler {
	return func(w http.ResponseWriter, err error, msg string) bool {
		if !errors.Is(err, sentinel) {
			return false
		}
		writeError(w, status, code, msg)
		return true
	}
}

// revisionConflictHandler handles ErrRevisionConflict with ETag header and extra fields.
func revisionConflictHandler(w http.ResponseWriter, err error, msg string) bool {
	if !errors.Is(err, domain.ErrRevisionConflict) {
		return false
	}
	var rce *domain.RevisionConflictError
	if errors.As(err, &rce) {
		w.Header().Set("ETag", strconv.Quote(strconv.Itoa(rce.CurrentRevision)))
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":             gen.ErrorResponseCodeRevisionConflict,
			"message":          msg,
			"current_revision": rce.CurrentRevision,
		})
		return true
	}
	writeError(w, http.StatusConflict, gen.ErrorResponseCodeRevisionConflict, msg)
	return true
}

func (s *Server) handleDomainError(w http.ResponseWriter, err error) {
	s.logger.Warn("domain error", zap.Error(err))
	msg := safeDomainMessage(err)
	for _, h := range s.errorHandlers {
		if h(w, err, msg) {
			return
		}
	}
	s.logger.Error("internal error", zap.Error(err))
	writeError(w, http.StatusInternalServerError, gen.ErrorResponseCodeInternalError, "internal error")
}

func collectionToGen(c domcol.Collection) gen.Collection {
	var fields *[]gen.FieldDefinition
	if len(c.Fields()) > 0 {
		f := make([]gen.FieldDefinition, len(c.Fields()))
		for i, ff := range c.Fields() {
			f[i] = gen.FieldDefinition{
				Name: ff.Name(),
				Type: gen.FieldDefinitionType(ff.FieldType()),
			}
		}
		fields = &f
	}

	var vectorDimensions *int
	if c.VectorDim() > 0 {
		d := c.VectorDim()
		vectorDimensions = &d
	}

	return gen.Collection{
		Name:             c.Name(),
		Fields:           fields,
		VectorDimensions: vectorDimensions,
		CreatedAt:        time.UnixMilli(c.CreatedAt()).UTC(),
		Revision:         c.Revision(),
	}
}

func fieldsFromGen(ff *[]gen.FieldDefinition) ([]field.Field, error) {
	if ff == nil {
		return nil, nil
	}
	fields := make([]field.Field, len(*ff))
	for i, f := range *ff {
		fld, err := field.New(f.Name, field.Type(f.Type))
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		fields[i] = fld
	}
	return fields, nil
}

func documentToGen(doc *domdoc.Document) gen.DocumentResponse {
	var tags *map[string]string
	if len(doc.Tags()) > 0 {
		t := doc.Tags()
		tags = &t
	}

	var numerics *map[string]float32
	if len(doc.Numerics()) > 0 {
		n := make(map[string]float32, len(doc.Numerics()))
		for k, v := range doc.Numerics() {
			n[k] = float32(v)
		}
		numerics = &n
	}

	return gen.DocumentResponse{
		Id:       doc.ID(),
		Content:  doc.Content(),
		Revision: doc.Revision(),
		Tags:     tags,
		Numerics: numerics,
	}
}

func documentFromUpsert(
	id string, req gen.UpsertDocumentRequest,
) (domdoc.Document, error) {
	tags := make(map[string]string)
	if req.Tags != nil {
		tags = *req.Tags
	}

	numerics := make(map[string]float64)
	if req.Numerics != nil {
		for k, v := range *req.Numerics {
			numerics[k] = float64(v)
		}
	}

	doc, err := domdoc.New(id, req.Content, tags, numerics)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("build document: %w", err)
	}
	return doc, nil
}

func batchItemToDoc(item gen.BatchUpsertItem) (domdoc.Document, error) {
	tags := make(map[string]string)
	if item.Tags != nil {
		tags = *item.Tags
	}

	numerics := make(map[string]float64)
	if item.Numerics != nil {
		for k, v := range *item.Numerics {
			numerics[k] = float64(v)
		}
	}

	doc, err := domdoc.New(item.Id, item.Content, tags, numerics)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("build batch item: %w", err)
	}
	return doc, nil
}

func patchFromGen(req gen.PatchDocumentRequest) (patch.Patch, error) {
	var tags map[string]*string
	if req.Tags != nil {
		tags = *req.Tags
	}

	var numerics map[string]*float64
	if req.Numerics != nil {
		numerics = make(map[string]*float64, len(*req.Numerics))
		for k, v := range *req.Numerics {
			if v == nil {
				numerics[k] = nil
			} else {
				f := float64(*v)
				numerics[k] = &f
			}
		}
	}

	p, err := patch.New(req.Content, tags, numerics)
	if err != nil {
		return patch.Patch{}, fmt.Errorf("build patch: %w", err)
	}
	return p, nil
}

func searchRequestFromGen(
	req gen.SearchRequest,
) (request.Request, error) {
	var m mode.Mode
	if req.Mode != nil {
		m = mode.Mode(*req.Mode)
	}

	filters, err := filtersFromGen(req.Filters)
	if err != nil {
		return request.Request{}, fmt.Errorf("parse filters: %w", err)
	}

	// Validate explicitly provided parameters (0 from derefInt means "not set").
	if req.TopK != nil {
		if *req.TopK <= 0 || *req.TopK > request.MaxTopK {
			return request.Request{}, fmt.Errorf("top_k must be between 1 and %d", request.MaxTopK)
		}
	}
	if req.Limit != nil {
		if *req.Limit <= 0 || *req.Limit > request.MaxLimit {
			return request.Request{}, fmt.Errorf("limit must be between 1 and %d", request.MaxLimit)
		}
	}

	topK := derefInt(req.TopK)
	limit := derefInt(req.Limit)
	minScore := derefFloat(req.MinScore)
	includeVectors := derefBool(req.IncludeVectors)

	r, err := request.New(
		req.Query, m, filters, topK, limit, minScore, includeVectors,
	)
	if err != nil {
		return request.Request{}, fmt.Errorf("build search request: %w", err)
	}
	return r, nil
}

func filtersFromGen(
	f *gen.FilterExpression,
) (filter.Expression, error) {
	if f == nil {
		return filter.Expression{}, nil
	}

	must, err := conditionsFromGen(f.Must)
	if err != nil {
		return filter.Expression{}, err
	}
	should, err := conditionsFromGen(f.Should)
	if err != nil {
		return filter.Expression{}, err
	}
	mustNot, err := conditionsFromGen(f.MustNot)
	if err != nil {
		return filter.Expression{}, err
	}

	expr, err := filter.NewExpression(must, should, mustNot)
	if err != nil {
		return filter.Expression{}, fmt.Errorf("new expression: %w", err)
	}
	return expr, nil
}

func conditionsFromGen(
	cs *[]gen.FilterCondition,
) ([]filter.Condition, error) {
	if cs == nil {
		return nil, nil
	}
	out := make([]filter.Condition, 0, len(*cs))
	for _, c := range *cs {
		cond, err := filterConditionFromGen(c)
		if err != nil {
			return nil, err
		}
		out = append(out, cond)
	}
	return out, nil
}

func filterConditionFromGen(
	c gen.FilterCondition,
) (filter.Condition, error) {
	if c.Match != nil && c.Range != nil {
		return filter.Condition{},
			fmt.Errorf("filter condition for %q must have match or range, not both", c.Key)
	}
	if c.Match != nil {
		cond, err := filter.NewMatch(c.Key, *c.Match)
		if err != nil {
			return filter.Condition{}, fmt.Errorf("match filter: %w", err)
		}
		return cond, nil
	}
	if c.Range != nil {
		return rangeConditionFromGen(c.Key, c.Range)
	}
	return filter.Condition{},
		errors.New("filter condition must have either match or range")
}

func rangeConditionFromGen(
	key string, r *gen.RangeFilter,
) (filter.Condition, error) {
	gt := f64Ptr(r.Gt)
	gte := f64Ptr(r.Gte)
	lt := f64Ptr(r.Lt)
	lte := f64Ptr(r.Lte)

	rf, err := filter.NewRangeFilter(gt, gte, lt, lte)
	if err != nil {
		return filter.Condition{}, fmt.Errorf("range filter: %w", err)
	}
	cond, err := filter.NewRange(key, rf)
	if err != nil {
		return filter.Condition{}, fmt.Errorf("range condition: %w", err)
	}
	return cond, nil
}

func f64Ptr(v *float32) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func searchResultToGen(r *result.Result) gen.SearchResultItem {
	item := gen.SearchResultItem{
		Id:      r.ID(),
		Score:   r.Score(),
		Content: r.Content(),
	}

	if len(r.Tags()) > 0 {
		t := r.Tags()
		item.Tags = &t
	}

	if len(r.Numerics()) > 0 {
		n := make(map[string]float32, len(r.Numerics()))
		for k, v := range r.Numerics() {
			n[k] = float32(v)
		}
		item.Numerics = &n
	}

	if len(r.Vector()) > 0 {
		v := r.Vector()
		item.Vector = &v
	}

	return item
}

func batchResultToGen(r dombatch.Result) gen.BatchResultItem {
	item := gen.BatchResultItem{
		Id:     r.ID(),
		Status: gen.BatchResultItemStatus(r.Status()),
	}
	if r.Err() != nil {
		errResp := gen.ErrorResponse{
			Code:    batchErrorCode(r.Err()),
			Message: safeDomainMessage(r.Err()),
		}
		item.Error = &errResp
	}
	return item
}

func batchErrorCode(err error) gen.ErrorResponseCode {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return gen.ErrorResponseCodeCollectionNotFound
	case errors.Is(err, domain.ErrDocumentNotFound):
		return gen.ErrorResponseCodeDocumentNotFound
	case errors.Is(err, domain.ErrVectorDimMismatch):
		return gen.ErrorResponseCodeVectorDimMismatch
	case errors.Is(err, domain.ErrInvalidSchema):
		return gen.ErrorResponseCodeValidationFailed
	case errors.Is(err, domain.ErrEmbeddingQuotaExceeded):
		return gen.ErrorResponseCodeEmbeddingQuotaExceeded
	case errors.Is(err, domain.ErrEmbeddingProviderError):
		return gen.ErrorResponseCodeEmbeddingProviderError
	case errors.Is(err, domain.ErrRateLimited):
		return gen.ErrorResponseCodeRateLimited
	default:
		return gen.ErrorResponseCodeInternalError
	}
}
