package db

import (
	"errors"
	"strconv"
)

// StorageType defines the document storage backend for FT indexes (HASH or JSON).
type StorageType string

const (
	// StorageHash stores documents as Redis hashes.
	StorageHash StorageType = "HASH"
	// StorageJSON stores documents as JSON.
	StorageJSON StorageType = "JSON"
)

// DistanceMetric used by FT.SEARCH vector similarity queries.
type DistanceMetric string

const (
	// DistanceL2 is Euclidean distance.
	DistanceL2 DistanceMetric = "L2"
	// DistanceIP is inner product distance.
	DistanceIP DistanceMetric = "IP"
	// DistanceCosine is cosine distance.
	DistanceCosine DistanceMetric = "COSINE"
)

// VectorAlgorithm selects the indexing algorithm for vector fields in FT.CREATE.
type VectorAlgorithm string

const (
	// VectorHNSW uses the HNSW algorithm.
	VectorHNSW VectorAlgorithm = "HNSW"
	// VectorFlat uses the FLAT (brute-force) algorithm.
	VectorFlat VectorAlgorithm = "FLAT"
)

// IndexFieldType enumerates supported FT index field types.
type IndexFieldType int

const (
	// IndexFieldNumeric is a numeric field.
	IndexFieldNumeric IndexFieldType = iota
	// IndexFieldTag is a tag field.
	IndexFieldTag
	// IndexFieldText is a text field.
	IndexFieldText
	// IndexFieldVector is a vector field.
	IndexFieldVector
)

// IndexField describes a single field in an FT index schema.
type IndexField struct {
	Name  string
	Alias string // AS alias in FT.CREATE SCHEMA
	Type  IndexFieldType

	// TAG options
	TagSeparator     string
	TagCaseSensitive bool

	// VECTOR options
	VectorAlgo        VectorAlgorithm
	VectorDim         int
	VectorDistance    DistanceMetric
	VectorM           int // HNSW M parameter: max edges per node (default 16)
	VectorEFConstruct int // HNSW EF_CONSTRUCTION: build-time dynamic list size (default 200)
	VectorBlockSize   int // FLAT BLOCK_SIZE
}

// IndexDefinition is a complete FT index definition used by FT.CREATE.
type IndexDefinition struct {
	Name        string
	StorageType StorageType
	Prefixes    []string
	Fields      []IndexField
}

// Validate checks that the index definition is well-formed.
func (idx *IndexDefinition) Validate() error {
	if idx.Name == "" {
		return errors.New("index name is required")
	}
	if !IsValidIdentifier(idx.Name) {
		return errors.New("index name contains invalid characters")
	}
	if len(idx.Fields) == 0 {
		return errors.New("at least one field is required")
	}

	seen := make(map[string]bool)
	for i := range idx.Fields {
		f := &idx.Fields[i]
		if f.Name == "" {
			return errors.New("field name is required at index " + strconv.Itoa(i))
		}
		key := f.Name
		if f.Alias != "" {
			key = f.Alias
		}
		if seen[key] {
			return errors.New("duplicate field name: " + key)
		}
		seen[key] = true

		if f.Type == IndexFieldVector && f.VectorDim <= 0 {
			return errors.New("vector field requires positive DIM")
		}
	}

	return nil
}

// IsValidIdentifier returns true if s matches [a-zA-Z0-9_:-]+.
func IsValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		isSpecial := r == '_' || r == ':' || r == '-'
		if !isAlpha && !isDigit && !isSpecial {
			return false
		}
	}
	return true
}
