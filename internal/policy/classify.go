package policy

import (
	"fmt"
	"strings"
)

// Classification is the full account of how one import path was classified.
// It carries more than the answer because the answer alone is not reviewable:
// when a verdict surprises someone, what they need is which pattern won and
// what else would have matched.
type Classification struct {
	Import string
	Group  string

	// Pattern is the winning pattern as the policy author wrote it, and
	// PatternNumber its position in declaration order across the whole policy.
	Pattern       string
	PatternNumber int

	// AlsoMatched lists patterns declared after the winner that would also
	// have matched. Under first-match-wins these are shadowed, and being able
	// to see them is what makes an ordering mistake findable.
	AlsoMatched []PatternMatch
}

// PatternMatch names one pattern that matched.
type PatternMatch struct {
	Group   string
	Pattern string
	Number  int
}

type patternRef struct {
	number  int
	group   string
	pattern Pattern
}

func (p Policy) flatPatterns() []patternRef {
	var refs []patternRef
	number := 0
	for _, group := range p.Groups {
		for _, pattern := range group.Patterns {
			number++
			refs = append(refs, patternRef{number: number, group: group.Name, pattern: pattern})
		}
	}
	return refs
}

// IsStdlib reports whether an import path names the Go standard library.
// Standard-library paths are the ones whose first segment carries no dot,
// which is the same rule the go command uses.
func IsStdlib(importPath string) bool {
	if importPath == "" {
		return false
	}
	first := importPath
	if index := strings.IndexByte(importPath, '/'); index >= 0 {
		first = importPath[:index]
	}
	return !strings.Contains(first, ".")
}

// Classify assigns an import path to a group. self is the module path being
// scanned, substituted for <self> patterns.
func (p Policy) Classify(importPath, self string) Classification {
	if IsStdlib(importPath) {
		return Classification{Import: importPath, Group: GroupStdlib}
	}
	result := Classification{Import: importPath, Group: GroupUnclassified}
	for _, ref := range p.flatPatterns() {
		if !ref.pattern.Match(importPath, self) {
			continue
		}
		if result.Group == GroupUnclassified {
			result.Group = ref.group
			result.Pattern = ref.pattern.String()
			result.PatternNumber = ref.number
			continue
		}
		if ref.group == result.Group {
			continue
		}
		result.AlsoMatched = append(result.AlsoMatched, PatternMatch{
			Group: ref.group, Pattern: ref.pattern.String(), Number: ref.number,
		})
	}
	return result
}

// Detect resolves a module path to a repository type.
//
// Types are declared in order and the first match wins — the same rule groups
// follow, so a policy document has one ordering principle rather than two.
// Overlap is expected and useful: "github.com/acme/ext-*/backend" above
// "github.com/acme/*/backend" reads as "contracts, then everything else",
// which is what the author means. Validate reports a detect pattern that an
// earlier type has already claimed entirely.
func (p Policy) Detect(modulePath string) (string, error) {
	for _, repoType := range p.Types {
		for _, pattern := range repoType.Detect {
			if pattern.Match(modulePath, modulePath) {
				return repoType.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no type in %s detects module %q; declare the type explicitly in the repository's policy file", p.Source, modulePath)
}
