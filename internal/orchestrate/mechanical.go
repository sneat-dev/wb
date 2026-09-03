package orchestrate

import (
	"encoding/json"
	"path"
	"strings"
)

// A mechanical bump is a change whose gate is the batch verification rather
// than a reviewer. Getting the boundary wrong is expensive in one direction
// only: a change that needed review and did not get one lands unreviewed, and
// nothing downstream notices.
//
// Filenames cannot decide it. `package.json` holds dependency ranges, and it
// also holds the `scripts` that run in CI, the `engines` that gate every
// install, and `pnpm.overrides`, which rewrites the dependency graph for every
// package in the workspace. A change to any of those is a change to how the
// software is built or run, and calling it mechanical because it happened to
// be spelled inside a manifest is how an unreviewed change reaches the default
// branch. So the classification reads the diff.

// mechanicalManifests are the files that may appear in a mechanical bump at
// all. Presence is necessary and never sufficient — see mechanicalHunks.
var mechanicalManifests = map[string]bool{
	"go.mod":              true,
	"go.sum":              true,
	"package.json":        true,
	"pnpm-lock.yaml":      true,
	"pnpm-workspace.yaml": true,
	"package-lock.json":   true,
}

// codeDirectories hold files that are code whatever they are named. A
// `package.json` under testdata/ is a fixture: changing it changes what the
// tests assert, which is exactly the kind of change a reviewer is for.
var codeDirectories = []string{"testdata/", "docs/", "examples/", "fixtures/"}

// mechanicalDependencySections are the `package.json` keys a dependency bump
// may touch.
var mechanicalDependencySections = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"peerDependencies":     true,
	"optionalDependencies": true,
}

// graphRewritingKeys change what the resolver produces for packages other than
// the one being edited, so they are never a version bump however much the line
// looks like one.
//
// `"semver": "7.6.0"` inside `overrides` reads exactly like the same line
// inside `dependencies`, and it means something entirely different: every
// package in the workspace that asked for any other semver now gets this one.
// A `replace` in `go.mod` is the same move in the other ecosystem, pointing a
// module path at something the registry never served.
var graphRewritingKeys = map[string]bool{
	"overrides":           true,
	"resolutions":         true,
	"packageExtensions":   true,
	"patchedDependencies": true,
	"peerDependencyRules": true,
	"catalog":             true,
	"catalogs":            true,
}

// sectionFromHunkHeader reads the enclosing context Git appends to a hunk
// header, so a hunk that shows no `"section": {` line of its own is still
// placed correctly.
func sectionFromHunkHeader(line string) []string {
	index := strings.Index(line[2:], "@@")
	if index < 0 {
		return nil
	}
	context := strings.TrimSpace(line[2+index+2:])
	if context == "" {
		return nil
	}
	key, _, ok := jsonLineKey(context)
	if !ok || !strings.HasSuffix(strings.TrimSpace(context), "{") {
		return nil
	}
	return []string{key}
}

// nonMechanicalGoModule refuses the `go.mod` directives that are decisions
// rather than versions.
//
// `require` is a version bump. `replace` and `exclude` rewrite what the module
// graph resolves to, and `go`/`toolchain` change the language and compiler the
// whole module is built with — none of which a batch verification can stand in
// for a reviewer on.
func nonMechanicalGoModule(name, patch string) string {
	for _, line := range changedPatchLines(patch) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "replace", "exclude":
			return name + " changes a " + fields[0] + " directive, which rewrites what the module graph resolves to"
		case "go", "toolchain":
			return name + " changes the " + fields[0] + " directive, which changes how the whole module is built"
		}
	}
	return ""
}

