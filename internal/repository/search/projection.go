package search

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func searchReturnFields(ctx context.Context, vectors, score bool) []string {
	fields := []string{"__content", "__metadata", "__parent_id", "__version"}
	fields = append(fields, domain.SearchMetadataFields(ctx)...)
	if vectors {
		fields = append(fields, "__vector")
	}
	if score {
		fields = append(fields, "__vector_score")
	}
	return fields
}
