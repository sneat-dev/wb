package agentguard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// gitInvocation is what survived parsing Git's global options.
type gitInvocation struct {
	// Directory is where the subcommand would operate: the shell's working
	// directory, moved by -C and --work-tree.
	Directory string
	// Subcommand is the first non-option word, empty when there is none.
	Subcommand string
	// Arguments are everything after the subcommand.
	Arguments []string
	// HooksPathOverride records `-c core.hooksPath=...`, which disables the
	// repository's managed Git hooks for that one invocation.
	HooksPathOverride bool
}

// inspectGit judges one `git ...` invocation.
//
// Git is where nearly every observed canonical-clone violation came from, and
// it is also the one program a canonical clone legitimately runs all day, so
// the verb table below is the sharpest part of the guard. Read verbs are
// allowed by name; write verbs are refused by name; the handful of verbs that
// are read or write depending on their flags — checkout, merge, pull, apply,
// clean, stash, switch — are decided from those flags; and an unrecognised
// verb is allowed.
func inspectGit(arguments []string, workingDirectory, projectsRoot string) *finding {
	invocation := parseGitGlobals(arguments, workingDirectory)
	if invocation.Subcommand == "" {
		return nil
	}
	// A managed-hook bypass has no legitimate reading anywhere WB manages —
	// not only a canonical clone. This is the exact construct that let a
	// commit land on 2026-08-27 after WB's pre-commit block had already
	// refused it (see lesson work-preservation-is-never-grounds-to-bypass-a-hook,
	// rule:hooks-are-never-bypassed), and a linked worktree's pre-push hook is
	// exactly as bypassable the same way. `wb` itself is never inspected here
	// (see inspectCommand's "wb" case in bash.go), so the sanctioned recovery
	// path — `wb worktree rescue --push` — is unaffected.
	if bypassesManagedHooks(invocation) {
		if location, ok := managedGitLocation(invocation.Directory, projectsRoot); ok {
			return &finding{Message: hookBypassRefusal(location, invocation)}
		}
		return nil
	}
	if invocation.Subcommand == "tag" || invocation.Subcommand == "push" {
		if result := inspectGitTagging(invocation.Subcommand, invocation.Arguments, invocation.Directory, projectsRoot); result != nil {
			return result
		}
	}
	location, ok := canonicalWorkingDirectory(invocation.Directory, projectsRoot)
	if !ok {
		return nil
	}
	if !gitSubcommandWrites(invocation.Subcommand, invocation.Arguments) {
		return nil
	}
	return &finding{Location: location, Detail: "git " + invocation.Subcommand}
}

// bypassesManagedHooks reports whether invocation disables Git's own hook
// mechanism, by any of the three constructs the lesson names:
//
//  1. `-c core.hooksPath=...` on the invocation itself (HooksPathOverride,
//     parsed in parseGitGlobals).
//  2. `--no-verify` on commit, push, or merge — the flag every version of
//     Git spells the same way for the hooks those three subcommands run.
//     `-n` is Git's short spelling of `--no-verify`, but only for `commit`:
//     `-n` means `--dry-run` on `push` and `--no-stat` on `merge`, and
//     treating those as a hook bypass would refuse a routine, safe dry run —
//     the false positive this guard exists to avoid. So the short flag is
//     checked for `commit` only; `push`/`merge` are covered by the long
//     `--no-verify` spelling alone.
//  3. `git config core.hooksPath ...` (get, set, or unset) — a standing
//     override that outlives the single invocation that made it, so reading
//     or writing it is refused outright rather than judged by intent.
func bypassesManagedHooks(invocation gitInvocation) bool {
	if invocation.HooksPathOverride {
		return true
	}
	switch invocation.Subcommand {
	case "commit", "push", "merge":
		if containsWord(invocation.Arguments, "--no-verify") {
			return true
		}
		if invocation.Subcommand == "commit" && containsShortFlag(invocation.Arguments, 'n') {
			return true
		}
	case "config":
		if configNamesHooksPath(invocation.Arguments) {
			return true
		}
	}
	return false
}

// configNamesHooksPath reports whether a `git config` invocation names
// core.hooksPath as the key it reads, sets, or removes — case-insensitively,
// matching Git's own comparison (see isHooksPathOverride).
func configNamesHooksPath(arguments []string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if isHooksPathOverride(argument) {
			return true
		}
		// The key is the first non-flag argument; later arguments are the
		// value being set. Stop at the first one considered.
		return false
	}
	return false
}

// managedGitLocation reports the WB-managed checkout — canonical clone or
// linked worktree, nested or not — that directory sits in, or false for
// anywhere else: a foreign checkout, an unresolved directory, or a location
// this guard cannot classify. A hook bypass is refused in any of them.
func managedGitLocation(directory, projectsRoot string) (Location, bool) {
	if directory == "" {
		return Location{}, false
	}
	location := Classify(projectsRoot, directory)
	if location.Kind == KindCanonical || location.Kind == KindLinked {
		return location, true
	}
	return Location{}, false
}

// hookBypassRefusal writes the message for a construct that disables Git's
// own managed hooks. It is deliberately distinct from the canonical-clone
// wording: the construct is refused everywhere WB manages, not because the
// location must "stay clean" but because the hook it disables is the safety
// signal itself (see work-preservation-is-never-grounds-to-bypass-a-hook).
func hookBypassRefusal(location Location, invocation gitInvocation) string {
	root := location.Root
	if root == "" {
		root = "this checkout"
	}
	var message strings.Builder
	fmt.Fprintf(&message, "git %s disables this checkout's managed hooks.\n", invocation.Subcommand)
	fmt.Fprintf(&message, "Refused in: %s\n\n", root)
	message.WriteString("rule: hooks-are-never-bypassed\n")
	message.WriteString("(lesson work-preservation-is-never-grounds-to-bypass-a-hook)\n\n")
	message.WriteString("A red hook is informational output about real risk, never an obstacle. Work\n")
	message.WriteString("already committed in a WB worktree survives agent and session death on its\n")
	message.WriteString("own — a push only protects against losing the machine — so \"preserve this\"\n")
	message.WriteString("is never a reason to disable the hook that is telling you something is wrong.\n\n")
	message.WriteString("Remedy: fix the failure the hook reported, or escalate instead of bypassing\n")
	message.WriteString("it. If work genuinely needs rescuing onto a branch, use:\n")
	message.WriteString("  wb worktree rescue --push\n")
	return message.String()
}

