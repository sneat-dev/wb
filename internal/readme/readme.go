// Package readme parses and updates the "dev-approach" section in a repository
// README. The canonical section is embedded from dev-approach.md and versioned
// via an HTML-comment marker (<!-- dev-approach:vN -->), so an older or missing
// section can be detected and replaced without disturbing the rest of the file.
package readme

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed dev-approach.md
var canonical string

const endMarker = "<!-- /dev-approach -->"

// blockRe matches an existing section from its start marker through its end
// marker. (?s) lets . span newlines; the version digits are captured.
var blockRe = regexp.MustCompile(`(?s)<!-- dev-approach:v(\d+) -->.*?<!-- /dev-approach -->`)

// startRe matches just the start marker, used when validating the template.
var startRe = regexp.MustCompile(`<!-- dev-approach:v(\d+) -->`)

// Action describes what Apply did (or Plan would do) to a README.
type Action int

const (
	ActionNoop    Action = iota // section already present and current
	ActionInsert                // no section found; one was added
	ActionReplace               // an older section was replaced
)

func (a Action) String() string {
	switch a {
	case ActionInsert:
		return "insert"
	case ActionReplace:
		return "replace"
	default:
		return "noop"
	}
}

// Template is the canonical dev-approach section and its version.
type Template struct {
	Version int
	Block   string // full block, including both markers, no trailing newline
}

// Canonical returns the embedded dev-approach template.
func Canonical() (Template, error) {
	return Parse(canonical)
}

// Parse validates marker presence and extracts the version from raw template
// content.
func Parse(content string) (Template, error) {
	content = strings.TrimSpace(content)
	m := startRe.FindStringSubmatch(content)
	if m == nil {
		return Template{}, fmt.Errorf("template missing start marker %q", "<!-- dev-approach:vN -->")
	}
	if !strings.Contains(content, endMarker) {
		return Template{}, fmt.Errorf("template missing end marker %q", endMarker)
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return Template{}, fmt.Errorf("invalid template version %q: %w", m[1], err)
	}
	return Template{Version: v, Block: content}, nil
}

// Plan reports the Action that Apply would take for the given README content,
// along with the version of any section already present (0 if none).
func Plan(content string, t Template) (Action, int) {
	loc := blockRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return ActionInsert, 0
	}
	existing, _ := strconv.Atoi(content[loc[2]:loc[3]])
	if existing >= t.Version {
		return ActionNoop, existing
	}
	return ActionReplace, existing
}

// Apply returns README content with the canonical section present and current.
// It inserts a missing section, replaces an older one, and leaves a current
// section untouched. The returned Action reports which path was taken.
func Apply(content string, t Template) (string, Action) {
	loc := blockRe.FindStringSubmatchIndex(content)
	if loc != nil {
		existing, _ := strconv.Atoi(content[loc[2]:loc[3]])
		if existing >= t.Version {
			return content, ActionNoop
		}
		return content[:loc[0]] + t.Block + content[loc[1]:], ActionReplace
	}
	return insert(content, t.Block), ActionInsert
}

// insert places the block after the H1 intro and before the first H2. With no
// H2 it appends at the end; with empty content it returns just the block.
func insert(content, block string) string {
	lines := strings.Split(content, "\n")
	h2 := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			h2 = i
			break
		}
	}
	if h2 == -1 {
		trimmed := strings.TrimRight(content, "\n")
		if trimmed == "" {
			return block + "\n"
		}
		return trimmed + "\n\n" + block + "\n"
	}
	before := strings.TrimRight(strings.Join(lines[:h2], "\n"), "\n")
	after := strings.Join(lines[h2:], "\n")
	if before == "" {
		return block + "\n\n" + after
	}
	return before + "\n\n" + block + "\n\n" + after
}
