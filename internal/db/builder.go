package db

import "strings"

// IndexBuilder is a fluent builder for FT index definitions.
type IndexBuilder struct {
	def IndexDefinition
}

// NewIndex starts building an FT index definition.
func NewIndex(name string) *IndexBuilder {
	return &IndexBuilder{
		def: IndexDefinition{
			Name:        name,
			StorageType: StorageHash,
		},
	}
}

// OnJSON sets the index storage type to JSON.
func (b *IndexBuilder) OnJSON() *IndexBuilder {
	b.def.StorageType = StorageJSON
	return b
}

// OnHash sets the index storage type to HASH.
func (b *IndexBuilder) OnHash() *IndexBuilder {
	b.def.StorageType = StorageHash
	return b
}

// Prefix adds key prefixes to the index.
func (b *IndexBuilder) Prefix(prefixes ...string) *IndexBuilder {
	b.def.Prefixes = append(b.def.Prefixes, prefixes...)
	return b
}

// Numeric adds a NUMERIC field to the index.
func (b *IndexBuilder) Numeric(name string) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name: name,
		Type: IndexFieldNumeric,
	})
	return b
}

// Tag adds a TAG field to the index.
func (b *IndexBuilder) Tag(name string) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name: name,
		Type: IndexFieldTag,
	})
	return b
}

// TagWithOpts adds a TAG field with custom separator and case sensitivity.
func (b *IndexBuilder) TagWithOpts(name, separator string, caseSensitive bool) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name:             name,
		Type:             IndexFieldTag,
		TagSeparator:     separator,
		TagCaseSensitive: caseSensitive,
	})
	return b
}

// Text adds a TEXT field to the index.
func (b *IndexBuilder) Text(name string) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name: name,
		Type: IndexFieldText,
	})
	return b
}

// Vector adds a VECTOR field to the index.
func (b *IndexBuilder) Vector(name string, dim int, algo VectorAlgorithm, distance DistanceMetric) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name:           name,
		Type:           IndexFieldVector,
		VectorAlgo:     algo,
		VectorDim:      dim,
		VectorDistance: distance,
	})
	return b
}

// VectorHNSW adds a VECTOR field with HNSW algorithm.
func (b *IndexBuilder) VectorHNSW(name string, dim int, distance DistanceMetric, m, efConstruct int) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name:              name,
		Type:              IndexFieldVector,
		VectorAlgo:        VectorHNSW,
		VectorDim:         dim,
		VectorDistance:    distance,
		VectorM:           m,
		VectorEFConstruct: efConstruct,
	})
	return b
}

// VectorFlat adds a VECTOR field with FLAT algorithm.
func (b *IndexBuilder) VectorFlat(name string, dim int, distance DistanceMetric, blockSize int) *IndexBuilder {
	b.def.Fields = append(b.def.Fields, IndexField{
		Name:            name,
		Type:            IndexFieldVector,
		VectorAlgo:      VectorFlat,
		VectorDim:       dim,
		VectorDistance:  distance,
		VectorBlockSize: blockSize,
	})
	return b
}

// Build validates and returns the index definition.
func (b *IndexBuilder) Build() (*IndexDefinition, error) {
	if err := b.def.Validate(); err != nil {
		return nil, err
	}
	return &b.def, nil
}

// MustBuild calls Build and panics on error.
func (b *IndexBuilder) MustBuild() *IndexDefinition {
	def, err := b.Build()
	if err != nil {
		panic(err)
	}
	return def
}

// String returns a debug representation resembling the FT.CREATE command.
func (idx *IndexDefinition) String() string {
	parts := []string{"FT.CREATE", idx.Name}
	if idx.StorageType != "" {
		parts = append(parts, "ON", string(idx.StorageType))
	}
	if len(idx.Prefixes) > 0 {
		parts = append(parts, "PREFIX")
		parts = append(parts, idx.Prefixes...)
	}
	parts = append(parts, "SCHEMA")
	for i := range idx.Fields {
		f := &idx.Fields[i]
		parts = append(parts, f.Name)
		if f.Alias != "" {
			parts = append(parts, "AS", f.Alias)
		}
		switch f.Type {
		case IndexFieldTag:
			parts = append(parts, "TAG")
		case IndexFieldNumeric:
			parts = append(parts, "NUMERIC")
		case IndexFieldText:
			parts = append(parts, "TEXT")
		case IndexFieldVector:
			parts = append(parts, "VECTOR", string(f.VectorAlgo))
		}
	}
	return strings.Join(parts, " ")
}
