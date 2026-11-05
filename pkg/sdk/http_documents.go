package vecdex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

type httpDocuments struct{ *httpBackend }

func (s *httpDocuments) Upsert(ctx context.Context, collection string, doc *domdoc.Document) (bool, error) {
	var reply gen.WriteResult
	status, err := s.call(ctx, http.MethodPut, documentHTTPPath(collection, doc.ID()),
		gen.UpsertDocumentRequest{Content: doc.Content(), Metadata: doc.Metadata().JSON(),
			ExpiresAt: optionalExpiresAt(doc.ExpiresAt())}, &reply)
	if err != nil {
		return false, err
	}
	written, err := writeResultFromHTTP(&reply, doc.ID())
	if err != nil {
		return false, err
	}
	if written.Created != (status == http.StatusCreated) {
		return false, fmt.Errorf("HTTP source receipt has inconsistent creation status")
	}
	doc.SetVersion(written.Version)
	return written.Created, nil
}

func (s *httpDocuments) Get(ctx context.Context, collection, id string) (domdoc.Document, error) {
	var reply gen.DocumentResponse
	if _, err := s.call(ctx, http.MethodGet, documentHTTPPath(collection, id), nil, &reply); err != nil {
		return domdoc.Document{}, err
	}
	return documentFromHTTP(&reply)
}

func (s *httpDocuments) Patch(
	ctx context.Context, collection, id string, update patch.Patch,
) (domdoc.WriteResult, error) {
	var reply gen.WriteResult
	_, err := s.call(ctx, http.MethodPatch, documentHTTPPath(collection, id),
		gen.PatchDocumentRequest{Content: update.Content(), Metadata: update.Metadata(),
			ExpiresAt: expiryPatchJSON(update.ExpiresAt())}, &reply)
	if err != nil {
		return domdoc.WriteResult{}, err
	}
	return writeResultFromHTTP(&reply, id)
}

func (s *httpDocuments) Delete(ctx context.Context, collection, id string) error {
	_, err := s.call(ctx, http.MethodDelete, documentHTTPPath(collection, id), nil, nil)
	return err
}

func (s *httpDocuments) List(
	ctx context.Context, collection, cursor string, limit int,
) ([]domdoc.Document, string, error) {
	path := collectionHTTPPath(collection) + "/documents?cursor=" + url.QueryEscape(cursor)
	if limit != 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	var reply gen.DocumentCursorListResponse
	if _, err := s.call(ctx, http.MethodGet, path, nil, &reply); err != nil {
		return nil, "", err
	}
	out := make([]domdoc.Document, len(reply.Items))
	for i := range reply.Items {
		value, err := documentFromHTTP(&reply.Items[i])
		if err != nil {
			return nil, "", err
		}
		out[i] = value
	}
	next := ""
	if reply.HasMore {
		if reply.NextCursor == nil || *reply.NextCursor == "" || *reply.NextCursor == cursor {
			return nil, "", fmt.Errorf("HTTP document page did not advance its cursor")
		}
		next = *reply.NextCursor
	}
	return out, next, nil
}

func (s *httpDocuments) Count(ctx context.Context, collection string) (int, error) {
	var reply gen.Collection
	if _, err := s.call(ctx, http.MethodGet, collectionHTTPPath(collection), nil, &reply); err != nil {
		return 0, err
	}
	if reply.DocumentCount == nil || *reply.DocumentCount < 0 {
		return 0, fmt.Errorf("HTTP collection response omitted document count")
	}
	return *reply.DocumentCount, nil
}

func documentFromHTTP(reply *gen.DocumentResponse) (domdoc.Document, error) {
	if reply.Version == "" {
		return domdoc.Document{}, fmt.Errorf("HTTP source response omitted its version")
	}
	doc, err := domdoc.New(reply.Id, reply.Content, nil, nil)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("HTTP source response: %w", err)
	}
	value, err := metadata.Parse(reply.Metadata)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("HTTP source metadata: %w", err)
	}
	doc.SetMetadata(value)
	doc.SetVersion(reply.Version)
	if reply.ExpiresAt != nil {
		if err := doc.SetExpiresAt(*reply.ExpiresAt); err != nil {
			return domdoc.Document{}, fmt.Errorf("HTTP source expiration: %w", err)
		}
	}
	return doc, nil
}

func expiryPatchJSON(value *int64) json.RawMessage {
	if value == nil {
		return nil
	}
	if *value == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(strconv.FormatInt(*value, 10))
}

func writeResultFromHTTP(reply *gen.WriteResult, id string) (domdoc.WriteResult, error) {
	if reply.Id != id || reply.Version == "" {
		return domdoc.WriteResult{}, fmt.Errorf("HTTP source receipt omitted its identity or version")
	}
	return domdoc.WriteResult{ID: reply.Id, Version: reply.Version, Created: reply.Created}, nil
}
