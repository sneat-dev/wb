// Package encode renders wb's report types as JSON.
//
// The report types are tagged for YAML, which is the on-disk report format.
// Deriving the JSON document from the YAML document — rather than maintaining a
// parallel set of json tags on every field — keeps the two representations
// identical by construction: a field added later cannot end up snake_case in
// the YAML report and CamelCase in the JSON an agent parses.
package encode

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// JSON renders value as indented JSON using the field names its YAML tags
// define, with a trailing newline so the output is a complete line-oriented
// document.
func JSON(value any) ([]byte, error) {
	intermediate, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	if err := yaml.Unmarshal(intermediate, &document); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
