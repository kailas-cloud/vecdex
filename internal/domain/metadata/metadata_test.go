package metadata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

func TestObjectPreservesJSONAndDetachesNestedValues(t *testing.T) {
	object, err := Parse([]byte(`{"big":9007199254740993,` +
		`"nested":{"list":[null,true,{"text":"original"}]},"nothing":null}`))
	if err != nil {
		t.Fatal(err)
	}
	values := object.Values()
	if values["big"] != json.Number("9007199254740993") {
		t.Fatal("metadata number lost precision")
	}
	values["nested"].(map[string]any)["list"].([]any)[2].(map[string]any)["text"] = "changed"
	encoded := object.JSON()
	encoded[0] = '!'
	if !strings.Contains(string(object.JSON()), `"text":"original"`) || !json.Valid(object.JSON()) {
		t.Fatal("caller mutation changed immutable metadata")
	}
}

func TestOnlyDeclaredStringAndNumberMetadataAreIndexed(t *testing.T) {
	object, err := Parse([]byte(`{"kind":"fact","weight":2.5,"nested":{"free":true},"big":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	fields := []field.Field{field.Reconstruct("kind", field.Tag), field.Reconstruct("weight", field.Numeric)}
	tags, numerics, err := object.Project(fields)
	if err != nil || len(tags) != 1 || tags["kind"] != "fact" || len(numerics) != 1 || numerics["weight"] != 2.5 {
		t.Fatalf("tags=%v numerics=%v err=%v", tags, numerics, err)
	}
	for _, raw := range []string{`[]`, `null`, `42`, `{} {}`, `{"weight":"2.5"}`, `{"kind":["fact"]}`} {
		value, err := Parse([]byte(raw))
		if err == nil {
			_, _, err = value.Project(fields)
		}
		if !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("accepted invalid indexed metadata %s: %v", raw, err)
		}
	}
}

func TestJSONMergePatchPreservesUnmentionedMembersAndReplacesArrays(t *testing.T) {
	base, err := Parse([]byte(`{"nested":{"keep":true,"remove":1},"array":[1,2],"big":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(base, []byte(`{"nested":{"remove":null,"new":"value"},"array":[false,null]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(merged.JSON()) != `{"array":[false,null],"big":9007199254740993,"nested":{"keep":true,"new":"value"}}` {
		t.Fatalf("merged %s", merged.JSON())
	}
	if !strings.Contains(string(base.JSON()), `"remove":1`) {
		t.Fatal("merge mutated the prior source version")
	}
	cleared, err := Merge(merged, []byte(`null`))
	if err != nil || string(cleared.JSON()) != `{}` || !cleared.Present() {
		t.Fatalf("clear=%s err=%v", cleared.JSON(), err)
	}
}

func TestMergeChecksFinalMetadataSize(t *testing.T) {
	base, err := Parse([]byte(`{"a":"` + strings.Repeat("x", 40000) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Merge(base, []byte(`{"b":"`+strings.Repeat("y", 40000)+`"}`))
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatalf("accepted oversized merged object: %v", err)
	}
}
