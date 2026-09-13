// Package envguard isolates a validation subprocess's environment from
// ambient machine state that has no lifecycle owner: a go.work file left
// above the process's TMPDIR or the repository being validated, an inherited
// GOWORK override, and WB_AGENT_* identity variables exported by whichever
// agent happens to be operating the shell that launched wb.
//
// Every validation subprocess -- go test/vet/build run by the quality gate,
// and the built-in Go pre-commit/pre-push hook blocks -- must not silently
// inherit these. Real evidence, 2026-09-07: a stray /private/tmp/go.work
// above TMPDIR=/private/tmp put every test temp module into Go workspace
// mode ("go: cannot load module … listed in go.work file",
// "-mod may only be set to readonly or vendor when in workspace mode"), and
// WB_AGENT_PID/WB_AGENT_RUNTIME/WB_AGENT_MODEL/WB_AGENT_ID exported by the
// operating agent were inherited by tests and changed verdicts
// ("worktree has an active owner", "agent-mode mutation requires a live
// registered session", an autoregister overwriting a parked row).
//
// A go.work is only ever recognized as "the repository's own" when it is a
// regular file: a symlinked go.work is always treated as ambient/WB-managed
// (GOWORK=off), the same fail-closed rule wb's local -link classifier
// applies to a symlink it does not resolve.
package envguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// AgentVarPrefix is the prefix every ambient agent-identity variable carries.
const AgentVarPrefix = "WB_AGENT_"

// IsAgentVar reports whether name is an ambient agent-identity variable.
func IsAgentVar(name string) bool {
	return strings.HasPrefix(name, AgentVarPrefix)
}

// AmbientInputs names the machine-state signals a validation subprocess must
// not silently inherit. Only names are ever captured -- WB_AGENT_* values
// never appear here, even in diagnostics.
type AmbientInputs struct {
	GoWorkAncestors []string `yaml:"go_work_ancestors,omitempty" json:"go_work_ancestors,omitempty"`
	GOWORK          string   `yaml:"gowork,omitempty" json:"gowork,omitempty"`
	AgentVars       []string `yaml:"agent_vars,omitempty" json:"agent_vars,omitempty"`
}

// Empty reports whether no ambient input was observed.
func (inputs AmbientInputs) Empty() bool {
	return len(inputs.GoWorkAncestors) == 0 && inputs.GOWORK == "" && len(inputs.AgentVars) == 0
}

// String renders a one-line "ambient inputs: …" summary for a failure detail,
// or "" when Empty.
func (inputs AmbientInputs) String() string {
	if inputs.Empty() {
		return ""
	}
	var parts []string
	if len(inputs.GoWorkAncestors) > 0 {
		parts = append(parts, "go.work: "+strings.Join(inputs.GoWorkAncestors, ", "))
	}
	if inputs.GOWORK != "" {
		parts = append(parts, "GOWORK="+inputs.GOWORK)
	}
	if len(inputs.AgentVars) > 0 {
		parts = append(parts, "agent vars: "+strings.Join(inputs.AgentVars, ", "))
	}
	return "ambient inputs: " + strings.Join(parts, "; ")
}

// Inspect reports the go.work files found in the ancestors of every
// directory in dirs (deduplicated and sorted), the gate's own GOWORK value,
// and the names of any WB_AGENT_* variable present in env (typically
// os.Environ()).
func Inspect(env []string, dirs ...string) AmbientInputs {
	var inputs AmbientInputs
	seen := map[string]bool{}
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, found := range goWorkAncestors(dir) {
			if !seen[found] {
				seen[found] = true
				inputs.GoWorkAncestors = append(inputs.GoWorkAncestors, found)
			}
		}
	}
	sort.Strings(inputs.GoWorkAncestors)
	agentSeen := map[string]bool{}
	for _, entry := range env {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if name == "GOWORK" && value != "" {
			inputs.GOWORK = value
		}
		if IsAgentVar(name) && !agentSeen[name] {
			agentSeen[name] = true
			inputs.AgentVars = append(inputs.AgentVars, name)
		}
	}
	sort.Strings(inputs.AgentVars)
	return inputs
}

