package vecdex

// CollectionOption configures collection creation.
type CollectionOption func(*collectionConfig)

type collectionConfig struct {
	colType CollectionType
	fields  []FieldInfo
}

// Geo sets the collection type to geographic (ECEF-based proximity search).
func Geo() CollectionOption {
	return func(c *collectionConfig) {
		c.colType = CollectionTypeGeo
	}
}

// Text sets the collection type to text (embedding-based semantic search).
// This is the default if no type option is provided.
func Text() CollectionOption {
	return func(c *collectionConfig) {
		c.colType = CollectionTypeText
	}
}

// WithField adds a filterable field to the collection schema.
func WithField(name string, ft FieldType) CollectionOption {
	return func(c *collectionConfig) {
		c.fields = append(c.fields, FieldInfo{Name: name, Type: ft})
	}
}
