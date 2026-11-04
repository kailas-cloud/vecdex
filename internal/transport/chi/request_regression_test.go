package chi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	"go.uber.org/zap"
)

func TestCollectionLimitRejectedBeforeServiceCall(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, zap.NewNop())
	for _, limit := range []int{-1, 0, 101} {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(context.Background(), "GET", "/collections", http.NoBody)
		server.ListCollections(w, r, gen.ListCollectionsParams{Limit: &limit})
		if w.Code != 400 {
			t.Fatalf("limit %d returned %d", limit, w.Code)
		}
	}
}

func TestSimilarParsesChunkedBody(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, zap.NewNop())
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(
		context.Background(), "POST", "/collections/test/documents/doc/similar", strings.NewReader("{broken"),
	)
	r.ContentLength = -1
	server.FindSimilarDocuments(w, r, "test", "doc", gen.FindSimilarDocumentsParams{})
	if w.Code != 400 {
		t.Fatalf("invalid chunked JSON returned %d", w.Code)
	}
}
