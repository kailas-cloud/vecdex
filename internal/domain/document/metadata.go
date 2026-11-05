package document

import (
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
)

// Metadata returns the document's complete immutable JSON metadata.
func (d *Document) Metadata() metadata.Object { return d.metadata }

// SetMetadata attaches validated JSON without duplicating it into fragments.
func (d *Document) SetMetadata(value metadata.Object) { d.metadata = value }

// ProjectMetadata derives indexed fields after collection schema validation.
func (d *Document) ProjectMetadata(fields []field.Field) error {
	if !d.metadata.Present() {
		return nil
	}
	tags, numerics, err := d.metadata.Project(fields)
	if err != nil {
		return fmt.Errorf("project document metadata: %w", err)
	}
	d.tags, d.numerics = tags, numerics
	return nil
}
