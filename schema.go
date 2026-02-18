package vecdex

import (
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
	geoLatIdx  int // -1 if not geo
	geoLonIdx  int // -1 if not geo

	// Schema fields for collection creation.
	fields []FieldInfo

	// Mapping from struct field index → document field name (for tags/numerics).
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
		typ: t, idIdx: -1, contentIdx: -1,
		geoLatIdx: -1, geoLonIdx: -1,
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
	case "geo_lat":
		if meta.geoLatIdx != -1 {
			return fmt.Errorf("vecdex: duplicate geo_lat tag on field %s", fieldName)
		}
		meta.geoLatIdx = idx
		addNumeric(meta, idx, name)
	case "geo_lon":
		if meta.geoLonIdx != -1 {
			return fmt.Errorf("vecdex: duplicate geo_lon tag on field %s", fieldName)
		}
		meta.geoLonIdx = idx
		addNumeric(meta, idx, name)
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
	hasGeoLat := meta.geoLatIdx != -1
	hasGeoLon := meta.geoLonIdx != -1
	if hasGeoLat != hasGeoLon {
		return nil, fmt.Errorf("vecdex: geo_lat and geo_lon must both be present in %s", t)
	}
	if hasGeoLat {
		meta.colType = CollectionTypeGeo
	} else {
		meta.colType = CollectionTypeText
	}
	return meta, nil
}

// collectionOptions builds CollectionOption slice from parsed schema.
func (m *schemaMeta) collectionOptions() []CollectionOption {
	opts := make([]CollectionOption, 0, len(m.fields)+1)
	if m.colType == CollectionTypeGeo {
		opts = append(opts, Geo())
	} else {
		opts = append(opts, Text())
	}
	for _, f := range m.fields {
		opts = append(opts, WithField(f.Name, f.Type))
	}
	return opts
}

// toDocument converts a typed struct to Document using schema metadata.
func (m *schemaMeta) toDocument(item any) (Document, error) {
	v := reflect.ValueOf(item)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}

	id := fmt.Sprint(v.Field(m.idIdx).Interface())

	var content string
	if m.contentIdx != -1 {
		content = fmt.Sprint(v.Field(m.contentIdx).Interface())
	}

	tags := make(map[string]string, len(m.tagFields))
	for _, tf := range m.tagFields {
		tags[tf.name] = fmt.Sprint(v.Field(tf.structIdx).Interface())
	}

	numerics := make(map[string]float64, len(m.numericFields))
	for _, nf := range m.numericFields {
		numerics[nf.name] = toFloat64(v.Field(nf.structIdx))
	}

	return Document{
		ID: id, Content: content,
		Tags: tags, Numerics: numerics,
	}, nil
}

// fromDocument converts a Document back to a typed struct using schema metadata.
func (m *schemaMeta) fromDocument(doc Document) any {
	v := reflect.New(m.typ).Elem()

	v.Field(m.idIdx).SetString(doc.ID)
	if m.contentIdx != -1 {
		v.Field(m.contentIdx).SetString(doc.Content)
	}
	for _, tf := range m.tagFields {
		if val, ok := doc.Tags[tf.name]; ok {
			v.Field(tf.structIdx).SetString(val)
		}
	}
	for _, nf := range m.numericFields {
		if val, ok := doc.Numerics[nf.name]; ok {
			setFloat(v.Field(nf.structIdx), val)
		}
	}
	return v.Interface()
}

func toFloat64(v reflect.Value) float64 {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	default:
		return 0
	}
}

func setFloat(v reflect.Value, f float64) {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		v.SetFloat(f)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(f))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f))
	}
}
