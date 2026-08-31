package deps

import (
	"fmt"
	"regexp"
	"strings"
)

// npmDependencyFieldNames is the canonical package.json dependency-field set
// for both fleet discovery and published-release verification. Normal, dev,
// peer, and optional dependencies all express a version selection in this
// fleet. `overrides`/`resolutions`-style npm/yarn fields are intentionally out
// of scope here; this fleet's cross-package pins live in pnpm-workspace.yaml
// (see npm_pnpm_workspace.go).
var npmDependencyFieldNames = []string{
	"dependencies",
	"devDependencies",
	"peerDependencies",
	"optionalDependencies",
}

var npmDependencyFields = func() map[string]bool {
	fields := make(map[string]bool, len(npmDependencyFieldNames))
	for _, field := range npmDependencyFieldNames {
		fields[field] = true
	}
	return fields
}()

// npmPackageJSONRef is one dependency-version reference found inside one of
// the four dependency fields of a package.json manifest.
type npmPackageJSONRef struct {
	Line  int // zero-based line index of the "key": "value" entry
	Field string
	Key   string
	Value string
}

var (
	npmObjectOpenPattern  = regexp.MustCompile(`^(\s*)"((?:[^"\\]|\\.)*)":\s*\{\s*$`)
	npmObjectClosePattern = regexp.MustCompile(`^(\s*)\}\s*,?\s*$`)
	npmStringEntryPattern = regexp.MustCompile(`^(\s*)"((?:[^"\\]|\\.)*)":\s*"((?:[^"\\]|\\.)*)"\s*,?\s*$`)
)

// scanNpmPackageJSONRefs finds every dependency entry inside dependencies,
// devDependencies, peerDependencies, and optionalDependencies. It tracks
// object nesting through indentation exactly as Prettier writes it — one key
// per line, 2-space (or repo-configured) steps — which is how every
// package.json in this fleet is formatted. A minified or single-line
// package.json is out of scope; every field it declares would simply be
// reported absent, which is a safe (if less useful) fallback, never a
// mismatched edit.
func scanNpmPackageJSONRefs(contents []byte) []npmPackageJSONRef {
	lines := splitPreservingLineEndings(string(contents))
	var refs []npmPackageJSONRef
	field := ""
	fieldIndent := -1
	for index, raw := range lines {
		if open := npmObjectOpenPattern.FindStringSubmatch(raw); open != nil {
			indent := len(open[1])
			if field != "" && indent <= fieldIndent {
				field, fieldIndent = "", -1
			}
			if field == "" && npmDependencyFields[open[2]] {
				field, fieldIndent = open[2], indent
			}
			continue
		}
		if close := npmObjectClosePattern.FindStringSubmatch(raw); close != nil {
			indent := len(close[1])
			if field != "" && indent <= fieldIndent {
				field, fieldIndent = "", -1
			}
			continue
		}
		if field == "" {
			continue
		}
		if entry := npmStringEntryPattern.FindStringSubmatch(raw); entry != nil {
			indent := len(entry[1])
			if indent > fieldIndent {
				refs = append(refs, npmPackageJSONRef{Line: index, Field: field, Key: entry[2], Value: entry[3]})
			}
		}
	}
	return refs
}

// applyNpmPackageJSONOverride rewrites every dependency-field entry for one
// package to an exact version, preserving every other byte of the file
// (indentation, key quoting, trailing comma, and line ending) so the diff a
// reviewer sees is exactly the version characters that changed.
func applyNpmPackageJSONOverride(contents []byte, dependency, version string) ([]byte, []npmPackageJSONRef, error) {
	refs := scanNpmPackageJSONRefs(contents)
	lines := splitPreservingLineEndings(string(contents))
	var matched []npmPackageJSONRef
	for _, ref := range refs {
		if ref.Key != dependency {
			continue
		}
		if ref.Line < 0 || ref.Line >= len(lines) {
			return nil, nil, fmt.Errorf("package.json: dependency line index out of range")
		}
		raw := lines[ref.Line]
		match := npmStringEntryPattern.FindStringSubmatch(raw)
		if match == nil {
			return nil, nil, fmt.Errorf("package.json: could not re-match dependency line %d", ref.Line+1)
		}
		trailingComma := ""
		if strings.HasSuffix(strings.TrimRight(raw, "\r\n"), ",") {
			trailingComma = ","
		}
		ending := lineEndingOf(raw)
		lines[ref.Line] = match[1] + `"` + ref.Key + `": "` + version + `"` + trailingComma + ending
		matched = append(matched, ref)
	}
	return []byte(strings.Join(lines, "")), matched, nil
}
