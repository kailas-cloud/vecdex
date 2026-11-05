// Package metadata preserves arbitrary JSON objects and projects declared indexes.
package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// MaxSize bounds one document's encoded metadata, independently of its text.
const MaxSize = 64 * 1024

// Object is an immutable JSON object. Its zero value represents absent metadata.
// Numbers retain their original JSON precision until explicitly indexed.
type Object struct{ data *objectData }

type objectData struct {
	raw    string
	values map[string]any
}

// Parse accepts a JSON object and retains nested values, arrays and nulls.
func Parse(raw []byte) (Object, error) {
	if len(raw) > MaxSize {
		return Object{}, fmt.Errorf("metadata exceeds %d bytes: %w", MaxSize, domain.ErrInvalidSchema)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return Object{}, fmt.Errorf("metadata must be a JSON object: %w: %w", domain.ErrInvalidSchema, err)
	}
	if values == nil || !json.Valid(raw) {
		return Object{}, fmt.Errorf("metadata must be a JSON object: %w", domain.ErrInvalidSchema)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return Object{}, fmt.Errorf("encode metadata: %w", err)
	}
	if len(encoded) > MaxSize {
		return Object{}, fmt.Errorf("encoded metadata exceeds %d bytes: %w", MaxSize, domain.ErrInvalidSchema)
	}
	return Object{data: &objectData{raw: string(encoded), values: values}}, nil
}

// Present distinguishes an explicit empty object from absent legacy metadata.
func (o Object) Present() bool { return o.data != nil }

// JSON returns a detached representation; the zero value encodes as an empty object.
func (o Object) JSON() []byte {
	if o.data == nil {
		return []byte("{}")
	}
	return []byte(o.data.raw)
}

// Values returns a deep copy suitable for an SDK or transport response.
func (o Object) Values() map[string]any { return cloneObject(o.members()) }

// Project creates only the string/numeric fields declared by the collection.
// Missing and null values have no index entry; other metadata remains stored.
func (o Object) Project(fields []field.Field) (tags map[string]string, numerics map[string]float64, err error) {
	tags, numerics = make(map[string]string), make(map[string]float64)
	for _, f := range fields {
		value := o.members()[f.Name()]
		if value == nil {
			continue
		}
		switch f.FieldType() {
		case field.Tag:
			text, ok := value.(string)
			if !ok {
				return nil, nil, fmt.Errorf("indexed metadata %q must be a string: %w", f.Name(), domain.ErrInvalidSchema)
			}
			tags[f.Name()] = text
		case field.Numeric:
			number, ok := value.(json.Number)
			if !ok {
				return nil, nil, fmt.Errorf("indexed metadata %q must be a number: %w", f.Name(), domain.ErrInvalidSchema)
			}
			v, err := number.Float64()
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, nil, fmt.Errorf("indexed metadata %q is outside numeric range: %w", f.Name(), domain.ErrInvalidSchema)
			}
			numerics[f.Name()] = v
		}
	}
	return tags, numerics, nil
}

// Merge applies JSON Merge Patch inside metadata. Null removes a member, arrays
// replace arrays, and a null metadata object clears the complete object.
func Merge(base Object, raw []byte) (Object, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Parse([]byte("{}"))
	}
	update, err := Parse(raw)
	if err != nil {
		return Object{}, err
	}
	values := cloneObject(base.members())
	if values == nil {
		values = make(map[string]any)
	}
	mergeObject(values, update.members())
	encoded, err := json.Marshal(values)
	if err != nil {
		return Object{}, fmt.Errorf("encode merged metadata: %w", err)
	}
	return Parse(encoded)
}

func mergeObject(base, update map[string]any) {
	for key, value := range update {
		if value == nil {
			delete(base, key)
			continue
		}
		if object, ok := value.(map[string]any); ok {
			previous, _ := base[key].(map[string]any)
			if previous == nil {
				previous = make(map[string]any)
			}
			mergeObject(previous, object)
			base[key] = previous
			continue
		}
		base[key] = cloneValue(value)
	}
}

func cloneObject(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneObject(v)
	case []any:
		result := make([]any, len(v))
		for i, element := range v {
			result[i] = cloneValue(element)
		}
		return result
	default:
		return value
	}
}

func (o Object) members() map[string]any {
	if o.data == nil {
		return nil
	}
	return o.data.values
}
