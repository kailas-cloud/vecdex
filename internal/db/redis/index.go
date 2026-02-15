package redis

import (
	"context"
	"errors"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// CreateIndex creates an FT index from the given definition.
func (s *Store) CreateIndex(ctx context.Context, def *db.IndexDefinition) error {
	args, err := buildCreateArgs(def)
	if err != nil {
		return err
	}

	cmd := s.b().Arbitrary("FT.CREATE").Args(args...).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		if isRedisErr(err, "index already exists") {
			return db.ErrIndexExists
		}
		return &db.Error{Op: db.OpCreateIndex, Err: err}
	}
	return nil
}

// DropIndex removes an FT index by name.
func (s *Store) DropIndex(ctx context.Context, name string) error {
	cmd := s.b().Arbitrary("FT.DROPINDEX").Args(name).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		if isRedisErr(err, "unknown index name") {
			return db.ErrIndexNotFound
		}
		return &db.Error{Op: db.OpDropIndex, Err: err}
	}
	return nil
}

// IndexExists probes index existence via FT.INFO; "unknown index name" means absent.
func (s *Store) IndexExists(ctx context.Context, name string) (bool, error) {
	cmd := s.b().Arbitrary("FT.INFO").Args(name).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		if isRedisErr(err, "unknown index name") {
			return false, nil
		}
		return false, &db.Error{Op: db.OpIndexInfo, Err: err}
	}
	return true, nil
}

// SupportsTextSearch returns true: Redis 8+ supports TEXT fields and BM25 scoring.
func (s *Store) SupportsTextSearch(_ context.Context) bool {
	return true
}

func buildCreateArgs(idx *db.IndexDefinition) ([]string, error) {
	if idx.Name == "" {
		return nil, errors.New("index name is required")
	}
	if len(idx.Fields) == 0 {
		return nil, errors.New("at least one field is required")
	}

	args := []string{idx.Name}

	storage := idx.StorageType
	if storage == "" {
		storage = db.StorageHash
	}
	args = append(args, "ON", string(storage))

	if len(idx.Prefixes) > 0 {
		args = append(args, "PREFIX", strconv.Itoa(len(idx.Prefixes)))
		args = append(args, idx.Prefixes...)
	}

	args = append(args, "SCHEMA")

	for i := range idx.Fields {
		fieldArgs, err := buildFieldArgs(&idx.Fields[i])
		if err != nil {
			return nil, err
		}
		args = append(args, fieldArgs...)
	}

	return args, nil
}

func buildFieldArgs(f *db.IndexField) ([]string, error) {
	if f.Name == "" {
		return nil, errors.New("field name is required")
	}

	args := []string{f.Name}

	if f.Alias != "" {
		args = append(args, "AS", f.Alias)
	}

	switch f.Type {
	case db.IndexFieldNumeric:
		args = append(args, "NUMERIC")

	case db.IndexFieldText:
		args = append(args, "TEXT")

	case db.IndexFieldTag:
		args = append(args, "TAG")
		if f.TagSeparator != "" {
			args = append(args, "SEPARATOR", f.TagSeparator)
		}
		if f.TagCaseSensitive {
			args = append(args, "CASESENSITIVE")
		}

	case db.IndexFieldVector:
		vectorArgs, err := buildVectorFieldArgs(f)
		if err != nil {
			return nil, err
		}
		args = append(args, vectorArgs...)

	default:
		return nil, errors.New("unknown field type")
	}

	return args, nil
}

func buildVectorFieldArgs(f *db.IndexField) ([]string, error) {
	if f.VectorDim <= 0 {
		return nil, errors.New("vector DIM must be positive")
	}

	algo := f.VectorAlgo
	if algo == "" {
		algo = db.VectorFlat
	}

	distance := f.VectorDistance
	if distance == "" {
		distance = db.DistanceCosine
	}

	attrs := []string{
		"TYPE", "FLOAT32",
		"DIM", strconv.Itoa(f.VectorDim),
		"DISTANCE_METRIC", string(distance),
	}

	switch algo {
	case db.VectorHNSW:
		if f.VectorM > 0 {
			attrs = append(attrs, "M", strconv.Itoa(f.VectorM))
		}
		if f.VectorEFConstruct > 0 {
			attrs = append(attrs, "EF_CONSTRUCTION", strconv.Itoa(f.VectorEFConstruct))
		}
	case db.VectorFlat:
		if f.VectorBlockSize > 0 {
			attrs = append(attrs, "BLOCK_SIZE", strconv.Itoa(f.VectorBlockSize))
		}
	}

	result := make([]string, 0, 3+len(attrs))
	result = append(result, "VECTOR", string(algo), strconv.Itoa(len(attrs)))
	result = append(result, attrs...)

	return result, nil
}
