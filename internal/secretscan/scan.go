package secretscan

import (
	"fmt"
	"sort"
	"strings"
)

// Segment is one named, independently line-numbered slice of the text under
// scan, e.g. a park continuation's distinct fields (--summary,
// --validation, --remaining, the body file) so a refusal can say exactly
// which one matched.
type Segment struct {
	Name    string
	Content []byte
}

// Finding is one rule match. It never carries the matched bytes: Fingerprint
// is the only trace of what was found, see Fingerprint.
type Finding struct {
	RuleID      string
	Description string
	Severity    Severity
	Segment     string
	Line        int
	Column      int
	Fingerprint string
	Source      string
}

// Key identifies this exact occurrence for --override-secret matching:
// "<rule-id>:<fingerprint-digest>". Line/column are deliberately excluded so
// an override survives immaterial reflow of surrounding text, but a
// different secret (different bytes, different fingerprint) always needs
// its own explicit override.
func (f Finding) Key() string {
	digest := f.Fingerprint
	if idx := strings.IndexByte(digest, ' '); idx >= 0 {
		digest = digest[:idx]
	}
	return f.RuleID + ":" + digest
}

// String renders one refusal line: which rule, where, and the redacted
// fingerprint -- never the matched value.
func (f Finding) String() string {
	return fmt.Sprintf("rule %q matched in %s at line %d, column %d (%s) [%s]",
		f.RuleID, f.Segment, f.Line, f.Column, f.Fingerprint, f.Description)
}

// Result is every finding from one Scan call.
type Result struct {
	Findings []Finding
}

// Blocking returns every SeverityBlock finding not covered by overrides.
func (r Result) Blocking(overrides Overrides) []Finding {
	var blocking []Finding
	for _, finding := range r.Findings {
		if finding.Severity != SeverityBlock {
			continue
		}
		if overrides.Covers(finding) {
			continue
		}
		blocking = append(blocking, finding)
	}
	return blocking
}

// Warnings returns every SeverityWarn finding, plus every SeverityBlock
// finding that was overridden (so the override is still surfaced, not
// silently dropped).
func (r Result) Warnings(overrides Overrides) []Finding {
	var warnings []Finding
	for _, finding := range r.Findings {
		if finding.Severity == SeverityWarn || (finding.Severity == SeverityBlock && overrides.Covers(finding)) {
			warnings = append(warnings, finding)
		}
	}
	return warnings
}

// Scanner evaluates Rules against text. The zero value is not usable; build
// one with Load or LoadDefault.
type Scanner struct {
	rules []Rule
}

// NewScanner builds a Scanner from already-loaded rules, for tests and for
// callers assembling a custom rule set directly.
func NewScanner(rules []Rule) *Scanner {
	cloned := append([]Rule(nil), rules...)
	return &Scanner{rules: cloned}
}

// Rules returns every loaded rule, for diagnostics (e.g. `wb` printing what
// a scan ran with). Callers must not mutate the result.
func (s *Scanner) Rules() []Rule {
	return s.rules
}

// Scan evaluates every rule against every segment and returns every match.
// It never returns an error: an unusable rule was already dropped at load
// time (see parseTOMLRuleset), so scanning itself cannot fail.
func (s *Scanner) Scan(segments ...Segment) Result {
	var result Result
	for _, segment := range segments {
		lower := strings.ToLower(string(segment.Content))
		for _, rule := range s.rules {
			if !keywordsPresent(lower, rule.Keywords) {
				continue
			}
			for _, match := range rule.Regex.FindAllIndex(segment.Content, -1) {
				start, end := match[0], match[1]
				line, column := lineAndColumn(segment.Content, start)
				result.Findings = append(result.Findings, Finding{
					RuleID:      rule.ID,
					Description: rule.Description,
					Severity:    rule.Severity,
					Segment:     segment.Name,
					Line:        line,
					Column:      column,
					Fingerprint: Fingerprint(segment.Content[start:end]),
					Source:      rule.Source,
				})
			}
		}
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.Segment != b.Segment {
			return a.Segment < b.Segment
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.RuleID < b.RuleID
	})
	return result
}

func keywordsPresent(lowerContent string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, keyword := range keywords {
		if strings.Contains(lowerContent, keyword) {
			return true
		}
	}
	return false
}

func lineAndColumn(content []byte, offset int) (line, column int) {
	line = 1
	lastNewline := -1
	for index := 0; index < offset && index < len(content); index++ {
		if content[index] == '\n' {
			line++
			lastNewline = index
		}
	}
	column = offset - lastNewline
	return line, column
}
