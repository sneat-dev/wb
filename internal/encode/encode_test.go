package encode

import (
	"encoding/json"
	"testing"
)

type nested struct {
	InnerField string `yaml:"inner_field"`
}

type sample struct {
	SchemaVersion int      `yaml:"schema_version"`
	BaseRef       string   `yaml:"base_ref"`
	Optional      string   `yaml:"optional,omitempty"`
	Children      []nested `yaml:"children"`
}

// TestJSONUsesTheYAMLFieldNames is the whole point of the package: an agent
// reading --format json must see the same snake_case keys the YAML report and
// the documentation use, not Go's exported field names.
func TestJSONUsesTheYAMLFieldNames(t *testing.T) {
	t.Parallel()
	raw, err := JSON(sample{SchemaVersion: 1, BaseRef: "main", Children: []nested{{InnerField: "x"}}})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"schema_version", "base_ref", "children"} {
		if _, ok := document[key]; !ok {
			t.Errorf("JSON is missing key %q: %s", key, raw)
		}
	}
	for _, key := range []string{"SchemaVersion", "BaseRef", "Children"} {
		if _, ok := document[key]; ok {
			t.Errorf("JSON leaked the Go field name %q: %s", key, raw)
		}
	}
	children, ok := document["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatalf("children did not survive: %s", raw)
	}
	child, ok := children[0].(map[string]any)
	if !ok {
		t.Fatalf("child is not an object: %s", raw)
	}
	if _, ok := child["inner_field"]; !ok {
		t.Errorf("nested yaml tags were not applied: %s", raw)
	}
}

// TestJSONHonoursOmitempty keeps the JSON and YAML documents the same shape,
// so a field absent from one is absent from the other.
func TestJSONHonoursOmitempty(t *testing.T) {
	t.Parallel()
	raw, err := JSON(sample{SchemaVersion: 1})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := document["optional"]; ok {
		t.Errorf("omitempty field was emitted: %s", raw)
	}
}

func TestJSONEndsWithANewline(t *testing.T) {
	t.Parallel()
	raw, err := JSON(sample{})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Errorf("output does not end with a newline: %q", raw)
	}
}
