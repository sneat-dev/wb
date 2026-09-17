package encode

import (
	"errors"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// tailCovUndeclinableValue fails inside yaml.Marshal itself. yaml.v3 reports
// only errors raised through its own fail() path as an error return; an
// unmarshalable Go kind such as a func panics instead, so a Marshaler that
// returns an error is the honest way to exercise this branch.
type tailCovUndeclinableValue struct{}

func (tailCovUndeclinableValue) MarshalYAML() (any, error) {
	return nil, errors.New("tailcov: value refuses to marshal")
}

// tailCovDuplicateKeyValue marshals successfully into YAML that cannot be read
// back: two mapping entries share a key. That is exactly the shape where
// yaml.Marshal succeeds and yaml.Unmarshal fails, which is the second error
// branch JSON has to translate.
type tailCovDuplicateKeyValue struct{}

func (tailCovDuplicateKeyValue) MarshalYAML() (any, error) {
	scalar := func(value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	}
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			scalar("tailcov"), scalar("first"),
			scalar("tailcov"), scalar("second"),
		},
	}, nil
}

// TestTailCovJSONSurfacesAYAMLMarshalFailure proves a value the YAML encoder
// rejects is reported to the caller rather than turning into a half-written
// document.
func TestTailCovJSONSurfacesAYAMLMarshalFailure(t *testing.T) {
	t.Parallel()

	raw, err := JSON(tailCovUndeclinableValue{})
	if err == nil {
		t.Fatalf("JSON returned %q, want the YAML marshal error", raw)
	}
	if !strings.Contains(err.Error(), "refuses to marshal") {
		t.Fatalf("JSON error = %v, want the marshaler's own error", err)
	}
	if raw != nil {
		t.Fatalf("JSON returned %q alongside an error, want nil", raw)
	}
}

// TestTailCovJSONSurfacesAYAMLDecodeFailure covers the intermediate step: bytes
// yaml.Marshal produced but yaml.Unmarshal refuses must surface, not be decoded
// as an empty document and emitted as "null".
func TestTailCovJSONSurfacesAYAMLDecodeFailure(t *testing.T) {
	t.Parallel()

	raw, err := JSON(tailCovDuplicateKeyValue{})
	if err == nil {
		t.Fatalf("JSON returned %q for a duplicated YAML mapping key, want an error", raw)
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("JSON error = %v, want the YAML duplicate-key diagnostic", err)
	}
}

// TestTailCovJSONSurfacesAJSONMarshalFailure is the reason the package derives
// JSON from YAML at all: the intermediate document is a bag of YAML scalars,
// and a scalar JSON cannot represent (NaN, ±Inf) must be an error rather than a
// silently corrupted number in an agent's report.
func TestTailCovJSONSurfacesAJSONMarshalFailure(t *testing.T) {
	t.Parallel()

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		raw, err := JSON(value)
		if err == nil {
			t.Errorf("JSON(%v) = %q, want an error: JSON has no representation for it", value, raw)
			continue
		}
		if !strings.Contains(err.Error(), "unsupported value") {
			t.Errorf("JSON(%v) error = %v, want encoding/json's unsupported-value error", value, err)
		}
		if raw != nil {
			t.Errorf("JSON(%v) returned %q alongside an error, want nil", value, raw)
		}
	}
}

// TestTailCovJSONKeepsFiniteNumbersAsNumbers guards the other side of the
// previous test: the YAML round trip must not turn a representable float into a
// string or lose its value.
func TestTailCovJSONKeepsFiniteNumbersAsNumbers(t *testing.T) {
	t.Parallel()

	raw, err := JSON(map[string]any{"ratio": 0.5})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got := string(raw); !strings.Contains(got, `"ratio": 0.5`) {
		t.Fatalf("JSON = %s, want the finite float preserved", got)
	}
}
