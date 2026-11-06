//nolint:wrapcheck // The protocol boundary classifies domain errors centrally and preserves their recovery data.
package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type resourceWatch struct {
	mu    sync.Mutex
	uri   string
	ctx   context.Context
	value [32]byte
	refs  int
}

func (h *Handler) watchResources(ctx context.Context, uris []string) (func(), error) {
	keys := make([]string, 0, len(uris))
	release := func() {
		h.watchMu.Lock()
		defer h.watchMu.Unlock()
		for _, key := range keys {
			if w := h.watches[key]; w != nil {
				w.refs--
				if w.refs == 0 {
					delete(h.watches, key)
				}
			}
		}
	}
	principal, _ := PrincipalFromContext(ctx)
	for _, uri := range uris {
		raw, err := h.resourceFingerprint(ctx, uri)
		if err != nil {
			release()
			return nil, resourceURIError(uri, err)
		}
		key := uri
		if ownerScopedURI(uri) {
			key = principal.KeyID + "\x00" + uri
		}
		h.watchMu.Lock()
		watch := h.watches[key]
		if watch == nil {
			watch = &resourceWatch{uri: uri, ctx: context.WithoutCancel(ctx), value: sha256.Sum256(raw)}
			h.watches[key] = watch
		}
		watch.refs++
		keys = append(keys, key)
		h.watchMu.Unlock()
	}
	h.observe(ctx, Event{Method: "subscriptions/listen", SubscriptionDelta: 1})
	return func() { release(); h.observe(ctx, Event{Method: "subscriptions/listen", SubscriptionDelta: -1}) }, nil
}

func (h *Handler) pollResources() {
	ticker := time.NewTicker(h.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop.Done():
			return
		case <-ticker.C:
			h.watchMu.Lock()
			watches := make([]*resourceWatch, 0, len(h.watches))
			for _, w := range h.watches {
				watches = append(watches, w)
			}
			h.watchMu.Unlock()
			for _, w := range watches {
				h.pollWatch(w)
			}
		}
	}
}

func (h *Handler) pollWatch(w *resourceWatch) {
	ctx, cancel := context.WithTimeout(w.ctx, min(h.opts.PollInterval, 5*time.Second))
	stopCancel := context.AfterFunc(h.stop, cancel)
	defer stopCancel()
	defer cancel()
	raw, err := h.resourceFingerprint(ctx, w.uri)
	if err != nil {
		h.observe(ctx, Event{Method: "subscriptions/listen", PollError: true})
		raw = jsonBytes(classify(err))
	}
	current := sha256.Sum256(raw)
	w.mu.Lock()
	changed := current != w.value
	w.value = current
	w.mu.Unlock()
	if changed {
		for _, s := range h.servers {
			params := &sdk.ResourceUpdatedNotificationParams{URI: w.uri}
			if ownerScopedURI(w.uri) {
				principal, _ := PrincipalFromContext(w.ctx)
				params.SetMeta(map[string]any{notificationOwnerKey: principal.KeyID})
			}
			_ = s.ResourceUpdated(ctx, params)
		}
	}
}

// Source changes are detected from identity/version/expiry, without fetching content or metadata.
func (h *Handler) resourceFingerprint(ctx context.Context, uri string) ([]byte, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	if u.Scheme == "vecdex" && u.Host == "collections" && u.User == nil && u.Fragment == "" {
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) == 4 && parts[2] == "documents" {
			return h.sourceFingerprint(ctx, uri, parts)
		}
	}

	raw, _, err := h.readURI(ctx, uri)
	return raw, err
}

func (h *Handler) sourceFingerprint(ctx context.Context, uri string, parts []string) ([]byte, error) {
	if _, _, err := parseResourceURI(uri); err != nil {
		return nil, err
	}
	col, err := h.deps.Collections.Get(ctx, parts[0])
	if err != nil {
		return nil, err
	}
	if col.Generation() != parts[1] {
		return nil, domain.ErrCollectionGenerationMismatch
	}
	ctx = domain.WithExpectedCollectionGeneration(ctx, parts[1])
	doc, err := h.deps.Documents.ReadSource(ctx, parts[0], parts[3], domdoc.SourceProjection{})
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s\x00%s\x00%d", doc.ID(), doc.Version(), doc.ExpiresAt())), nil
}

const notificationOwnerKey = "io.vecdex/internalNotificationOwner"

// The SDK broadcasts by URI. Owner-relative URIs need an additional boundary;
// the routing marker is removed before serialization and never reaches clients.
func (h *Handler) isolateOwnerNotifications(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		params, ok := req.GetParams().(*sdk.ResourceUpdatedNotificationParams)
		if !ok {
			return next(ctx, method, req)
		}
		owner, scoped := params.GetMeta()[notificationOwnerKey].(string)
		if !scoped {
			return next(ctx, method, req)
		}
		target, present := h.listenerOwners.Load(req.GetSession())
		if !present || target != owner {
			return nil,
				nil
		}
		meta := make(map[string]any, len(params.GetMeta()))
		for key, value := range params.GetMeta() {
			if key != notificationOwnerKey {
				meta[key] = value
			}
		}
		params.SetMeta(meta)
		return next(ctx, method, req)
	}
}

func ownerScopedURI(uri string) bool {
	parsed, err := url.Parse(uri)
	return err == nil && parsed.Scheme == "vecdex" && parsed.Host == "contexts"
}
