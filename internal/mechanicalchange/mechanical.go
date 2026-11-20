package mechanicalchange

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
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
// may touch. `overrides` (npm and `pnpm.overrides`) and `resolutions` (yarn)
// are dependency pins: a bot that keeps a family of packages on one version
// bumps them in the same commit as `dependencies`, and a version-shaped edit
// there is a bump. Only the version-shaped value is accepted (see
// looksLikeDependencyEntry), so a nested override object or a script-like
// value still reads as a decision.
var mechanicalDependencySections = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"peerDependencies":     true,
	"optionalDependencies": true,
	"overrides":            true,
	"resolutions":          true,
}

// pinnedSections are the `package.json` sections of mechanicalDependencySections
// that redirect resolution instead of declaring a dependency. They are held to
// a stricter rule than `dependencies`: a changed line must REPLACE an existing
// pin of the same key (never add, remove or rename one, never open or rewrite
// the section) and its value must be a registry semver version or range, never
// a protocol (`npm:`, `github:`, `git+`, `file:`, `link:`, ...). An override
// that points a package at another package, a git URL or a path silently
// swaps the code every transitive user of that package runs.
var pinnedSections = map[string]bool{
	"overrides":   true,
	"resolutions": true,
}

// strictPinRange accepts only digits, `x`/`*` wildcards inside a version and
// the semver range operators: no `:` or `/`, so no protocol, alias, URL, path or
// git ref can pass.
var strictPinRange = regexp.MustCompile(
	`^(?:[\^~]|[<>]=?|=)?\s*v?\d+(?:\.(?:\d+|[xX*])){0,2}(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z.-]+)?` +
		`(?:(?:\s+|\s*\|\|\s*)(?:[\^~]|[<>]=?|=)?\s*v?\d+(?:\.(?:\d+|[xX*])){0,2}(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z.-]+)?)*$`)

func strictPinVersion(value string) bool {
	return strictPinRange.MatchString(strings.TrimSpace(value))
}

// pinChanges pairs, within one hunk, the removed and added pin lines of one
// key. A hunk whose added keys are not exactly its removed keys adds, removes
// or renames a pin, which is a decision rather than a version bump.
type pinChanges struct {
	removed, added map[string]int
}

func newPinChanges() *pinChanges {
	return &pinChanges{removed: map[string]int{}, added: map[string]int{}}
}

func (pins *pinChanges) record(sign byte, id string) {
	if sign == '+' {
		pins.added[id]++
	} else {
		pins.removed[id]++
	}
}

// unpaired returns the first pin key whose added and removed counts differ.
func (pins *pinChanges) unpaired() string {
	for id, count := range pins.added {
		if pins.removed[id] != count {
			return id
		}
	}
	for id, count := range pins.removed {
		if pins.added[id] != count {
			return id
		}
	}
	return ""
}

