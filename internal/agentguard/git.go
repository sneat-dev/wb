package agentguard

import (
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
	location, ok := canonicalWorkingDirectory(invocation.Directory, projectsRoot)
	if !ok {
		return nil
	}
	// A managed-hook bypass aimed at a canonical clone has no legitimate
	// reading. This is the exact construct that let a commit land on
	// 2026-08-27 after WB's pre-commit block had already refused it, and it is
	// why a hook alone was never going to be enough.
	if invocation.HooksPathOverride || containsWord(invocation.Arguments, "--no-verify") {
		return &finding{
			Location: location,
			Detail:   "git " + invocation.Subcommand + " with the repository's managed hooks disabled",
		}
	}
	if !gitSubcommandWrites(invocation.Subcommand, invocation.Arguments) {
		return nil
	}
	return &finding{Location: location, Detail: "git " + invocation.Subcommand}
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
