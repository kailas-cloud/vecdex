// Package mcp exposes Vecdex's agent contract over Streamable HTTP.
package mcp

import (
	"context"
	"time"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	"github.com/kailas-cloud/vecdex/internal/usecase/health"
)

// Principal identifies a stable owner independently of key rotation.
type Principal struct {
	KeyID string
	Role  string
}
type principalKey struct{}

// WithPrincipal is called by authenticated HTTP middleware, never from MCP input.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the principal established by the HTTP boundary.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok && p.KeyID != "" && (p.Role == "read" || p.Role == "write" || p.Role == "admin")
}

// CollectionService is the engine boundary required by the transport.
type CollectionService interface {
	ListPage(context.Context, string, int) ([]domcol.Collection, string, error)
	Get(context.Context, string) (domcol.Collection, error)
	Inspect(context.Context, string) (domcol.Inspection, error)
	Create(context.Context, string, domcol.Type, []field.Field) (domcol.Collection, error)
	DeleteIfGeneration(context.Context, string, string) error
}

// DocumentService is the engine boundary required by the transport.
type DocumentService interface {
	ReadSource(context.Context, string, string, domdoc.SourceProjection) (domdoc.Document, error)
	ListSources(context.Context, string, string, int, domdoc.SourceProjection) ([]domdoc.Document, string, error)
	Upsert(context.Context, string, *domdoc.Document) (bool, error)
	Patch(context.Context, string, string, patch.Patch) (domdoc.WriteResult, error)
	Delete(context.Context, string, string) error
}

// SearchService is the engine boundary required by the transport.
type SearchService interface {
	Search(context.Context, string, *request.Request) ([]result.Result, int, error)
	Similar(context.Context, string, string, *request.SimilarRequest) ([]result.Result, int, error)
}

// UsageService is the engine boundary required by the transport.
type UsageService interface {
	GetReport(context.Context, domusage.Period) (domusage.Report, error)
}

// HealthService is the engine boundary required by the transport.
type HealthService interface {
	Check(context.Context) health.Report
}

// ContextService is the engine boundary required by the transport.
type ContextService interface {
	Create(context.Context, memorycontext.CreateRequest) (memorycontext.MutationResult, error)
	CreateValidated(context.Context,
		memorycontext.CreateRequest,
		func(context.Context) error) (memorycontext.MutationResult,
		error)
	Get(context.Context, string, string) (memorycontext.Context, error)
	List(context.Context, memorycontext.ListRequest) (memorycontext.Page, error)
	Update(context.Context, memorycontext.UpdateRequest) (memorycontext.MutationResult, error)
	Delete(context.Context, memorycontext.DeleteRequest) (memorycontext.MutationResult, error)
}

// Dependencies are shared use cases; the transport never reaches storage directly.
type Dependencies struct {
	Collections CollectionService
	Documents   DocumentService
	Search      SearchService
	Usage       UsageService
	Health      HealthService
	Contexts    ContextService
}

// Skill contains the complete embedded skill bundle. Files are relative paths;
// Content is the exact SKILL.md text, including its YAML frontmatter.
type Skill struct {
	Name, Description, Content string
	Files                      map[string][]byte
}

// Options configures response and subscription budgets.
type Options struct {
	Version         string
	Skills          []Skill
	MaxResultBytes  int
	MaxRequestBytes int64
	PollInterval    time.Duration
	Observer        func(context.Context, Event)
}

// Event reports bounded-cardinality transport telemetry. ResponseBytes measures
// serialized MCP result bytes before the SDK's protocol enrichment and framing.
type Event struct {
	Method, Tool, Outcome         string
	Duration                      time.Duration
	ResponseBytes                 int
	Conflict, Replayed, Truncated bool
	SubscriptionDelta             int
	PollError                     bool
}
