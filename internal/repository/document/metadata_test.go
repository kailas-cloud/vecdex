package document

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
)

func TestFullJSONMetadataStoredOnceOnSource(t *testing.T) {
	doc, err := domdoc.New("source", "firstsecond", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := metadata.Parse([]byte(`{"kind":"fact","nested":{"payload":"` + strings.Repeat("x", 16000) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	doc.SetMetadata(value)
	if err := doc.ProjectMetadata([]field.Field{field.Reconstruct("kind", field.Tag)}); err != nil {
		t.Fatal(err)
	}
	doc.SetVector([]float32{1, 0})
	doc.SetFragments([]domdoc.Fragment{{Content: "first", Vector: []float32{1, 0}},
		{Content: "second", Vector: []float32{1, 0}}})
	parent, children, err := prepareFragmentFields(&doc, testLayout(), "version")
	if err != nil {
		t.Fatal(err)
	}
	if parent["__source_metadata"] != string(value.JSON()) {
		t.Fatal("parent lost complete JSON metadata")
	}
	for _, child := range children {
		if child.Fields["__source_metadata"] != "" || strings.Contains(child.Fields["__metadata"], "payload") {
			t.Fatal("arbitrary metadata was duplicated into a fragment")
		}
		if child.Fields["kind"] != "fact" {
			t.Fatal("declared string index missing from fragment")
		}
	}
	restored, err := parseHashFields(doc.ID(), parent)
	if err != nil || !bytes.Equal(restored.Metadata().JSON(), value.JSON()) {
		t.Fatalf("metadata hydration failed: %v", err)
	}
}
