package deps

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sneat-dev/wb/internal/npmrange"
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
	if npmrange.Wildcard(specifier) {
		return npmRangeVerdict{Evaluated: true, Admits: true}
	}
	if protocol, ok := npmrange.Protocol(specifier); ok {
		return npmRangeVerdict{Reason: "specifier uses the " + protocol + ": protocol, which does not name a registry version range"}
	}
	// A hyphen range and a comma list are still declined: the first is a
	// distinct grammar rather than a conjunction of comparators, and npm
	// ranges do not use commas at all, so a comma is more likely a manifest
	// written for a different ecosystem than a range WB should interpret.
	if npmrange.HyphenRange(specifier) {
		return npmRangeVerdict{Reason: "hyphen range " + quoteForReason(specifier) + " is not evaluated"}
	}
	if npmrange.CommaSeparated(specifier) {
		return npmRangeVerdict{Reason: "comma-separated specifier " + quoteForReason(specifier) + " is not evaluated"}
	}
	if npmrange.Union(specifier) {
		return npmRangeUnionAdmits(specifier, version)
	}
	if npmrange.Conjunction(specifier) {
		return npmRangeConjunctionAdmits(specifier, version)
	}
	return npmRangeComparatorAdmits(specifier, version)
}

// npmRangeUnionAdmits evaluates `A || B`: the version is admitted when any
// branch admits it.
//
// The interesting case is a union WB can only partly read. One branch that
// provably admits settles the question no matter what the others say, so it is
// answered. Otherwise an unreadable branch could still have admitted, so the
// union is unevaluated rather than rejected — WB never converts "I could not
// read this" into "this does not match".
func npmRangeUnionAdmits(specifier, version string) npmRangeVerdict {
	unevaluated := ""
	for _, branch := range npmrange.UnionBranches(specifier) {
		verdict := npmRangeConjunctionAdmits(branch, version)
		if verdict.Evaluated && verdict.Admits {
			return npmRangeVerdict{Evaluated: true, Admits: true}
		}
		if !verdict.Evaluated && unevaluated == "" {
			unevaluated = verdict.Reason
		}
	}
	if unevaluated != "" {
		return npmRangeVerdict{Reason: "union specifier " + quoteForReason(specifier) + " has a branch WB does not evaluate: " + unevaluated}
	}
	return npmRangeVerdict{Evaluated: true}
}

// npmRangeConjunctionAdmits evaluates space-separated comparators, which npm
// reads as AND: `>=22.0.0 <23.0.0` is how a major line is pinned, and it is by
// far the most common peerDependencies shape in this fleet — every Angular and
// Ionic peer uses it. Declining it left `wb deps peers` reporting five of eight
// real rows unevaluated on its first live run.
//
// A single comparator that provably rejects settles the question, so it is
// answered even when a sibling comparator is unreadable. Otherwise an
// unreadable comparator could still have rejected, so the conjunction is
// unevaluated rather than admitted — the asymmetry mirrors the union's, and
// both keep WB from ever guessing in the direction that hides a conflict.
func npmRangeConjunctionAdmits(specifier, version string) npmRangeVerdict {
	unevaluated := ""
	evaluated := 0
	for _, comparator := range npmrange.Comparators(specifier) {
		verdict := npmRangeComparatorAdmits(comparator, version)
		if verdict.Evaluated && !verdict.Admits {
			return npmRangeVerdict{Evaluated: true}
		}
		if !verdict.Evaluated {
			if unevaluated == "" {
				unevaluated = verdict.Reason
			}
			continue
		}
		evaluated++
	}
	if unevaluated != "" {
		return npmRangeVerdict{Reason: unevaluated}
	}
	if evaluated == 0 {
		return npmRangeVerdict{Reason: "specifier " + quoteForReason(specifier) + " names no comparator"}
	}
	return npmRangeVerdict{Evaluated: true, Admits: true}
}

// npmRangeComparatorAdmits evaluates exactly one comparator.
func npmRangeComparatorAdmits(specifier, version string) npmRangeVerdict {
	if npmrange.Wildcard(specifier) {
		return npmRangeVerdict{Evaluated: true, Admits: true}
	}
	operator, literal := npmrange.SplitOperator(specifier)
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