// addedKey returns the first key (in sorted order, so the reason is stable)
// added more often than it was removed, and "" when every added key was paired
// with a removal of the same key. Pairing is by key over everything recorded,
// never by position, so a line that moves (a re-sort) is a replacement.
func (pins *pinChanges) addedKey() string {
	keys := make([]string, 0, len(pins.added))
	for id, count := range pins.added {
		if count > pins.removed[id] {
			keys = append(keys, id)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// newDirectDependencyReason is the refusal for a direct dependency the change
// introduces: a bump replaces the version of a dependency the repository
// already chose, and choosing a new one is a decision a reviewer records.
func newDirectDependencyReason(name, dependency string) string {
	return name + " adds new direct dependency " + trimForMessage(dependency) + "; needs a recorded review"
}

// verify returns the refusal for an unpaired pin and starts the next hunk.
func (pins *pinChanges) verify(name string) string {
	id := pins.unpaired()
	pins.removed, pins.added = map[string]int{}, map[string]int{}
	if id == "" {
		return ""
	}
	return name + " adds, removes or renames the pin " + trimForMessage(strings.ReplaceAll(id, "\x00", " ")) +
		" instead of changing the version of an existing one"
}

// workspaceVersionSections are the top-level `pnpm-workspace.yaml` keys whose
// entries are package-name to version pins, the YAML twin of the `package.json`
// sections above.
var workspaceVersionSections = map[string]bool{
	"overrides": true,
	"catalog":   true,
	"catalogs":  true,
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

// goDirectives are the top-level `go.mod` directives. A line whose first word
// is one of them is a single-line directive; any other indented line inside a
// block is an entry of that block.
var goDirectives = map[string]bool{
	"module": true, "go": true, "toolchain": true, "godebug": true, "require": true,
	"replace": true, "exclude": true, "retract": true, "tool": true, "ignore": true,
}

// goBlock is where a `go.mod` line sits: the directive of the enclosing
// `directive (` block, "" at top level, and whether that is known at all. A
// hunk in the middle of a long block shows no opener of its own, so the head
// file places it (see goBlockAt); without that a changed entry cannot be
// attributed to a directive and is refused.
type goBlock struct {
	directive string
	known     bool
}

// goBlockAt returns the block enclosing the start of line `line` (1-based) of
// content, walking the same opener and closer lines the hunk loop walks.
func goBlockAt(content string, line int) goBlock {
	block := goBlock{known: true}
	for index, text := range strings.Split(content, "\n") {
		if index+1 >= line {
			break
		}
		fields := strings.Fields(text)
		switch {
		case len(fields) == 2 && fields[1] == "(":
			block.directive = fields[0]
		case len(fields) > 0 && fields[0] == ")":
			block.directive = ""
		}
	}
	return block
}

// goDirectiveReason says why a change to directive is not a version bump.
func goDirectiveReason(name, directive, line string) string {
	switch directive {
	case "":
		return name + " changes a line that cannot be placed in a go.mod directive or block, so it is not shown to be a require edit: " + trimForMessage(strings.TrimSpace(line))
	case "replace", "exclude":
		return name + " changes a " + directive + " directive, which rewrites what the module graph resolves to"
	case "go", "toolchain":
		return name + " changes the " + directive + " directive, which changes how the whole module is built"
	case "module":
		return name + " changes the module directive, which renames the module every consumer imports"
	case "tool":
		return name + " changes a tool directive, which names code that go tool and go generate execute"
	case "godebug":
		return name + " changes a godebug directive, which changes the module's runtime behaviour"
	}
	return name + " changes a " + directive + " directive, which is not a dependency version bump"
}

// nonMechanicalGoModule is deny-by-default: the only mechanical changes to
// go.mod are `require` lines (single-line, or inside a `require (` block) and
// the comments and blank lines around them. Within those, a version bump of an
// existing module and a new `// indirect` requirement are mechanical, and a
// new direct requirement is a decision. Every other directive, the opener or
// closer of any other block and every entry of one (`tool`, `godebug`,
// `exclude`, `replace`, `retract`, `ignore`, `module`, `go`, `toolchain`, an
// unknown one) is refused by name. `replace` and `exclude` rewrite what the
// module graph resolves to, `go`/`toolchain` change the compiler the whole
// module is built with, and `tool` and `godebug` name code to run and runtime
// behaviour: none of which a batch verification can stand in for a reviewer on.
func nonMechanicalGoModule(name, patch, content string) string {
	direct := newPinChanges()
	var block goBlock
	for _, raw := range strings.Split(patch, "\n") {
		if strings.HasPrefix(raw, "+++") || strings.HasPrefix(raw, "---") || strings.HasPrefix(raw, `\`) {
			continue
		}
		if strings.HasPrefix(raw, "@@") {
			block = goBlock{}
			if start, ok := hunkNewStart(raw); ok && content != "" {
				block = goBlockAt(content, start)
			}
			continue
		}
		changed := len(raw) > 0 && (raw[0] == '+' || raw[0] == '-')
		body := raw
		if len(raw) > 0 && (raw[0] == '+' || raw[0] == '-' || raw[0] == ' ') {
			body = raw[1:]
		}
		fields := strings.Fields(body)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "//") {
			continue
		}
		// A removed line is not in the file the hunk leaves behind, so it does
		// not move the block the following lines are in.
		newSide := !(len(raw) > 0 && raw[0] == '-')
		directive := block.directive
		switch {
		case fields[0] == ")":
			if changed && block.directive != "require" {
				// An unknown block has no directive, so the reason says it cannot be placed.
				return goDirectiveReason(name, block.directive, body)
			}
			if newSide {
				block = goBlock{known: true}
			}
			continue
		case len(fields) == 2 && fields[1] == "(":
			if changed && fields[0] != "require" {
				return goDirectiveReason(name, fields[0], body)
			}
			if newSide {
				block = goBlock{directive: fields[0], known: true}
			}
			continue
		case goDirectives[fields[0]] && !(block.known && block.directive != ""):
			// A single-line directive can only stand at top level.
			if newSide {
				block = goBlock{known: true}
			}
			directive = fields[0]
			fields = fields[1:]
		case !block.known:
			directive = ""
		}
		if !changed {
			continue
		}
		if directive != "require" {
			return goDirectiveReason(name, directive, body)
		}
		if len(fields) < 2 {
			return name + " changes a require line that is not a module and a version"
		}
		if len(fields) >= 4 && fields[2] == "//" && fields[3] == "indirect" {
			// A transitive requirement a bump brings in or drops.
			continue
		}
		direct.record(raw[0], fields[0])
	}
	if dependency := direct.addedKey(); dependency != "" {
		return newDirectDependencyReason(name, dependency)
	}
	return ""
}

// nonMechanicalWorkspace is deny-by-default: the only mechanical change to
// pnpm-workspace.yaml is the version of an EXISTING pin under `overrides`,
// `catalog` or `catalogs`. Every other changed line is a decision: a new,
// removed or renamed pin, a new group, any top-level key (`packages`,
// `onlyBuiltDependencies`, `dangerouslyAllowAllBuilds`, `packageExtensions`,
// ...) and any value that is not a plain registry version or range.
//
// The enclosing top-level key comes from the head file at the hunk's first
// line when it is available. Git's default hunk heading is not trusted: it
// skips quoted keys, so it can name an earlier section than the one the hunk
// is in. Without the head file only a column-0 line inside the hunk names the
// section, and a changed line with no known section is refused.
func nonMechanicalWorkspace(name, patch, content string) string {
	top, group := "", ""
	pins := newPinChanges()
	// catalogued pairs catalog entries over the whole file: a catalog entry is
	// a direct dependency of every package that says `catalog:`.
	catalogued := newPinChanges()
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if reason := pins.verify(name); reason != "" {
				return reason
			}
			top, group = "", ""
			if start, ok := hunkNewStart(line); ok && content != "" {
				top = workspaceTopAt(content, start)
			}
			continue
		}
		changed := len(line) > 0 && (line[0] == '+' || line[0] == '-')
		body := line
		if len(line) > 0 && (line[0] == '+' || line[0] == '-' || line[0] == ' ') {
			body = line[1:]
		}
		trimmed := strings.TrimSpace(body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, isProperty := yamlLineKey(trimmed)
		indent := len(body) - len(strings.TrimLeft(body, " "))
		if indent == 0 {
			if isProperty {
				top, group = key, ""
			}
			if changed {
				return name + " changes the top-level line " + trimForMessage(trimmed) +
					", and only the version of an existing pin under overrides, catalog or catalogs is mechanical"
			}
			continue
		}
		if !changed {
			if top == "catalogs" && indent == 2 && isProperty && value == "" {
				group = key
			}
			continue
		}
		if !workspaceVersionSections[top] {
			return name + " changes " + trimForMessage(trimmed) + " outside overrides, catalog and catalogs"
		}
		depth := 2
		if top == "catalogs" {
			depth = 4
		}
		if !isProperty || value == "" || indent != depth || !strictPinVersion(unquoteYAMLScalar(value)) {
			return name + " changes " + trimForMessage(trimmed) + " inside " + top +
				", which is not the version of a pin"
		}
		if top == "catalog" || top == "catalogs" {
			catalogued.record(line[0], top+"\x00"+group+"\x00"+key)
			continue
		}
		pins.record(line[0], top+"\x00"+group+"\x00"+key)
	}
	if reason := pins.verify(name); reason != "" {
		return reason
	}
	if id := catalogued.addedKey(); id != "" {
		return newDirectDependencyReason(name, id[strings.LastIndex(id, "\x00")+1:])
	}
	if id := catalogued.unpaired(); id != "" {
		return name + " removes the catalog entry " + trimForMessage(id[strings.LastIndex(id, "\x00")+1:]) +
			" instead of changing the version of an existing one"
	}
	return ""
}

// workspaceTopAt returns the top-level YAML key enclosing the start of line
// `line` (1-based) of content.
func workspaceTopAt(content string, line int) string {
	top := ""
	lines := strings.Split(content, "\n")
	if line > len(lines)+1 {
		// The patch starts past the end of the file it is supposed to be a
		// change to: the two do not describe the same commit.
		return ""
	}
	for index, text := range lines {
		if index+1 >= line {
			break
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || text[0] == ' ' || text[0] == '\t' {
			continue
		}
		if key, _, ok := yamlLineKey(trimmed); ok {
			top = key
		}
	}
	return top
}

// yamlLineKey reads `key: value` from one YAML line, unquoting the key.
func yamlLineKey(body string) (key, value string, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", false
	}
	if quote := body[0]; quote == '\'' || quote == '"' {
		end := strings.IndexByte(body[1:], quote)
		if end < 0 {
			return "", "", false
		}
		rest := strings.TrimSpace(body[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return body[1 : end+1], strings.TrimSpace(rest[1:]), true
	}
	index := strings.Index(body, ":")
	if index <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(body[:index]), strings.TrimSpace(body[index+1:]), true
}

// unquoteYAMLScalar drops one level of YAML quoting from a scalar.
func unquoteYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return value
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
	// ContentsURL is where GitHub serves the file at the pull request head.
	ContentsURL string `json:"contents_url"`
	// Content is the file at the pull request head, when the caller fetched
	// it. A hunk of a long manifest often starts deep inside a section whose
	// opening line is outside the hunk, and Git's default hunk heading never
	// matches a JSON line, so the patch alone cannot say which section a
	// changed line is in. Absent, such a hunk is refused rather than guessed.
	Content string `json:"-"`
}

// MechanicalVerdict explains a classification well enough to argue with.
type MechanicalVerdict struct {
	Mechanical bool
	// Reasons name each file that made the change non-mechanical, and why.
	Reasons []string
	// NonManifest is the subset of Reasons that is just "this is code".
	NonManifest []string
}

// ClassifyMechanical decides from the diff alone whether a change is a
// mechanical dependency bump. Without the pull request's author nothing
// outside a dependency manifest can be mechanical.
func ClassifyMechanical(files []ChangedFile) MechanicalVerdict {
	return ClassifyMechanicalPullRequest(files, PullRequest{})
}

// ClassifyMechanicalPullRequest is ClassifyMechanical with the pull request's
// author and title. A dependency bot's pull request may also change the
// version refs of a workflow file (see nonMechanicalWorkflow) and the
// `require` versions of a `go.mod`/`go.sum` fixture under `testdata/`.
func ClassifyMechanicalPullRequest(files []ChangedFile, pr PullRequest) MechanicalVerdict {
	if len(files) == 0 {
		// An empty diff is not a bump; it is a pull request with nothing in it.
		return MechanicalVerdict{Reasons: []string{"the pull request changes no files"}}
	}
	verdict := MechanicalVerdict{Mechanical: true}
	for _, file := range files {
		name := file.Filename
		if reason := staleHeadRefusal(file, pr.HeadSHA); reason != "" {
			verdict.Mechanical = false
			verdict.Reasons = append(verdict.Reasons, reason)
			continue
		}
		if pr.byDependencyBot() && workflowFile(name) {
			if reason := nonMechanicalWorkflow(name, file.Status, file.Patch, pr); reason != "" {
				verdict.Mechanical = false
				verdict.Reasons = append(verdict.Reasons, reason)
			}
			continue
		}
		if inCodeDirectory(name) && (!pr.byDependencyBot() || !goFixtureManifest(name)) {
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
		if reason := nonMechanicalContent(name, file.Patch, file.Content); reason != "" {
			verdict.Mechanical = false
			verdict.Reasons = append(verdict.Reasons, reason)
		}
	}
	return verdict
}

// staleHeadRefusal refuses a file that GitHub served at a different commit
// than the pull request head the merge is pinned to: a push before the file
// list was read, undone by a force-push before the merge, would otherwise get
// the benign head classified while the original lands.
func staleHeadRefusal(file ChangedFile, headSHA string) string {
	if headSHA == "" {
		return ""
	}
	// An empty `contents_url` is not "nothing to compare": GitHub always sends
	// one, so its absence means the file list is not what the API returned and
	// the commit it was read at cannot be proved.
	parsed, err := url.Parse(file.ContentsURL)
	if err != nil || parsed.Query().Get("ref") != headSHA {
		return file.Filename + " was read at a different commit than the pull request head " + trimForMessage(headSHA)
	}
	return ""
}

// goFixtureManifest reports whether name is a `go.mod` or `go.sum` whose only
// code directory is `testdata/`. Such a file is still checked line by line by
// nonMechanicalGoModule, so a `replace`, `exclude`, `go` or `toolchain` change
// in a fixture is refused like anywhere else; `docs/`, `examples/` and
// `fixtures/` stay code, and so does every non-Go manifest under `testdata/`.
func goFixtureManifest(name string) bool {
	base := path.Base(name)
	if base != "go.mod" && base != "go.sum" {
		return false
	}
	for _, directory := range codeDirectories {
		if directory != "testdata/" && hasCodeDirectory(name, directory) {
			return false
		}
	}
	return true
}

func hasCodeDirectory(name, directory string) bool {
	normalized := strings.TrimPrefix(name, "./")
	return strings.HasPrefix(normalized, directory) || strings.Contains(normalized, "/"+directory)
}

func inCodeDirectory(name string) bool {
	for _, directory := range codeDirectories {
		if hasCodeDirectory(name, directory) {
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
func nonMechanicalContent(name, patch, content string) string {
	if strings.TrimSpace(patch) == "" {
		return name + " has no diff GitHub could show, so its content cannot be classified"
	}
	switch path.Base(name) {
	case "package.json":
		// Checked line by line below.
	case "go.mod":
		return nonMechanicalGoModule(name, patch, content)
	case "pnpm-workspace.yaml":
		return nonMechanicalWorkspace(name, patch, content)
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
	pins := newPinChanges()
	// direct pairs, over the whole file, the dependency keys the patch removes
	// with the ones it adds (a re-sort moves a line between hunks).
	direct := newPinChanges()
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if reason := pins.verify(name); reason != "" {
				return reason
			}
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
			if start, ok := hunkNewStart(line); ok && content != "" {
				section = sectionStackAt(content, start)
			}
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
			if changed && (!mechanicalDependencySections[key] || pinnedSections[key]) {
				return name + " changes the " + key + " section, which is not a dependency section"
			}
			continue
		}
		if body == "}" || body == "}," || body == "{" {
			if body == "{" {
				section = append(section, "")
			} else if len(section) > 0 {
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
		if pinnedSections[enclosing] {
			var decoded string
			if json.Unmarshal([]byte(strings.TrimSuffix(value, ",")), &decoded) != nil || !strictPinVersion(decoded) {
				return name + " changes the pin " + key + " to something that is not a plain registry version or range: " +
					trimForMessage(value)
			}
			pins.record(line[0], strings.Join(section, "/")+"\x00"+key)
			continue
		}
		if !looksLikeDependencyEntry(value) {
			return name + " changes " + key + " to something that is not a version range: " + trimForMessage(value)
		}
		direct.record(line[0], enclosing+"\x00"+key)
	}
	if reason := pins.verify(name); reason != "" {
		return reason
	}
	if dependency := direct.addedKey(); dependency != "" {
		return newDirectDependencyReason(name, dependency[strings.Index(dependency, "\x00")+1:])
	}
	return ""
}

// hunkNewStart reads the first line number of the new side of a hunk header,
// `@@ -a,b +c,d @@`.
func hunkNewStart(header string) (int, bool) {
	plus := strings.Index(header, " +")
	if plus < 0 {
		return 0, false
	}
	digits := header[plus+2:]
	end := 0
	for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
		end++
	}
	number, err := strconv.Atoi(digits[:end])
	return number, err == nil && number > 0
}

// sectionStackAt returns the JSON objects enclosing the start of line `line`
// (1-based) of content, walking the same open and close lines the hunk loop
// walks. A bare `{` pushes an empty name so the closing brace stays balanced
// and the name can never match a dependency section.
func sectionStackAt(content string, line int) []string {
	section := make([]string, 0, 4)
	for index, text := range strings.Split(content, "\n") {
		if index+1 >= line {
			break
		}
		body := strings.TrimSpace(text)
		if key, _, ok := jsonLineKey(body); ok && strings.HasSuffix(body, "{") {
			section = append(section, key)
		} else if body == "{" {
			section = append(section, "")
		} else if (body == "}" || body == "},") && len(section) > 0 {
			section = section[:len(section)-1]
		}
	}
	return section
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
	if strings.ContainsAny(decoded, ":/@") && !strings.HasPrefix(decoded, "workspace:") && !strings.HasPrefix(decoded, "catalog:") {
		return false
	}
	if rest, ok := strings.CutPrefix(decoded, "workspace:"); ok {
		// `workspace:../../evil` and `workspace:@scope/other@1` are paths and
		// aliases, not a version: only the three bare specifiers and a plain
		// version or range point at a package of this workspace.
		return rest == "*" || rest == "^" || rest == "~" || strictPinVersion(rest)
	}
	switch decoded[0] {
	case '^', '~', '>', '<', '=', '*':
		return true
	}
	if decoded[0] >= '0' && decoded[0] <= '9' {
		return true
	}
	// workspace: and catalog: only point inside the repository. Every other
	// protocol or path (npm: aliases, github:, git+, file:, link:, http(s):,
	// patch:, portal:) names code the registry never served for this package.
	for _, prefix := range []string{"workspace:", "catalog:"} {
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

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	trimmed := append([]string(nil), values[:limit]...)
	return append(trimmed, fmt.Sprintf("and %d more", len(values)-limit))
}
