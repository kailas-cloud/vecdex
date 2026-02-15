package collection

import (
	"context"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

// Repository defines the storage contract for collections.
type Repository interface {
	Create(ctx context.Context, col domcol.Collection) error
	Get(ctx context.Context, name string) (domcol.Collection, error)
	List(ctx context.Context) ([]domcol.Collection, error)
	Delete(ctx context.Context, name string) error
}
