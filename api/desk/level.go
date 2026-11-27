package desk

import (
	"encoding/json"
	"strings"
)

// withheldAtRepositories is the one definition of what the `repositories`
// detail level withholds from an envelope: the daemon's reduction is tested
// against it and Desk refuses a snapshot that carries any of it
// (desk-connect#req:detail-level-applied-on-the-machine). A path is dotted,
// and `[]` marks each element of an array.
var withheldAtRepositories = []string{
	"fleet.worktrees[].name",
	"fleet.worktrees[].task",
	"fleet.worktrees[].stream",
	"fleet.worktrees[].branch",
	"fleet.pull_requests[].worktree",
	"fleet.pull_requests[].branch",
	"fleet.pull_requests[].failed_check",
	"fleet.agents[].task",
	"fleet.agents[].worktrees",
	"fleet.throughput.slowest[].task",
}

// worktreeBranch is the one withheld path with an exception: a worktree on
// its repository's default branch may name it, since the repository already
// does.
const worktreeBranch = "fleet.worktrees[].branch"

// WithheldAtRepositories returns the envelope paths the `repositories` level
// withholds, in a stable order.
func WithheldAtRepositories() []string {
	return append([]string(nil), withheldAtRepositories...)
}

// CheckLevel reports whether envelope carries anything above level. At `full`
// every envelope passes; at `repositories` one that carries a withheld path
// with a value, or a value of the wrong JSON type where a withheld path
// leads, is refused. The error names the path, never the value.
func CheckLevel(envelope json.RawMessage, level Level) error {
	var root map[string]any
	if err := json.Unmarshal(envelope, &root); err != nil || root == nil {
		return invalid("envelope")
	}
	switch level {
	case LevelFull:
		return nil
	case LevelRepositories:
	default:
		return invalid("detail")
	}
	defaults := defaultBranches(root)
	onDefault := func(worktree map[string]any, value any) bool {
		branch, _ := value.(string)
		repository, _ := worktree["repository"].(string)
		return branch != "" && defaults[repository] == branch
	}
	for _, path := range withheldAtRepositories {
		var allowed func(map[string]any, any) bool
		if path == worktreeBranch {
			allowed = onDefault
		}
		if carries(root, strings.Split(path, "."), allowed) {
			return invalid("%s is withheld at %s detail", path, level)
		}
	}
	return nil
}

// defaultBranches maps each repository id to its default branch.
func defaultBranches(root map[string]any) map[string]string {
	defaults := map[string]string{}
	fleet, _ := root["fleet"].(map[string]any)
	repositories, _ := fleet["repositories"].([]any)
	for _, item := range repositories {
		repository, _ := item.(map[string]any)
		id, _ := repository["id"].(string)
		branch, _ := repository["default_branch"].(string)
		if id != "" && branch != "" {
			defaults[id] = branch
		}
	}
	return defaults
}

// carries reports whether node holds a value at the path segments, other
// than one allowed reports as permitted in its parent object. A non-null
// value of the wrong type on the way counts as carrying: a malformed
// envelope cannot hide a field.
func carries(node any, segments []string, allowed func(map[string]any, any) bool) bool {
	if node == nil {
		return false
	}
	object, ok := node.(map[string]any)
	if !ok {
		return true
	}
	key, each := strings.CutSuffix(segments[0], "[]")
	child := object[key]
	switch {
	case len(segments) == 1:
		return !empty(child) && (allowed == nil || !allowed(object, child))
	case !each:
		return carries(child, segments[1:], allowed)
	case child == nil:
		return false
	}
	items, ok := child.([]any)
	if !ok {
		return true
	}
	for _, item := range items {
		if carries(item, segments[1:], allowed) {
			return true
		}
	}
	return false
}

// empty reports whether value carries nothing: absent, null, "", [] or {}.
func empty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}
