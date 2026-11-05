package vecdex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

type httpCollections struct{ *httpBackend }

func (s *httpCollections) Create(
	ctx context.Context, name string, kind domcol.Type, fields []field.Field,
) (domcol.Collection, error) {
	definitions := make([]gen.FieldDefinition, len(fields))
	for i, item := range fields {
		definitions[i] = gen.FieldDefinition{Name: item.Name(), Type: gen.FieldDefinitionType(item.FieldType())}
	}
	typ := gen.CreateCollectionRequestType(kind)
	var reply gen.Collection
	_, err := s.call(ctx, http.MethodPost, "/api/v2/collections",
		gen.CreateCollectionRequest{Name: name, Type: &typ, Fields: &definitions}, &reply)
	if err != nil {
		return domcol.Collection{}, err
	}
	return collectionFromHTTP(&reply)
}

func (s *httpCollections) Get(ctx context.Context, name string) (domcol.Collection, error) {
	var reply gen.Collection
	if _, err := s.call(ctx, http.MethodGet, collectionHTTPPath(name), nil, &reply); err != nil {
		return domcol.Collection{}, err
	}
	return collectionFromHTTP(&reply)
}

func (s *httpCollections) Delete(ctx context.Context, name string) error {
	_, err := s.call(ctx, http.MethodDelete, collectionHTTPPath(name), nil, nil)
	return err
}

func (s *httpCollections) List(ctx context.Context) ([]domcol.Collection, error) {
	var out []domcol.Collection
	cursor := ""
	for {
		var reply gen.CollectionCursorListResponse
		path := "/api/v2/collections?limit=100&cursor=" + url.QueryEscape(cursor)
		if _, err := s.call(ctx, http.MethodGet, path, nil, &reply); err != nil {
			return nil, err
		}
		for i := range reply.Items {
			item, err := collectionFromHTTP(&reply.Items[i])
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		if !reply.HasMore {
			return out, nil
		}
		if reply.NextCursor == nil || *reply.NextCursor == "" || *reply.NextCursor <= cursor {
			return nil, fmt.Errorf("HTTP collection page did not advance its cursor")
		}
		cursor = *reply.NextCursor
	}
}

func collectionFromHTTP(reply *gen.Collection) (domcol.Collection, error) {
	if reply.Name == "" || reply.VectorDimensions == nil || *reply.VectorDimensions <= 0 {
		return domcol.Collection{}, fmt.Errorf("HTTP collection response omitted name or dimensions")
	}
	var fields []field.Field
	if reply.Fields != nil {
		for _, item := range *reply.Fields {
			value, err := field.New(item.Name, field.Type(item.Type))
			if err != nil {
				return domcol.Collection{}, fmt.Errorf("HTTP collection field: %w", err)
			}
			fields = append(fields, value)
		}
	}
	kind := domcol.TypeText
	if reply.Type != nil {
		kind = domcol.Type(*reply.Type)
	}
	col := domcol.Reconstruct(reply.Name, kind, fields, *reply.VectorDimensions,
		reply.CreatedAt.UnixMilli(), reply.Revision)
	if reply.EmbeddingSpace != nil {
		col = col.WithEmbeddingSpace(*reply.EmbeddingSpace)
	}
	return col, nil
}