// nonMechanicalWorkspace refuses the pnpm workspace keys that rewrite the
// graph. The file is in the manifest list because a bump can add a catalogued
// version, but the same file carries overrides and package extensions.
func nonMechanicalWorkspace(name, patch string) string {
	// Every line of the hunk, context included. A change under `overrides:`
	// shows the key as CONTEXT and only the version as changed — reading the
	// changed lines alone sees `semver: 7.6.0` and cannot tell it from a
	// catalogued version bump. If a graph-rewriting key is anywhere in the
	// hunk, the hunk is in or beside one, and that is enough to want a reader.
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		body := strings.TrimSpace(strings.TrimLeft(line, "+- "))
		key := strings.Trim(strings.TrimSpace(strings.SplitN(body, ":", 2)[0]), `"'`)
		if graphRewritingKeys[key] {
			return name + " touches " + key + ", which rewrites what the workspace resolves for every package"
		}
	}
	return ""
}

// changedPatchLines yields the added and removed lines of a patch, without
// their diff marker.
func changedPatchLines(patch string) []string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(patch, "\n") {
		if len(line) == 0 || line[0] != '+' && line[0] != '-' {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		lines = append(lines, strings.TrimSpace(line[1:]))
	}
	return lines
}

// ChangedFile is one file of a pull request's diff, with the patch GitHub
// returns for it.
type ChangedFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Patch    string `json:"patch"`
}

// MechanicalVerdict explains a classification well enough to argue with.
type MechanicalVerdict struct {
	Mechanical bool
	// Reasons name each file that made the change non-mechanical, and why.
	Reasons []string
	// NonManifest is the subset of Reasons that is just "this is code".
	NonManifest []string
}

// ClassifyMechanical decides from the diff whether a change is a mechanical
// dependency bump.
func ClassifyMechanical(files []ChangedFile) MechanicalVerdict {
	if len(files) == 0 {
		// An empty diff is not a bump; it is a pull request with nothing in it.
		return MechanicalVerdict{Reasons: []string{"the pull request changes no files"}}
	}
	verdict := MechanicalVerdict{Mechanical: true}
	for _, file := range files {
		name := file.Filename
		if inCodeDirectory(name) {
			verdict.Mechanical = false
			verdict.NonManifest = append(verdict.NonManifest, name)
			verdict.Reasons = append(verdict.Reasons,
				name+" is under a directory whose contents are code, whatever the file is named")
			continue
		}
		if !mechanicalManifests[path.Base(name)] {
			verdict.Mechanical = false
			verdict.NonManifest = append(verdict.NonManifest, name)
			verdict.Reasons = append(verdict.Reasons, name+" is not a dependency manifest")
			continue
		}
		if reason := nonMechanicalContent(name, file.Patch); reason != "" {
			verdict.Mechanical = false
			verdict.Reasons = append(verdict.Reasons, reason)
		}
	}
	return verdict
}

func inCodeDirectory(name string) bool {
	normalized := strings.TrimPrefix(name, "./")
	for _, directory := range codeDirectories {
		if strings.HasPrefix(normalized, directory) || strings.Contains(normalized, "/"+directory) {
			return true
		}
	}
	return false
}

