package depsrun

import (
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

func TestEffectiveValidationModePreservesTheLegacyOverrideAndDefault(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value    string
		override bool
		want     deps.ValidationMode
	}{{"", false, deps.ValidationModeFull}, {"fast", false, deps.ValidationModeFast}, {"none", false, deps.ValidationModeNone}, {"unvalidated", false, "unvalidated"}, {"full", true, deps.ValidationModeNone}, {"", true, deps.ValidationModeNone}} {
		if got := EffectiveValidationMode(test.value, test.override); got != test.want {
			t.Fatalf("mode(%q,%v)=%q want%q", test.value, test.override, got, test.want)
		}
	}
}

func TestDerivedScopesKeepNilAndActualSliceProvenance(t *testing.T) {
	t.Parallel()
	scopes := []string{"acme/*"}
	if got := DerivedScopes(false, scopes); got != nil {
		t.Fatalf("unused scopes=%v", got)
	}
	got := DerivedScopes(true, scopes)
	if len(got) != 1 || &got[0] != &scopes[0] {
		t.Fatalf("derived scopes copied/lost: %v", got)
	}
	if DerivedScopes(true, nil) != nil {
		t.Fatal("nil derived scopes changed")
	}
}
