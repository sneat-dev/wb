package deps

import (
	"fmt"
	"strings"
)

// pnpmWorkspaceRef is one dependency-version reference found inside the
// `overrides:`, `catalog:`, or `catalogs:` blocks of a pnpm-workspace.yaml
// manifest. Pnpm 11 no longer reads the legacy `pnpm.overrides` block from
// package.json, so this is the only place a fleet-wide version pin for an
// `@scope/name` package lives once a repo adopts workspace overrides.
type pnpmWorkspaceRef struct {
	Line        int    // zero-based line index of the "key: value" entry
	Section     string // "overrides", "catalog", or "catalogs"
	CatalogName string // set only when Section == "catalogs"
	Key         string // dependency name, already unquoted
	Value       string // version literal, already unquoted
	quote       byte   // 0, '\'', or '"' — the quote style the value used, preserved on rewrite
	comment     string // trailing "# ..." comment, preserved verbatim on rewrite
}

// pnpmWorkspaceLine is one parsed line of a pnpm-workspace.yaml manifest. Only
// mapping entries ("key: value" or "key:") are decoded; list items, blank
// lines, and comment-only lines are preserved as opaque text.
type pnpmWorkspaceLine struct {
	raw       string
	indent    int
	isMapping bool
	key       string
	keyQuote  byte
	value     string
	valueSet  bool // true when the line has content after the colon (a leaf, not a section header)
	quote     byte
	comment   string
}

// parsePnpmWorkspaceLine decodes one line of pnpm-workspace.yaml into its
// indentation and, when present, its "key: value" mapping shape. YAML forbids
// an unquoted plain scalar from starting with '@' or '`', so every
// `@scope/name` dependency key in an overrides or catalog block is written
// quoted; this parser only recognizes quoted or bare-word keys, which covers
// every form pnpm itself writes and every form a human is likely to add by
// hand.
func parsePnpmWorkspaceLine(raw string) pnpmWorkspaceLine {
	line := pnpmWorkspaceLine{raw: raw}
	trimmedLeft := strings.TrimLeft(raw, " ")
	line.indent = len(raw) - len(trimmedLeft)
	body := strings.TrimRight(trimmedLeft, "\r\n")
	if body == "" || strings.HasPrefix(body, "#") || strings.HasPrefix(body, "-") {
		return line
	}
	rest := body
	var key string
	var keyQuote byte
	if len(rest) > 0 && (rest[0] == '\'' || rest[0] == '"') {
		quoteChar := rest[0]
		closing := indexUnescapedQuote(rest[1:], quoteChar)
		if closing < 0 {
			return line
		}
		key = rest[1 : 1+closing]
		keyQuote = quoteChar
		rest = rest[1+closing+1:]
	} else {
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return line
		}
		key = rest[:colon]
		rest = rest[colon:]
	}
	rest = strings.TrimLeft(rest, " ")
	if !strings.HasPrefix(rest, ":") {
		return line
	}
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimLeft(rest, " \t")
	line.isMapping = true
	line.key = key
	line.keyQuote = keyQuote
	if rest == "" {
		return line
	}
	value, quote, comment := parsePnpmWorkspaceValue(rest)
	line.value = value
	line.quote = quote
	line.comment = comment
	line.valueSet = true
	return line
}

// indexUnescapedQuote finds the closing quote of a YAML quoted scalar. Single
// quotes escape by doubling (”) and double quotes escape with a backslash;
// dependency names and versions never legitimately contain either, so this
// only needs to not crash on them, not round-trip them perfectly.
func indexUnescapedQuote(value string, quote byte) int {
	for index := 0; index < len(value); index++ {
		if value[index] == quote {
			if quote == '\'' && index+1 < len(value) && value[index+1] == '\'' {
				index++
				continue
			}
			return index
		}
		if quote == '"' && value[index] == '\\' {
			index++
		}
	}
	return -1
}

// parsePnpmWorkspaceValue splits the remainder of a mapping line into its
// scalar value and an optional trailing "# comment".
func parsePnpmWorkspaceValue(rest string) (value string, quote byte, comment string) {
	if len(rest) > 0 && (rest[0] == '\'' || rest[0] == '"') {
		quoteChar := rest[0]
		closing := indexUnescapedQuote(rest[1:], quoteChar)
		if closing >= 0 {
			value = rest[1 : 1+closing]
			quote = quoteChar
			trailing := strings.TrimSpace(rest[1+closing+1:])
			if strings.HasPrefix(trailing, "#") {
				comment = trailing
			}
			return value, quote, comment
		}
	}
	if hash := strings.Index(rest, " #"); hash >= 0 {
		comment = strings.TrimSpace(rest[hash+1:])
		rest = rest[:hash]
	} else if strings.HasPrefix(rest, "#") {
		comment = rest
		rest = ""
	}
	value = strings.TrimSpace(rest)
	return value, 0, comment
}