// goWorkAncestors walks from dir up to the filesystem root, collecting every
// go.work file found along the way, nearest first.
func goWorkAncestors(dir string) []string {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	var found []string
	current := absolute
	for {
		candidate := filepath.Join(current, "go.work")
		if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode().IsRegular() {
			found = append(found, candidate)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return found
}

// SanitizeEnv derives a subprocess environment from base (typically
// os.Environ()): strips every WB_AGENT_* entry, then applies overrides in
// order, each winning over any earlier entry -- including one already
// present in base -- carrying the same key.
//
// Plain concatenation such as append(os.Environ(), "GOWORK=off") does not
// achieve an override: most C libraries' getenv scans forward and returns
// the FIRST match, so a value appended after an ambient duplicate of the
// same key is silently ignored by the child process. SanitizeEnv instead
// keeps exactly one entry per key, in first-seen order, holding the winning
// value.
func SanitizeEnv(base []string, overrides ...string) []string {
	order := make([]string, 0, len(base)+len(overrides))
	values := make(map[string]string, len(base)+len(overrides))
	present := make(map[string]bool, len(base)+len(overrides))
	apply := func(entry string, stripAgentVars bool) {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			return
		}
		if stripAgentVars && IsAgentVar(name) {
			delete(values, name)
			present[name] = false
			return
		}
		if !present[name] {
			order = append(order, name)
		}
		values[name] = value
		present[name] = true
	}
	for _, entry := range base {
		apply(entry, true)
	}
	for _, entry := range overrides {
		apply(entry, false)
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		if !present[name] {
			continue
		}
		result = append(result, name+"="+values[name])
	}
	return result
}

// TracksOwnGoWork reports whether repoRoot's HEAD commit tracks a go.work
// file that is unchanged in the working tree -- i.e. the repository being
// validated owns multi-module workspace mode intrinsically, rather than
// carrying an ambient or WB-managed link. A repository in this state must
// keep GOWORK enabled; every other repository gets GOWORK=off.
//
// go.work must be a regular file, not a symlink: this matches wb's local
// -link classifier, which fails closed (treats a symlinked go.work as not
// its own) rather than resolving what it points at.
//
// repoRoot need not be the git top level -- a workspace's go.work commonly
// sits below it (e.g. a nested/go.work with the git root above nested/).
// TracksOwnGoWork resolves the actual top level with `git rev-parse
// --show-toplevel` and runs both HEAD checks against go.work's path
// relative to that top level, never against a HEAD:go.work path assumed to
// be rooted at repoRoot itself.
func TracksOwnGoWork(repoRoot string) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(repoRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	repoRoot = resolvedRoot
	info, err := os.Lstat(filepath.Join(repoRoot, "go.work"))
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	toplevel, err := gitTopLevel(repoRoot)
	if err != nil {
		return false, err
	}
	if toplevel == "" {
		// Not inside a git repository at all: nothing to track against.
		return false, nil
	}
	relativePath, err := filepath.Rel(toplevel, filepath.Join(repoRoot, "go.work"))
	if err != nil {
		return false, err
	}
	tracked, err := gitPathExistsAtHEAD(toplevel, relativePath)
	if err != nil || !tracked {
		return false, err
	}
	return gitPathUnchangedFromHEAD(toplevel, relativePath)
}

// gitTopLevel resolves the git top-level directory containing dir, or ""
// (with a nil error) when dir is not inside a git repository at all.
func gitTopLevel(dir string) (string, error) {
	command := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// GoEnvOverrides returns the environment overrides a Go check against dir
// must carry: GOWORK=off, unless the nearest go.work in dir's own ancestry
// belongs to a repository that tracks it, unchanged, in HEAD -- i.e. dir's
// module is intrinsically part of that repository's own committed
// workspace, rather than picking up an ambient or WB-managed link. dir is
// typically one Go module directory within a repository, which for a
// workspace repository is a use entry below the go.work itself, so the
// search walks upward rather than checking dir alone. A git inspection
// failure fails closed toward isolation (GOWORK=off) rather than failing the
// check outright.
func GoEnvOverrides(dir string) []string {
	if repoRoot := nearestGoWorkDir(dir); repoRoot != "" {
		if tracksOwn, err := TracksOwnGoWork(repoRoot); err == nil && tracksOwn {
			return nil
		}
	}
	return []string{"GOWORK=off"}
}

// nearestGoWorkDir returns the directory of the closest go.work file found by
// walking upward from dir (inclusive), or "" when none exists in any
// ancestor.
func nearestGoWorkDir(dir string) string {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	current := absolute
	for {
		candidate := filepath.Join(current, "go.work")
		if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode().IsRegular() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func gitPathExistsAtHEAD(repoRoot, path string) (bool, error) {
	command := exec.Command("git", "-C", repoRoot, "cat-file", "-e", "HEAD:"+filepath.ToSlash(path))
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func gitPathUnchangedFromHEAD(repoRoot, path string) (bool, error) {
	command := exec.Command("git", "-C", repoRoot, "diff", "--quiet", "HEAD", "--", path)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
