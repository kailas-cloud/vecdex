package collection

import (
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// buildIndex creates an IndexDefinition from domain collection fields.
// textSearchEnabled adds a TEXT field for $.__content (BM25 keyword search).
// Requires Redis 8.4+ — valkey-search 1.0.x does not support TEXT.
func buildIndex(
	name string, fields []field.Field, vectorDim int,
	textSearchEnabled bool, hnsw HNSWConfig,
) (*db.IndexDefinition, error) {
	extraFields := 1 // vector
	if textSearchEnabled {
		extraFields = 2 // vector + text
	}

	def := &db.IndexDefinition{
		Name:        indexName(name),
		StorageType: db.StorageHash,
		Prefixes:    []string{collectionPrefix(name)},
		Fields:      make([]db.IndexField, 0, len(fields)+extraFields),
	}

	for _, f := range fields {
		var fieldType db.IndexFieldType
		switch f.FieldType() {
		case field.Tag:
			fieldType = db.IndexFieldTag
			def.Fields = append(def.Fields, db.IndexField{
				Name: f.Name(),
				Type: fieldType,
			})
		case field.Numeric:
			fieldType = db.IndexFieldNumeric
			// Numerics are stored with "__n:" prefix; alias restores original name for queries.
			def.Fields = append(def.Fields, db.IndexField{
				Name:  "__n:" + f.Name(),
				Alias: f.Name(),
				Type:  fieldType,
			})
		default:
			return nil, fmt.Errorf("unknown field type: %s", f.FieldType())
		}
	}

	// TEXT field for BM25 keyword search when the backend supports it.
	if textSearchEnabled {
		def.Fields = append(def.Fields, db.IndexField{
			Name: "__content",
			Type: db.IndexFieldText,
		})
	}
	def.Fields = append(def.Fields, db.IndexField{
		Name:              "__vector",
		Alias:             "vector",
		Type:              db.IndexFieldVector,
		VectorAlgo:        db.VectorHNSW,
		VectorDim:         vectorDim,
		VectorDistance:    db.DistanceCosine,
		VectorM:           hnsw.M,
		VectorEFConstruct: hnsw.EFConstruct,
	})

	return def, nil
}
