package main

import "testing"

// TestJSONFormatValueSetAcceptsTextAndJSON drives every branch of
// jsonFormatValue.Set: the two recognised formats plus the default refusal.
func TestJSONFormatValueSetAcceptsTextAndJSON(t *testing.T) {
	t.Parallel()
	var jsonOut bool
	value := &jsonFormatValue{jsonOut: &jsonOut}

	if err := value.Set("json"); err != nil {
		t.Fatalf("Set(json) returned %v, want nil", err)
	}
	if !jsonOut {
		t.Fatal("Set(json) did not flip jsonOut to true")
	}

	if err := value.Set("text"); err != nil {
		t.Fatalf("Set(text) returned %v, want nil", err)
	}
	if jsonOut {
		t.Fatal("Set(text) did not flip jsonOut to false")
	}
}

func TestJSONFormatValueSetRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()
	var jsonOut bool
	value := &jsonFormatValue{jsonOut: &jsonOut}

	err := value.Set("yaml")
	if err == nil {
		t.Fatal("Set(yaml) returned nil error, want a refusal")
	}
	const want = `unsupported format "yaml"; use text or json`
	if err.Error() != want {
		t.Fatalf("Set(yaml) error = %q, want %q", err.Error(), want)
	}
}

func TestJSONFormatValueStringReflectsCurrentValue(t *testing.T) {
	t.Parallel()
	jsonOut := true
	value := &jsonFormatValue{jsonOut: &jsonOut}
	if got := value.String(); got != "json" {
		t.Fatalf("String() = %q, want %q", got, "json")
	}
	jsonOut = false
	if got := value.String(); got != "text" {
		t.Fatalf("String() = %q, want %q", got, "text")
	}
}