// parseGitGlobals consumes Git's own options — the ones that come before the
// subcommand — so `git -C dir -c key=value checkout` is read as a checkout in
// dir and not as an unrecognised command.
func parseGitGlobals(arguments []string, workingDirectory string) gitInvocation {
	invocation := gitInvocation{Directory: workingDirectory}
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		switch {
		case argument == "-C":
			if index+1 < len(arguments) {
				if resolved, ok := resolveAgainst(invocation.Directory, arguments[index+1]); ok {
					invocation.Directory = resolved
				} else {
					invocation.Directory = ""
				}
			}
			index += 2
		case argument == "-c":
			if index+1 < len(arguments) && isHooksPathOverride(arguments[index+1]) {
				invocation.HooksPathOverride = true
			}
			index += 2
		case strings.HasPrefix(argument, "-c") && len(argument) > 2:
			if isHooksPathOverride(argument[2:]) {
				invocation.HooksPathOverride = true
			}
			index++
		case strings.HasPrefix(argument, "--work-tree="):
			if resolved, ok := resolveAgainst(invocation.Directory, strings.TrimPrefix(argument, "--work-tree=")); ok {
				invocation.Directory = resolved
			}
			index++
		case strings.HasPrefix(argument, "--git-dir="):
			value := strings.TrimPrefix(argument, "--git-dir=")
			if resolved, ok := resolveAgainst(invocation.Directory, value); ok && filepath.Base(resolved) == ".git" {
				invocation.Directory = filepath.Dir(resolved)
			}
			index++
		case argument == "--exec-path", argument == "--namespace", argument == "--work-tree", argument == "--git-dir":
			index += 2
		case strings.HasPrefix(argument, "-"):
			index++
		default:
			invocation.Subcommand = argument
			invocation.Arguments = arguments[index+1:]
			return invocation
		}
	}
	return invocation
}

// isHooksPathOverride reports whether a `-c` value disables managed hooks.
// Git treats the section and key of a configuration name case-insensitively.
func isHooksPathOverride(setting string) bool {
	name, _, found := strings.Cut(setting, "=")
	if !found {
		name = setting
	}
	return strings.EqualFold(strings.TrimSpace(name), "core.hooksPath")
}

// gitWriteSubcommands mutate the working tree, the index, or history. Every
// one of them leaves a canonical clone in a state WB's own guard already
// refuses; refusing them here simply moves the refusal ahead of the damage.
var gitWriteSubcommands = map[string]bool{
	"add": true, "am": true, "cherry-pick": true, "commit": true,
	"mv": true, "rebase": true, "reset": true, "restore": true,
	"revert": true, "rm": true, "sparse-checkout": true,
	"update-index": true, "filter-branch": true, "citool": true,
}

// gitSubcommandWrites decides one Git subcommand, including the flag-dependent
// cases.
func gitSubcommandWrites(subcommand string, arguments []string) bool {
	if gitWriteSubcommands[subcommand] {
		return true
	}
	switch subcommand {
	case "apply":
		// `git apply --check` and the reporting flags only inspect a patch.
		return !containsAny(arguments, "--check", "--stat", "--numstat", "--summary")
	case "clean":
		return !containsShortFlag(arguments, 'n') && !containsWord(arguments, "--dry-run")
	case "stash":
		if len(arguments) > 0 && (arguments[0] == "list" || arguments[0] == "show") {
			return false
		}
		return true
	case "checkout":
		// A bare `git checkout <branch>` is left alone: it is ambiguous with
		// the recovery a stale canonical clone needs, and the post-checkout
		// hook already reports it. A pathspec checkout is the construct that
		// destroys uncommitted work, and `-b` is how a canonical clone leaves
		// its base branch.
		if containsWord(arguments, "--") {
			return true
		}
		if containsAny(arguments, "-b", "-B", "-f", "--force", "-p", "--patch", "--ours", "--theirs") {
			return true
		}
		return containsAny(arguments, ".", "*", "./")
	case "switch":
		return containsAny(arguments, "-c", "-C", "-f", "--force", "--discard-changes")
	case "merge":
		// Fetch and fast-forward is precisely what a canonical clone is for.
		// Anything that can create a commit or leave conflict markers is not.
		return !containsAny(arguments, "--ff-only", "--abort", "--continue", "--quit")
	case "pull":
		return !containsAny(arguments, "--ff-only")
	case "bisect":
		return len(arguments) > 0 && arguments[0] != "log" && arguments[0] != "view"
	}
	return false
}

// containsShortFlag reports whether a short option letter appears, including
// inside a bundle: `git clean -nd` is a dry run just as much as `git clean -n`.
func containsShortFlag(words []string, letter byte) bool {
	for _, word := range words {
		if len(word) < 2 || word[0] != '-' || word[1] == '-' {
			continue
		}
		if strings.IndexByte(word[1:], letter) >= 0 {
			return true
		}
	}
	return false
}

func containsAny(words []string, targets ...string) bool {
	for _, word := range words {
		for _, target := range targets {
			if word == target {
				return true
			}
		}
	}
	return false
}