// renderPnpmWorkspaceValue re-encodes a version literal using the same quote
// style the original line used, so an override rewrite touches only the
// version characters an operator would expect to change.
func renderPnpmWorkspaceValue(value string, quote byte, comment string) string {
	var rendered string
	switch quote {
	case '\'':
		rendered = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	case '"':
		rendered = "\"" + strings.ReplaceAll(value, "\"", "\\\"") + "\""
	default:
		rendered = value
	}
	if comment != "" {
		rendered += " " + comment
	}
	return rendered
}

// scanPnpmWorkspaceRefs walks every line of a pnpm-workspace.yaml manifest and
// returns every dependency-version entry declared under `overrides:`,
// `catalog:`, or `catalogs:`. It tracks structure purely through indentation,
// exactly as YAML itself does for this shallow, well-known shape.
func scanPnpmWorkspaceRefs(contents []byte) []pnpmWorkspaceRef {
	rawLines := splitPreservingLineEndings(string(contents))
	parsed := make([]pnpmWorkspaceLine, len(rawLines))
	for index, raw := range rawLines {
		parsed[index] = parsePnpmWorkspaceLine(raw)
	}
	var refs []pnpmWorkspaceRef
	section := "" // "" | "overrides" | "catalog" | "catalogs"
	sectionIndent := -1
	catalogName := ""
	catalogIndent := -1
	for index, line := range parsed {
		if !line.isMapping {
			continue
		}
		// A line at or above the active section's own indent closes it (and,
		// for catalogs, closes any open catalog-name block too).
		if section != "" && line.indent <= sectionIndent {
			section, sectionIndent, catalogName, catalogIndent = "", -1, "", -1
		}
		if section == "catalogs" && catalogName != "" && line.indent <= catalogIndent {
			catalogName, catalogIndent = "", -1
		}
		if section == "" && !line.valueSet {
			switch line.key {
			case "overrides", "catalog", "catalogs":
				section, sectionIndent = line.key, line.indent
			}
			continue
		}
		switch section {
		case "overrides", "catalog":
			if line.valueSet && line.indent > sectionIndent {
				refs = append(refs, pnpmWorkspaceRef{
					Line: index, Section: section, Key: line.key, Value: line.value,
					quote: line.quote, comment: line.comment,
				})
			}
		case "catalogs":
			if catalogName == "" {
				if !line.valueSet && line.indent > sectionIndent {
					catalogName, catalogIndent = line.key, line.indent
				}
				continue
			}
			if line.valueSet && line.indent > catalogIndent {
				refs = append(refs, pnpmWorkspaceRef{
					Line: index, Section: section, CatalogName: catalogName, Key: line.key, Value: line.value,
					quote: line.quote, comment: line.comment,
				})
			} else if line.indent <= catalogIndent {
				catalogName, catalogIndent = "", -1
			}
		}
	}
	return refs
}

// applyPnpmWorkspaceOverride rewrites every `overrides:`, `catalog:`, and
// `catalogs:` entry for one dependency to an exact version, preserving every
// other line byte-for-byte, including comments, quote style, and key
// formatting. It returns the updated contents and the refs it changed.
func applyPnpmWorkspaceOverride(contents []byte, dependency, version string) ([]byte, []pnpmWorkspaceRef, error) {
	refs := scanPnpmWorkspaceRefs(contents)
	rawLines := splitPreservingLineEndings(string(contents))
	var matched []pnpmWorkspaceRef
	for _, ref := range refs {
		if ref.Key != dependency {
			continue
		}
		if ref.Line < 0 || ref.Line >= len(rawLines) {
			return nil, nil, fmt.Errorf("pnpm-workspace.yaml: override line index out of range")
		}
		parsedLine := parsePnpmWorkspaceLine(rawLines[ref.Line])
		lineEnding := lineEndingOf(rawLines[ref.Line])
		indent := strings.Repeat(" ", parsedLine.indent)
		keyRendered := renderPnpmWorkspaceKey(parsedLine.key, parsedLine.keyQuote)
		newValue := renderPnpmWorkspaceValue(version, ref.quote, ref.comment)
		rawLines[ref.Line] = indent + keyRendered + ": " + newValue + lineEnding
		matched = append(matched, ref)
	}
	return []byte(strings.Join(rawLines, "")), matched, nil
}

func renderPnpmWorkspaceKey(key string, quote byte) string {
	switch quote {
	case '\'':
		return "'" + strings.ReplaceAll(key, "'", "''") + "'"
	case '"':
		return "\"" + strings.ReplaceAll(key, "\"", "\\\"") + "\""
	default:
		return key
	}
}

// splitPreservingLineEndings splits text into lines, keeping each line's
// trailing "\n" or "\r\n" attached so the caller can rejoin with
// strings.Join(lines, "") and reproduce the original file exactly.
func splitPreservingLineEndings(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines = append(lines, text[start:index+1])
			start = index + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

func lineEndingOf(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}
