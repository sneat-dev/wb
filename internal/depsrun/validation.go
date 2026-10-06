package depsrun

import "github.com/sneat-dev/wb/internal/deps"

// EffectiveValidationMode applies the common legacy escape hatch/default policy
// after the caller has validated its command-specific flags.
func EffectiveValidationMode(value string, noVerify bool) deps.ValidationMode {
	if noVerify {
		return deps.ValidationModeNone
	}
	if value == "" {
		return deps.ValidationModeFull
	}
	return deps.ValidationMode(value)
}

// DerivedScopes records scopes only when they actually produced registry seeds.
func DerivedScopes(latest bool, scopes []string) []string {
	if !latest {
		return nil
	}
	return scopes
}
