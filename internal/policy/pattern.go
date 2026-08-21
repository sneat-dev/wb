package policy

import (
	"fmt"
	"path"
	"strings"
)

// selfToken stands for the module path of the module being scanned. It is
// resolved at match time rather than at compile time so that one compiled
// policy can be applied to every repository in a fleet.
const selfToken = "<self>"

// coverageFiller is substituted for a wildcard when Covers builds a
// representative path. It is deliberately unlikely to satisfy a literal
// prefix such as "ext-" on its own.
const coverageFiller = "zz"

// Pattern matches Go import paths and module paths.
//
// The syntax is deliberately the one Go developers already read:
//
//	github.com/acme/thing        exactly that path
//	github.com/acme/thing/...    that path and everything beneath it
//	github.com/acme/ext-*/...    "*" matches within one path segment
//	github.com/acme/{a,b}/...    brace alternation
//	<self>/...                   the module being scanned
//
// Braces are expanded at compile time, so a pattern is held as one or more
// flat alternatives.
type Pattern struct {
	raw          string
	alternatives []alternative
}

type alternative struct {
	segments []string
	prefix   bool
}

// CompilePattern parses one pattern. It reports an error rather than silently
// accepting a pattern that cannot match anything, because an unmatchable
// pattern in a policy is invisible at check time.
func CompilePattern(raw string) (Pattern, error) {
	if strings.TrimSpace(raw) == "" {
		return Pattern{}, fmt.Errorf("pattern is empty")
	}
	expanded, err := expandBraces(raw)
	if err != nil {
		return Pattern{}, fmt.Errorf("pattern %q: %w", raw, err)
	}
	compiled := Pattern{raw: raw}
	for _, candidate := range expanded {
		segments := strings.Split(candidate, "/")
		prefix := false
		for index, segment := range segments {
			if segment != "..." {
				continue
			}
			if index != len(segments)-1 {
				return Pattern{}, fmt.Errorf("pattern %q: \"...\" is only allowed as the final segment", raw)
			}
			prefix = true
		}
		if prefix {
			segments = segments[:len(segments)-1]
		}
		for _, segment := range segments {
			if segment == "" {
				return Pattern{}, fmt.Errorf("pattern %q: empty path segment", raw)
			}
			if _, err := path.Match(segment, ""); err != nil {
				return Pattern{}, fmt.Errorf("pattern %q: bad segment %q: %w", raw, segment, err)
			}
		}
		compiled.alternatives = append(compiled.alternatives, alternative{segments: segments, prefix: prefix})
	}
	return compiled, nil
}

// String returns the pattern as written in the policy, so diagnostics quote
// what the author typed rather than an expanded form.
func (p Pattern) String() string { return p.raw }

// Match reports whether importPath is described by this pattern. self is the
// module path substituted for <self>; an empty self makes <self> patterns
// match nothing rather than match everything.
func (p Pattern) Match(importPath, self string) bool {
	if importPath == "" {
		return false
	}
	segments := strings.Split(importPath, "/")
	for _, candidate := range p.alternatives {
		if candidate.match(segments, self) {
			return true
		}
	}
	return false
}

func (a alternative) match(pathSegments []string, self string) bool {
	segments := a.segments
	if len(segments) > 0 && segments[0] == selfToken {
		if self == "" {
			return false
		}
		segments = append(strings.Split(self, "/"), segments[1:]...)
	}
	if a.prefix {
		if len(pathSegments) < len(segments) {
			return false
		}
	} else if len(pathSegments) != len(segments) {
		return false
	}
	for index, segment := range segments {
		matched, err := path.Match(segment, pathSegments[index])
		if err != nil || !matched {
			return false
		}
	}
	return true
}

// Covers reports whether every path described by other is also described by
// p — the check behind the "unreachable pattern" diagnostic in validate.
//
// It is a deliberately conservative approximation: for each of other's
// alternatives it builds one representative concrete path and asks whether p
// matches it. That catches the ordering mistake this exists for (a broad
// pattern placed above a narrow one), and errs towards staying quiet rather
// than reporting a shadow that is not real.
func (p Pattern) Covers(other Pattern) bool {
	if len(other.alternatives) == 0 {
		return false
	}
	for _, candidate := range other.alternatives {
		if !p.Match(candidate.representative(), "") {
			return false
		}
	}
	return true
}

func (a alternative) representative() string {
	segments := make([]string, 0, len(a.segments)+1)
	for _, segment := range a.segments {
		if segment == selfToken {
			segment = "self"
		}
		replaced := strings.ReplaceAll(segment, "*", coverageFiller)
		segments = append(segments, strings.ReplaceAll(replaced, "?", "z"))
	}
	if a.prefix {
		segments = append(segments, coverageFiller)
	}
	return strings.Join(segments, "/")
}

// expandBraces turns "a/{b,c}/d" into ["a/b/d", "a/c/d"].
func expandBraces(raw string) ([]string, error) {
	open := strings.IndexByte(raw, '{')
	if open < 0 {
		if strings.IndexByte(raw, '}') >= 0 {
			return nil, fmt.Errorf("unmatched \"}\"")
		}
		return []string{raw}, nil
	}
	closing := strings.IndexByte(raw[open:], '}')
	if closing < 0 {
		return nil, fmt.Errorf("unmatched \"{\"")
	}
	closing += open
	choices := strings.Split(raw[open+1:closing], ",")
	if len(choices) == 0 {
		return nil, fmt.Errorf("empty brace group")
	}
	var expanded []string
	for _, choice := range choices {
		if choice == "" {
			return nil, fmt.Errorf("empty alternative in brace group")
		}
		rest, err := expandBraces(raw[:open] + choice + raw[closing+1:])
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, rest...)
	}
	return expanded, nil
}
