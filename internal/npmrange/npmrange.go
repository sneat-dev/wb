// Package npmrange holds the one decomposition of npm's version-specifier
// grammar WB uses.
//
// Two callers need the same grammar and want different answers from it.
// `wb deps` asks whether a specifier *admits an exact version*, and declines
// every shape it cannot evaluate rather than guessing. `wb pr land` asks the
// much weaker question of whether a changed `package.json` value *is a version
// specifier at all*, which is what separates `"lodash": "^4.17.21"` from
// `"build": "tsc && node scripts/x.js"`.
//
// Those are different policies over one grammar. When the grammar was written
// twice, they disagreed: `wb deps` learned to read `"^0.26.1 || ^0.27.0"` and
// the landing classifier still refused it as "not a version range" because it
// contained a space and a `|`, so routine fleet bumps needed `--approved-by`.
// So the splitting rules live here once, and each caller applies its own
// policy to the leaves.
package npmrange

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Wildcards are the specifiers that name any published version.
var wildcards = map[string]bool{"": true, "*": true, "x": true, "X": true, "latest": true}

// protocols are the non-registry specifier prefixes npm accepts. They name a
// source rather than a range, and every caller has to decide about them
// separately, so they are recognised here and judged there.
var protocols = map[string]bool{
	"workspace": true, "catalog": true, "npm": true, "file": true, "link": true,
	"git": true, "git+ssh": true, "git+https": true, "portal": true, "patch": true,
	"github": true,
}

// Wildcard reports whether specifier names any version.
func Wildcard(specifier string) bool {
	return wildcards[strings.TrimSpace(specifier)]
}

// Protocol returns the non-registry protocol a specifier uses, if any.
func Protocol(specifier string) (string, bool) {
	name, _, found := strings.Cut(strings.TrimSpace(specifier), ":")
	if !found || !protocols[name] {
		return "", false
	}
	return name, true
}

// HyphenRange reports the `1.2.3 - 2.0.0` grammar, which is a range form of
// its own rather than a conjunction of comparators.
func HyphenRange(specifier string) bool {
	return strings.Contains(specifier, " - ")
}

// CommaSeparated reports a comma list. npm ranges do not use commas at all, so
// a comma says the value was probably written for another ecosystem.
func CommaSeparated(specifier string) bool {
	return strings.Contains(specifier, ",")
}

// UnionBranches splits `A || B` into its branches, dropping empty ones. A
// specifier with no `||` is a single branch.
func UnionBranches(specifier string) []string {
	branches := make([]string, 0, 2)
	for _, branch := range strings.Split(specifier, "||") {
		branch = strings.TrimSpace(branch)
		if branch != "" {
			branches = append(branches, branch)
		}
	}
	return branches
}

// Comparators splits one union branch into the space-separated comparators npm
// reads as AND: `>=22.0.0 <23.0.0`.
func Comparators(branch string) []string {
	return strings.Fields(branch)
}

// Union reports whether a specifier is a `||` union.
func Union(specifier string) bool {
	return strings.Contains(specifier, "||")
}

// Conjunction reports whether a specifier is a space-separated conjunction.
func Conjunction(specifier string) bool {
	return strings.ContainsAny(specifier, " \t")
}

// SplitOperator separates a leading comparison operator from the version
// literal that follows it. Two-character operators are matched first so
// ">=1.2.3" never degrades into ">" plus "=1.2.3".
func SplitOperator(comparator string) (operator, literal string) {
	for _, candidate := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(comparator, candidate) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(comparator, candidate))
		}
	}
	return "", comparator
}

// IsSpecifier reports whether value is an npm version specifier.
//
// This is a question about shape, not about evaluability: a hyphen range and a
// `1.2.x` wildcard are specifiers even though `wb deps` declines to evaluate
// them, and a caller that needs an exact answer asks npmRangeAdmits instead.
// What it excludes is everything that is not a version at all — a shell
// command, a path, a sentence — which is the distinction a dependency-manifest
// classifier turns on.
func IsSpecifier(value string) bool {
	value = strings.TrimSpace(value)
	if Wildcard(value) {
		return true
	}
	if _, ok := Protocol(value); ok {
		return true
	}
	if CommaSeparated(value) {
		return false
	}
	if HyphenRange(value) {
		left, right, _ := strings.Cut(value, " - ")
		return isComparator(strings.TrimSpace(left)) && isComparator(strings.TrimSpace(right))
	}
	branches := UnionBranches(value)
	if len(branches) == 0 {
		return false
	}
	for _, branch := range branches {
		comparators := Comparators(branch)
		if len(comparators) == 0 {
			return false
		}
		for _, comparator := range comparators {
			if !isComparator(comparator) {
				return false
			}
		}
	}
	return true
}

// isComparator reports whether one comparator is an operator followed by
// something that reads as a version. `1.2.x`, `1.2.*` and prereleases count:
// they are versions WB does not evaluate, not values that are not versions.
func isComparator(comparator string) bool {
	if Wildcard(comparator) {
		return true
	}
	_, literal := SplitOperator(comparator)
	literal = strings.TrimSpace(literal)
	if literal == "" {
		return false
	}
	if Wildcard(literal) {
		return true
	}
	// A trailing wildcard component stands in for a number, so canonicalise it
	// to one before asking semver, which has no wildcard grammar of its own.
	normalized := literal
	for _, suffix := range []string{".x", ".X", ".*"} {
		for strings.HasSuffix(normalized, suffix) {
			normalized = strings.TrimSuffix(normalized, suffix) + ".0"
		}
	}
	if !strings.HasPrefix(normalized, "v") {
		normalized = "v" + normalized
	}
	return semver.IsValid(normalized)
}
