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
// may touch. `pnpm.overrides` is deliberately absent: an override rewrites the
// resolved graph for every package in the workspace, which is a decision about
// the software rather than a version bump.
var mechanicalDependencySections = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"peerDependencies":     true,
	"optionalDependencies": true,
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
	if path.Base(name) != "package.json" {
		// go.mod, go.sum and the lockfiles carry only resolved dependency
		// data; there is no non-dependency region inside them to protect.
		return ""
	}
	// Track the object the diff is inside. A version bump and a `pnpm.overrides`
	// rewrite look identical line by line — `"semver": "7.6.0"` either way —
	// and only the enclosing section tells them apart. The hunk's context lines
	// carry that section, which is why they are read rather than skipped.
	section := make([]string, 0, 4)
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
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
