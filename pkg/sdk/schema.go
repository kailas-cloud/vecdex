package vecdex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const tagKey = "vecdex"

// schemaMeta holds parsed struct tag metadata, cached per TypedIndex.
type schemaMeta struct {
	typ     reflect.Type // struct type for reconstruction
	colType CollectionType

	// Field index in the struct for each role.
	idIdx      int
	contentIdx int // -1 if not present

	// Schema fields for collection creation.
	fields []FieldInfo

	// Mapping from struct field index to a JSON metadata member.
	tagFields     []fieldMapping
	numericFields []fieldMapping
}

type fieldMapping struct {
	structIdx int
	name      string
}

// parseSchema reflects on T and extracts vecdex struct tag metadata.
func parseSchema[T any]() (*schemaMeta, error) {
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("vecdex: type %s is not a struct", t)
	}

	meta := &schemaMeta{
		typ: t, colType: CollectionTypeText,
		idIdx: -1, contentIdx: -1,
	}

	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get(tagKey)
		if tag == "" || tag == "-" {
			continue
		}
		if err := applyTag(meta, i, f.Name, tag); err != nil {
			return nil, err
		}
	}

	return validateSchema(meta, t)
}

// applyTag processes a single struct field's vecdex tag.
func applyTag(meta *schemaMeta, idx int, fieldName, tag string) error {
	parts := strings.SplitN(tag, ",", 2)
	name := parts[0]
	modifier := ""
	if len(parts) == 2 {
		modifier = parts[1]
	}

	switch modifier {
	case "id":
		if meta.idIdx != -1 {
			return fmt.Errorf("vecdex: duplicate id tag on field %s", fieldName)
		}
		meta.idIdx = idx
	case "content":
		if meta.contentIdx != -1 {
			return fmt.Errorf("vecdex: duplicate content tag on field %s", fieldName)
		}
		meta.contentIdx = idx
	case "tag":
		meta.fields = append(meta.fields, FieldInfo{Name: name, Type: FieldTag})
		meta.tagFields = append(meta.tagFields, fieldMapping{structIdx: idx, name: name})
	case "numeric":
		addNumeric(meta, idx, name)
	case "stored":
		// Stored metadata is retained as JSON without an index entry.
		meta.tagFields = append(meta.tagFields, fieldMapping{structIdx: idx, name: name})
	case "":
		// Поле без модификатора — mapped name, не индексируется.
	default:
		return fmt.Errorf("vecdex: unknown modifier %q on field %s", modifier, fieldName)
	}
	return nil
}

func addNumeric(meta *schemaMeta, idx int, name string) {
	meta.fields = append(meta.fields, FieldInfo{Name: name, Type: FieldNumeric})
	meta.numericFields = append(meta.numericFields, fieldMapping{structIdx: idx, name: name})
}

func validateSchema(meta *schemaMeta, t reflect.Type) (*schemaMeta, error) {
	if meta.idIdx == -1 {
		return nil, fmt.Errorf("vecdex: no field with `vecdex:\"...,id\"` tag in %s", t)
	}
	return meta, nil
}

// collectionOptions builds CollectionOption slice from parsed schema.
func (m *schemaMeta) collectionOptions() []CollectionOption {
	opts := make([]CollectionOption, 0, len(m.fields)+1)
	opts = append(opts, Text())
	for _, f := range m.fields {
		opts = append(opts, WithField(f.Name, f.Type))
	}
	return opts
}

// toDocument converts a typed struct to Document using schema metadata.
func (m *schemaMeta) toDocument(item any) Document {
	v := reflect.ValueOf(item)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}

	id := fmt.Sprint(v.Field(m.idIdx).Interface())

	var content string
	if m.contentIdx != -1 {
		content = fmt.Sprint(v.Field(m.contentIdx).Interface())
	}

	values := make(map[string]any, len(m.tagFields)+len(m.numericFields))
	for _, tf := range m.tagFields {
		values[tf.name] = v.Field(tf.structIdx).Interface()
	}

	for _, nf := range m.numericFields {
		values[nf.name] = v.Field(nf.structIdx).Interface()
	}

	return Document{
		ID: id, Content: content,
		Metadata: values,
	}
}

// fromDocument restores typed JSON metadata without losing integer precision.
func (m *schemaMeta) fromDocument(doc Document) (any, error) {
	v := reflect.New(m.typ).Elem()
	v.Field(m.idIdx).SetString(doc.ID)
	if m.contentIdx != -1 {
		v.Field(m.contentIdx).SetString(doc.Content)
	}
	for _, fields := range [][]fieldMapping{m.tagFields, m.numericFields} {
		for _, f := range fields {
			value, present := doc.Metadata[f.name]
			if !present {
				continue
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("encode metadata %q: %w", f.name, err)
			}
			target := v.Field(f.structIdx)
			if err := json.Unmarshal(raw, target.Addr().Interface()); err != nil {
				return nil, fmt.Errorf("decode metadata %q into %s: %w", f.name, target.Type(), err)
			}
		}
	}
	return v.Interface(), nil
}
