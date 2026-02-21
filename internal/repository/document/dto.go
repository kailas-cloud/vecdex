package document

import (
	"encoding/binary"
	"math"
	"strconv"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// buildHashFields converts a domain Document into a flat map[string]string for HSET.
func buildHashFields(doc *domdoc.Document) map[string]string {
	m := make(map[string]string, 2+len(doc.Tags())+len(doc.Numerics()))
	m["__content"] = doc.Content()
	m["__vector"] = vectorToBytes(doc.Vector())
	for k, v := range doc.Tags() {
		m[k] = v
	}
	for k, v := range doc.Numerics() {
		m[k] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return m
}

// parseHashFields converts a flat hash map back into a domain Document.
func parseHashFields(id string, m map[string]string) domdoc.Document {
	var content string
	var vector []float32
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	for k, v := range m {
		switch k {
		case "__content":
			content = v
		case "__vector":
			vector = bytesToVector(v)
		default:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				numerics[k] = f
			} else {
				tags[k] = v
			}
		}
	}

	return domdoc.Reconstruct(id, content, tags, numerics, vector, 0)
}

// vectorToBytes serializes []float32 to a binary string (4 bytes per float, little-endian).
func vectorToBytes(v []float32) string {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return string(buf)
}

// bytesToVector deserializes a binary string back to []float32.
func bytesToVector(s string) []float32 {
	b := []byte(s)
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