// nonMechanicalContent inspects the hunks of a manifest change and returns the
// reason it is not mechanical, or "".
//
// A patch GitHub could not produce — a file too large to diff, a rename with no
// content change — is treated as not mechanical. Absence of evidence is not
// evidence that a change is safe to land unreviewed.
func nonMechanicalContent(name, patch string) string {
	if strings.TrimSpace(patch) == "" {
		return name + " has no diff GitHub could show, so its content cannot be classified"
	}
	switch path.Base(name) {
	case "package.json":
		// Checked line by line below.
	case "go.mod":
		return nonMechanicalGoModule(name, patch)
	case "pnpm-workspace.yaml":
		return nonMechanicalWorkspace(name, patch)
	default:
		// go.sum and the lockfiles carry only resolved dependency data, and a
		// lockfile is a *consequence* of a manifest edit rather than a decision
		// of its own. There is no non-dependency region inside them to protect.
		return ""
	}
	// Track the object the diff is inside. A version bump and a `pnpm.overrides`
	// rewrite look identical line by line — `"semver": "7.6.0"` either way —
	// and only the enclosing section tells them apart. The hunk's context lines
	// carry that section, which is why they are read rather than skipped.
	section := make([]string, 0, 4)
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			// Git puts the enclosing context after the second `@@` — for a
			// manifest that is usually the very section the hunk is inside:
			//
			//   @@ -12,7 +12,7 @@   "dependencies": {
			//
			// Skipping it left the stack empty for every hunk whose only
			// context is in the header, so a real dependency bump read as
			// "outside any dependency section" and was refused. Seed the stack
			// from it instead.
			section = sectionFromHunkHeader(line)
			continue
		}
		changed := len(line) > 0 && (line[0] == '+' || line[0] == '-')
		body := line
		if len(line) > 0 && (line[0] == '+' || line[0] == '-' || line[0] == ' ') {
			body = line[1:]
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		if key, _, ok := jsonLineKey(body); ok && strings.HasSuffix(body, "{") {
			section = append(section, key)
			if changed && !mechanicalDependencySections[key] {
				return name + " changes the " + key + " section, which is not a dependency section"
			}
			continue
		}
		if body == "}" || body == "}," || body == "{" {
			if body != "{" && len(section) > 0 {
				section = section[:len(section)-1]
			}
			continue
		}
		if !changed {
			continue
		}
		key, value, ok := jsonLineKey(body)
		if !ok {
			return name + " changes a line that is not a single JSON property: " + trimForMessage(body)
		}
		enclosing := ""
		if len(section) > 0 {
			enclosing = section[len(section)-1]
		}
		if graphRewritingKeys[enclosing] || graphRewritingKeys[key] {
			rewriting := enclosing
			if graphRewritingKeys[key] {
				rewriting = key
			}
			return name + " changes " + rewriting + ", which rewrites what the resolver produces for " +
				"packages other than the one being edited"
		}
		if !mechanicalDependencySections[enclosing] {
			where := "outside any dependency section"
			if enclosing != "" {
				where = "inside " + enclosing
			}
			return name + " changes " + key + " " + where + ", which a dependency bump does not touch"
		}
		if !looksLikeDependencyEntry(value) {
			return name + " changes " + key + " to something that is not a version range: " + trimForMessage(value)
		}
	}
	return ""
}

// jsonLineKey reads the property name from one line of a JSON diff.
func jsonLineKey(body string) (key, value string, ok bool) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(body), ",")
	if !strings.HasPrefix(trimmed, `"`) {
		return "", "", false
	}
	end := strings.Index(trimmed[1:], `"`)
	if end < 0 {
		return "", "", false
	}
	key = trimmed[1 : end+1]
	rest := strings.TrimSpace(trimmed[end+2:])
	if !strings.HasPrefix(rest, ":") {
		return "", "", false
	}
	return key, strings.TrimSpace(strings.TrimPrefix(rest, ":")), true
}

// looksLikeDependencyEntry reports whether a changed property is a package
// name mapped to a version range. It is what distinguishes
// `"lodash": "^4.17.21"` from `"build": "tsc && node scripts/x.js"`.
func looksLikeDependencyEntry(value string) bool {
	var decoded string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return false
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return false
	}
	// A version range is short, has no spaces, and starts with a digit or one
	// of the range operators. A script contains spaces and shell.
	if strings.ContainsAny(decoded, " \t&|;") {
		return false
	}
	switch decoded[0] {
	case '^', '~', '>', '<', '=', '*':
		return true
	}
	if decoded[0] >= '0' && decoded[0] <= '9' {
		return true
	}
	// workspace:, catalog:, npm: and file: protocols are dependency spellings.
	for _, prefix := range []string{"workspace:", "catalog:", "npm:", "file:", "link:", "git+", "github:"} {
		if strings.HasPrefix(decoded, prefix) {
			return true
		}
	}
	return false
}

func trimForMessage(value string) string {
	const limit = 60
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// Summary renders the verdict for a refusal.
func (verdict MechanicalVerdict) Summary() string {
	if verdict.Mechanical {
		return "every changed line is a dependency edit in a manifest"
	}
	if len(verdict.Reasons) == 0 {
		return "the change is not a mechanical dependency bump"
	}
	return strings.Join(limitStrings(verdict.Reasons, 5), "; ")
}
