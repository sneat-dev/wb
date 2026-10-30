// Package repofilter is the one implementation of the fleet --filter selector.
//
// Every fleet verb used to spell the selector as strings.Contains, so a
// shell-style pattern such as 'datatug/*' was compared as the literal text
// "datatug/*", matched nothing, and the verb reported an empty success
// (wb 0.202.0, 2026-10-29: 240 rows unfiltered, zero with the glob). A filter
// is now a case-insensitive owner/name substring, or a glob when it carries a
// glob metacharacter.
package repofilter

import (
	"path"
	"strings"
)

// IsGlob reports whether filter is matched as a glob rather than a substring.
func IsGlob(filter string) bool { return strings.ContainsAny(filter, "*?[") }

// Valid reports whether filter is usable. Only a malformed glob is invalid.
func Valid(filter string) bool {
	if !IsGlob(filter) {
		return true
	}
	_, err := path.Match(strings.ToLower(filter), "")
	return err == nil
}

// Match reports whether filter selects any candidate. An empty filter selects
// everything. A substring filter matches anywhere in a candidate. A glob is
// matched against the whole candidate and against every suffix that starts
// after a '/', so 'datatug/*' selects the slug datatug/websites, the
// host-qualified github.com/datatug/websites, and a checkout path ending in
// either.
func Match(filter string, candidates ...string) bool {
	if filter == "" {
		return true
	}
	filter = strings.ToLower(filter)
	glob := IsGlob(filter)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		candidate = strings.ToLower(candidate)
		if !glob {
			if strings.Contains(candidate, filter) {
				return true
			}
			continue
		}
		if globMatchesSuffix(filter, candidate) {
			return true
		}
	}
	return false
}

func globMatchesSuffix(pattern, candidate string) bool {
	for {
		if matched, err := path.Match(pattern, candidate); err == nil && matched {
			return true
		}
		index := strings.IndexByte(candidate, '/')
		if index < 0 {
			return false
		}
		candidate = candidate[index+1:]
	}
}
