package recipe

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Action describes what a template-section recipe did (or would do) to its
// target file.
type Action int

const (
	ActionNoop Action = iota
	ActionInsert
	ActionReplace
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

// templateBlock is a parsed, versioned recipe template.
type templateBlock struct {
	Version int
	Block   string // full block including both markers, no trailing newline
}

// loadTemplate reads and validates r's template file, using r.Marker to
// build the marker regexes.
func (r Recipe) loadTemplate() (templateBlock, error) {
	raw, err := os.ReadFile(expandPath(r.Template))
	if err != nil {
		return templateBlock{}, fmt.Errorf("read template %s: %w", r.Template, err)
	}
	content := strings.TrimSpace(string(raw))
	m := r.startMarkerRe().FindStringSubmatch(content)
	if m == nil {
		return templateBlock{}, fmt.Errorf("template missing start marker <!-- %s:vN -->", r.Marker)
	}
	end := r.endMarker()
	if !strings.Contains(content, end) {
		return templateBlock{}, fmt.Errorf("template missing end marker %s", end)
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return templateBlock{}, fmt.Errorf("invalid template version %q: %w", m[1], err)
	}
	return templateBlock{Version: v, Block: content}, nil
}

func (r Recipe) startMarkerRe() *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`<!-- %s:v(\d+) -->`, regexp.QuoteMeta(r.Marker)))
}

func (r Recipe) blockRe() *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`(?s)<!-- %s:v(\d+) -->.*?<!-- /%s -->`, regexp.QuoteMeta(r.Marker), regexp.QuoteMeta(r.Marker)))
}

func (r Recipe) endMarker() string {
	return fmt.Sprintf("<!-- /%s -->", r.Marker)
}

// planTemplateSection reports the Action Apply would take for the given
// target-file content.
func planTemplateSection(content string, tmpl templateBlock, blockRe *regexp.Regexp) Action {
	loc := blockRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return ActionInsert
	}
	existing, _ := strconv.Atoi(content[loc[2]:loc[3]])
	if existing >= tmpl.Version {
		return ActionNoop
	}
	return ActionReplace
}

// applyTemplateSection returns target content with tmpl's block present and
// current.
func applyTemplateSection(content string, tmpl templateBlock, blockRe *regexp.Regexp) (string, Action) {
	loc := blockRe.FindStringSubmatchIndex(content)
	if loc != nil {
		existing, _ := strconv.Atoi(content[loc[2]:loc[3]])
		if existing >= tmpl.Version {
			return content, ActionNoop
		}
		return content[:loc[0]] + tmpl.Block + content[loc[1]:], ActionReplace
	}
	return insertBlock(content, tmpl.Block), ActionInsert
}

// insertBlock places block after the H1 intro and before the first H2. With
// no H2 it appends at the end; with empty content it returns just the block.
func insertBlock(content, block string) string {
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
