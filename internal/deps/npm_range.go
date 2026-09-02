package deps

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// npmRangeVerdict is the deterministic outcome of asking whether one npm
// version specifier admits one exact published version.
//
// WB never guesses. npm's specifier grammar is far larger than the shapes a
// Sneat manifest actually uses (compound `||` unions, hyphen ranges, `npm:`
// aliases, `workspace:`/`catalog:` protocols, git and file specifiers), so an
// unrecognised shape is reported as *unevaluated* with the reason, never
// silently treated as satisfied or unsatisfied. A drift report that says "this
// range was not evaluated" is useful; one that quietly guesses is not.
type npmRangeVerdict struct {
	// Evaluated is false when the specifier is outside the supported subset.
	Evaluated bool
	// Admits is meaningful only when Evaluated is true.
	Admits bool
	// Reason explains an unevaluated specifier.
	Reason string
}

// npmRangeAdmits reports whether specifier admits the exact version.
//
// Supported specifier shapes, which cover every reference the Sneat fleet's
// package.json files declare for their own packages:
//
//	""  "*"  "x"  "X"      any version
//	1.2.3  =1.2.3          exactly that version
//	^1.2.3                 npm caret, including its 0.x and 0.0.x narrowing
//	~1.2.3                 npm tilde
//	>=1.2.3  >1.2.3        lower bounds
//	<=1.2.3  <1.2.3        upper bounds
//
// Anything else — a union, a hyphen range, or a non-registry protocol — is
// returned unevaluated.
func npmRangeAdmits(specifier, version string) npmRangeVerdict {
	specifier = strings.TrimSpace(specifier)
	version = strings.TrimSpace(version)
	if !universalSemverValid(version) {
		return npmRangeVerdict{Reason: "candidate version " + quoteForReason(version) + " is not an exact semantic version"}
	}
	if semver.Prerelease(normalizeSemverPrefix(version)) != "" {
		return npmRangeVerdict{Reason: "candidate version " + quoteForReason(version) + " is a prerelease; npm prerelease admission is specifier-scoped and is not evaluated"}
	}
	switch specifier {
	case "", "*", "x", "X", "latest":
		return npmRangeVerdict{Evaluated: true, Admits: true}
	}
	if protocol, _, found := strings.Cut(specifier, ":"); found {
		switch protocol {
		case "workspace", "catalog", "npm", "file", "link", "git", "git+ssh", "git+https", "portal", "patch":
			return npmRangeVerdict{Reason: "specifier uses the " + protocol + ": protocol, which does not name a registry version range"}
		}
	}
	if strings.Contains(specifier, "||") || strings.Contains(specifier, " - ") || strings.ContainsAny(specifier, " ,") {
		return npmRangeVerdict{Reason: "compound specifier " + quoteForReason(specifier) + " is not evaluated"}
	}

	operator, literal := splitNpmRangeOperator(specifier)
	if strings.HasSuffix(literal, ".x") || strings.HasSuffix(literal, ".X") || strings.HasSuffix(literal, ".*") {
		return npmRangeVerdict{Reason: "wildcard specifier " + quoteForReason(specifier) + " is not evaluated"}
	}
	if !universalSemverValid(literal) {
		return npmRangeVerdict{Reason: "specifier " + quoteForReason(specifier) + " does not name an exact semantic version"}
	}
	if semver.Prerelease(normalizeSemverPrefix(literal)) != "" {
		return npmRangeVerdict{Reason: "specifier " + quoteForReason(specifier) + " pins a prerelease; npm prerelease admission is not evaluated"}
	}

	compared := universalSemverCompare(version, literal)
	switch operator {
	case "", "=":
		return npmRangeVerdict{Evaluated: true, Admits: compared == 0}
	case ">":
		return npmRangeVerdict{Evaluated: true, Admits: compared > 0}
	case ">=":
		return npmRangeVerdict{Evaluated: true, Admits: compared >= 0}
	case "<":
		return npmRangeVerdict{Evaluated: true, Admits: compared < 0}
	case "<=":
		return npmRangeVerdict{Evaluated: true, Admits: compared <= 0}
	case "^":
		return npmRangeVerdict{Evaluated: true, Admits: compared >= 0 && universalSemverCompare(version, npmCaretCeiling(literal)) < 0}
	case "~":
		return npmRangeVerdict{Evaluated: true, Admits: compared >= 0 && universalSemverCompare(version, npmTildeCeiling(literal)) < 0}
	}
	return npmRangeVerdict{Reason: "specifier operator " + quoteForReason(operator) + " is not evaluated"}
}

// splitNpmRangeOperator separates a leading comparison operator from the
// version literal that follows it. Two-character operators are matched first
// so ">=1.2.3" never degrades into ">" plus "=1.2.3".
func splitNpmRangeOperator(specifier string) (operator, literal string) {
	for _, candidate := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(specifier, candidate) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(specifier, candidate))
		}
	}
	return "", specifier
}

// npmCaretCeiling is the exclusive upper bound of `^literal` under npm's own
// rule: the caret allows changes that do not modify the left-most non-zero
// element, so ^1.2.3 stops at 2.0.0, ^0.2.3 at 0.3.0, and ^0.0.3 at 0.0.4.
// This 0.x narrowing is exactly why a fleet on 0.x versions cannot assume a
// caret range picks up the next published minor.
func npmCaretCeiling(literal string) string {
	major, minor, patch := splitNpmVersionParts(literal)
	switch {
	case major != 0:
		return formatNpmVersion(major+1, 0, 0)
	case minor != 0:
		return formatNpmVersion(0, minor+1, 0)
	default:
		return formatNpmVersion(0, 0, patch+1)
	}
}

// npmTildeCeiling is the exclusive upper bound of `~literal`: with a minor
// present, the tilde allows patch-level changes only.
func npmTildeCeiling(literal string) string {
	major, minor, _ := splitNpmVersionParts(literal)
	return formatNpmVersion(major, minor+1, 0)
}

func splitNpmVersionParts(literal string) (major, minor, patch int) {
	canonical := semver.Canonical(normalizeSemverPrefix(literal))
	canonical = strings.TrimPrefix(canonical, "v")
	if index := strings.IndexAny(canonical, "-+"); index >= 0 {
		canonical = canonical[:index]
	}
	parts := strings.Split(canonical, ".")
	values := [3]int{}
	for index := 0; index < len(parts) && index < 3; index++ {
		values[index], _ = strconv.Atoi(parts[index])
	}
	return values[0], values[1], values[2]
}

func formatNpmVersion(major, minor, patch int) string {
	return strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
}

func quoteForReason(value string) string {
	return strconv.Quote(value)
}
