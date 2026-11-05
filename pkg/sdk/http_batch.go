package vecdex

import (
	"context"
	"fmt"
	"net/http"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

type httpBatches struct{ *httpBackend }

func (s *httpBatches) Upsert(ctx context.Context, collection string, docs []dombatch.UpsertItem) []dombatch.Result {
	items := make([]gen.BatchUpsertItem, len(docs))
	ids := make([]string, len(docs))
	for i := range docs {
		items[i] = gen.BatchUpsertItem{Id: docs[i].ID(), Content: docs[i].Content(),
			ExpiresAt: optionalExpiresAt(docs[i].ExpiresAt()),
			Metadata:  docs[i].Metadata().JSON(), IfVersion: docs[i].IfVersion, IdempotencyKey: docs[i].IdempotencyKey}
		ids[i] = docs[i].ID()
	}
	var reply gen.BatchUpsertResponse
	_, err := s.call(ctx, http.MethodPost, collectionHTTPPath(collection)+"/documents/batch-upsert",
		gen.BatchUpsertRequest{Documents: items}, &reply)
	return batchFromHTTP(ids, reply.Items, reply.Succeeded, reply.Failed, true, err)
}

func (s *httpBatches) Delete(ctx context.Context, collection string, commands []dombatch.DeleteItem) []dombatch.Result {
	items := make([]gen.BatchDeleteItem, len(commands))
	ids := make([]string, len(commands))
	for i, command := range commands {
		items[i] = gen.BatchDeleteItem{Id: command.ID, IfVersion: command.IfVersion, IdempotencyKey: command.IdempotencyKey}
		ids[i] = command.ID
	}
	var reply gen.BatchDeleteResponse
	_, err := s.call(ctx, http.MethodPost, collectionHTTPPath(collection)+"/documents/batch-delete",
		gen.BatchDeleteRequest{Documents: items}, &reply)
	return batchFromHTTP(ids, reply.Items, reply.Succeeded, reply.Failed, false, err)
}

func batchFromHTTP(
	ids []string, items []gen.BatchResultItem, succeeded, failed int, written bool, requestErr error,
) []dombatch.Result {
	if requestErr == nil && (len(items) != len(ids) || succeeded < 0 || failed < 0 || succeeded+failed != len(ids)) {
		requestErr = fmt.Errorf("HTTP batch response has inconsistent item counts")
	}
	if requestErr != nil {
		return failedHTTPBatch(ids, requestErr)
	}
	out := make([]dombatch.Result, len(ids))
	okCount := 0
	for i := range items {
		value, err := batchItemFromHTTP(ids[i], &items[i], written)
		if err != nil {
			return failedHTTPBatch(ids, err)
		}
		out[i] = value
		if value.Status() == dombatch.StatusOK {
			okCount++
		}
	}
	if okCount != succeeded {
		return failedHTTPBatch(ids, fmt.Errorf("HTTP batch response has inconsistent success count"))
	}
	return out
}

func failedHTTPBatch(ids []string, err error) []dombatch.Result {
	out := make([]dombatch.Result, len(ids))
	for i, id := range ids {
		out[i] = dombatch.NewError(id, err)
	}
	return out
}

func batchItemFromHTTP(id string, item *gen.BatchResultItem, written bool) (dombatch.Result, error) {
	if item.Id != id {
		return dombatch.Result{}, fmt.Errorf("HTTP batch response has inconsistent item identity")
	}
	switch item.Status {
	case gen.BatchResultItemStatusError:
		if item.Error == nil || item.Version != nil {
			return dombatch.Result{}, fmt.Errorf("HTTP batch failure has inconsistent error or version")
		}
		return dombatch.NewError(item.Id, newHTTPError(http.StatusOK, "", item.Error)), nil
	case gen.BatchResultItemStatusOk:
		if item.Error != nil {
			return dombatch.Result{}, fmt.Errorf("HTTP successful batch item has an error")
		}
		if !written {
			return dombatch.NewOK(item.Id), nil
		}
		if item.Version == nil || *item.Version == "" {
			return dombatch.Result{}, fmt.Errorf("HTTP batch upsert response omitted a committed version")
		}
		return dombatch.NewWritten(item.Id, *item.Version), nil
	default:
		return dombatch.Result{}, fmt.Errorf("HTTP batch response has unknown item status")
	}
}
