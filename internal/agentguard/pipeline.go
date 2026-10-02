package agentguard

import (
	"strings"
)

// This file holds the masked-exit-status policy
// (spec/features/quiet-verbs-and-masked-status-guard).
//
// A pipeline reports the exit status of its LAST command. On 2026-10-02 a
// coordinator ran `wb worktree create … 2>&1 | tail -1 && cd <worktree> && …
// && wb pr create`: `wb worktree create` refused, the pipe to `tail` made the
// pipeline exit 0, the && chain went on, and a duplicate pull request was
// opened (sneat-dev/wb#813). The agent piped because lifecycle verbs print
// progress and it wanted only the outcome, so the verbs now have `--quiet`
// and this policy refuses the pipe.
//
// Like every other policy in this package it reads commands through the small
// shell reader in shell.go, models no expansion, and fails open: a command it
// cannot read, a wrapper it does not see through (eval, script, unbuffer, $WB,
// a shell function, env -S, go run ./cmd/wb, a renamed binary, ssh, watch), or
// a verb that is not in statefulWBVerbs resolves to "no finding". The one place
// it leans the other way is pipefail: when it cannot tell that pipefail is in
// effect where the pipeline runs, it refuses.
//
// There is no escape other than making the status observable the supported
// way: --quiet instead of the pipe, or pipefail in the shell that runs the
// pipeline. Reading PIPESTATUS afterwards is not accepted, because a `&&` chain
// has already run on by the time anything reads it.

// maxVerbPathWords bounds how many leading command words a statefulWBVerbs
// path may name (`worktree merge prepare` is the longest in use).
const maxVerbPathWords = 3

// statefulVerb names one wb command whose refusal or failure gates a follow-up
// step, so its exit status must stay visible.
type statefulVerb struct {
	// Path is the command path, with the `worktree` spelling for its aliases.
	Path []string
	// NeedsApply marks a verb that plans by default and only changes state
	// with --apply, so a plain run piped to tail is a read.
	NeedsApply bool
	// Quiet marks a verb that accepts --quiet, which is the fix the refusal
	// names; a verb without it is told to redirect or enable pipefail instead.
	Quiet bool
}

// statefulWBVerbs is the one table the policy reads. A path matches by prefix,
// so `worktree merge` covers every merge leaf. TestEveryStatefulWBVerbIsAWBCommand
// in cmd/wb checks every path against the real command tree, and a verb that
// has no --apply and no dry-run default is listed without NeedsApply because
// every plain run of it changes state.
//
// Not listed on purpose: gating read verbs (`wb run`, `wb ci wait`,
// `wb check`). Their exit status gates a follow-up step too, but they change
// nothing, so a masked one costs a wrong decision rather than a wrong mutation
// and the policy leaves them maskable.
var statefulWBVerbs = []statefulVerb{
	{Path: []string{"create"}, Quiet: true},
	{Path: []string{"land"}, Quiet: true},
	{Path: []string{"worktree", "create"}, Quiet: true},
	{Path: []string{"worktree", "land"}, Quiet: true},
	{Path: []string{"worktree", "merge"}, Quiet: true},
	{Path: []string{"worktree", "cleanup"}, Quiet: true, NeedsApply: true},
	{Path: []string{"pr", "create"}, Quiet: true},
	{Path: []string{"pr", "land"}, Quiet: true},
	{Path: []string{"pr", "update"}},
	{Path: []string{"worktree", "end"}},
	{Path: []string{"worktree", "abort"}, NeedsApply: true},
	{Path: []string{"worktree", "adopt"}, NeedsApply: true},
	{Path: []string{"worktree", "gc"}, NeedsApply: true},
	{Path: []string{"worktree", "relocate"}, NeedsApply: true},
	{Path: []string{"branch", "cleanup"}, NeedsApply: true},
	{Path: []string{"branch", "quarantine"}, NeedsApply: true},
	{Path: []string{"archive", "clean"}, NeedsApply: true},
	{Path: []string{"layout", "clean"}, NeedsApply: true},
	{Path: []string{"layout", "migrate"}, NeedsApply: true},
	{Path: []string{"stream", "start"}},
	{Path: []string{"stream", "join"}},
	{Path: []string{"stream", "sync"}},
	{Path: []string{"stream", "end"}, NeedsApply: true},
	{Path: []string{"stream", "delete"}, NeedsApply: true},
	{Path: []string{"remote", "claim"}},
	{Path: []string{"remote", "release"}},
	{Path: []string{"session", "park"}},
	{Path: []string{"session", "move"}},
	{Path: []string{"agent", "dispatch"}},
	{Path: []string{"deps", "bump"}},
	{Path: []string{"migrate"}, NeedsApply: true},
	{Path: []string{"repo", "init-remote"}},
	{Path: []string{"fleet", "merge-policy"}, NeedsApply: true},
	{Path: []string{"sync"}},
	{Path: []string{"hooks", "install"}},
	{Path: []string{"hooks", "repair"}},
	{Path: []string{"self-update"}},
}

// MaskedPipelineVerbPaths lists the command paths of the state-changing verbs
// the policy watches, so a test in the command package can check each one
// against the real command tree.
func MaskedPipelineVerbPaths() [][]string {
	paths := make([][]string, len(statefulWBVerbs))
	for index, verb := range statefulWBVerbs {
		paths[index] = verb.Path
	}
	return paths
}

// valueFlags are the wb flags that take the next word as their value, which
// the policy has to step over: `--title --dry-run` names a pull request title,
// it does not ask for a dry run, and a path word inside a value is not a verb.
// TestEveryValueTakingFlagOfAWatchedVerbIsKnownToTheMaskedPipelinePolicy in
// cmd/wb walks the real command tree and fails when a watched verb has a
// value-taking flag that is missing here.
var valueFlags = setOf(
	"--absorbed-by", "--actor", "--add", "--agent", "--agent-id", "--agent-runtime", "--approved-by",
	"--base", "--body", "--body-file", "--branch", "--branch-prefix", "--changed", "--check-interval",
	"--check-timeout", "--checks", "--claim", "--closed-pr", "--cli", "--closes", "--config", "--context-file",
	"--defer-direct-ci-pr", "--disposition", "--effort", "--exclude", "--filter", "--format",
	"--github-dir", "--go-private", "--handover-file", "--harness", "--hold", "--include-task",
	"--initiator", "--keep-commits", "--lane-reason", "--library", "--manifest", "--match",
	"--max-waves", "--merge-method", "--message", "--mode", "--model", "--module-ref",
	"--new-worktree", "--note", "--older-than", "--on-failure", "--org", "--original-prompt-file",
	"--override-secret", "--parallel", "--peer-evidence", "--pid", "--poll-interval",
	"--prepare-timeout", "--profile", "--projects-root", "--provider", "--reason", "--ref",
	"--refresh-after", "--regex", "--release-poll", "--remaining", "--repo", "--report-dir",
	"--require-host", "--residue-depth", "--resume", "--retry", "--review-comment",
	"--review-comment-file", "--role", "--route", "--run", "--runtime", "--scope",
	"--session-freshness", "--sha", "--shard-attempt-timeout", "--stale", "--subject", "--successor",
	"--summary", "--superseded-by", "--target", "--task", "--task-file", "--timeout", "--title",
	"--to", "--ttl", "--undo", "--use-worktree", "--validation", "--verify", "--version", "--via",
	"--wb-session-id", "--workers", "-j", "-m", "-o",
)

// MaskedPipelineValueFlag reports whether the policy knows name (a flag as
// typed, with its dashes) as taking a value.
func MaskedPipelineValueFlag(name string) bool {
	return valueFlags[name]
}

// verbMatch is a recognised state-changing invocation.
type verbMatch struct {
	statefulVerb
	// JSON is set when the invocation asks for --format json, so the piped
	// consumer is most likely a parser (`| jq`) and --quiet is no help.
	JSON bool
}

// statefulWBVerb reports which state-changing verb words (a stripped wb
// command, program name first) invoke. It is a read when the invocation only
// asks for help or a dry run, or when the verb plans by default and --apply is
// absent. A marker such as --help counts only as an argument of its own: the
// word after a flag that takes a value is that flag's value.
func statefulWBVerb(words []string) (verbMatch, bool) {
	var path []string
	apply, readOnly, asJSON := false, false, false
	for index := 1; index < len(words); index++ {
		word := words[index]
		switch {
		case word == "--help" || word == "-h" || word == "--dry-run" || word == "--apply=false":
			readOnly = true
		case word == "--apply" || word == "--apply=true":
			apply = true
		case word == "--format=json":
			asJSON = true
		case valueFlags[word]:
			if word == "--format" && index+1 < len(words) && words[index+1] == "json" {
				asJSON = true
			}
			index++
		case strings.HasPrefix(word, "-"):
		case len(path) < maxVerbPathWords:
			path = append(path, word)
		}
	}
	if readOnly || len(path) == 0 {
		return verbMatch{}, false
	}
	if path[0] == "wt" || path[0] == "worktrees" {
		path[0] = "worktree"
	}
	for _, verb := range statefulWBVerbs {
		if !hasWordPrefix(path, verb.Path) {
			continue
		}
		if verb.NeedsApply && !apply {
			return verbMatch{}, false
		}
		return verbMatch{statefulVerb: verb, JSON: asJSON}, true
	}
	return verbMatch{}, false
}

func hasWordPrefix(words, prefix []string) bool {
	if len(words) < len(prefix) {
		return false
	}
	for index, want := range prefix {
		if words[index] != want {
			return false
		}
	}
	return true
}

// inspectMaskedPipelines refuses a command in which a state-changing wb verb is
// followed by a pipe, unless pipefail is in effect where the pipeline runs.
func inspectMaskedPipelines(command string) *finding {
	return inspectMaskedPipelinesDepth(command, false, 0)
}

// openScope is a group the walk is inside, with the pipefail state it was
// opened in.
type openScope struct {
	frame  *scopeFrame
	before bool
}

// inspectMaskedPipelinesDepth walks command's simple commands in order, keeping
// the pipefail state the shell would have at each one. pipefail is the state at
// the start of command.
func inspectMaskedPipelinesDepth(command string, pipefail bool, depth int) *finding {
	var open []openScope
	for _, current := range splitSegments(command) {
		open, pipefail = syncScopes(open, current.Scope, pipefail)
		for _, body := range current.Substitutions {
			if result := inspectMaskedPipelinesDepth(body, pipefail, depth); result != nil {
				return result
			}
		}
		if len(current.Words) == 0 {
			continue
		}
		if on, toggled := pipefailSetting(programName(current.Words[0]), current.Words); toggled {
			if segmentRunsUnconditionally(current) {
				pipefail = on
			} else if !on {
				// Whether a conditional `set +o pipefail` ran is unknown, so
				// pipefail is not relied on afterwards.
				pipefail = false
			}
			continue
		}
		words := stripCommandPrefixes(current.Words).Words
		if len(words) == 0 {
			continue
		}
		name := programName(words[0])
		if readings, ok := shellInterpreters[name]; ok && depth < maxShellUnwrapDepth {
			if payloads := shellDashCPayloads(words, readings); len(payloads) > 0 {
				// A child shell does not inherit the parent's pipefail: its
				// payload starts with it off unless the shell's own options or
				// the payload's own body turn it on.
				own := shellOwnPipefail(words)
				for _, payload := range payloads {
					if result := inspectMaskedPipelinesDepth(payload, own, depth+1); result != nil {
						return result
					}
				}
				continue
			}
		}
		if name != "wb" || !statusIsMasked(current, open, pipefail) {
			continue
		}
		if verb, ok := statefulWBVerb(words); ok {
			return &finding{Message: maskedPipelineRefusal(verb)}
		}
	}
	return nil
}

// syncScopes brings the walk's open groups in line with the groups segment
// scope says the next command is in. Leaving a subshell (or a substitution)
// discards the options set inside it, so the pipefail state from before it
// opened comes back; leaving a brace group keeps them.
func syncScopes(open []openScope, scope []*scopeFrame, pipefail bool) ([]openScope, bool) {
	keep := 0
	for keep < len(open) && keep < len(scope) && open[keep].frame == scope[keep] {
		keep++
	}
	for index := len(open) - 1; index >= keep; index-- {
		if open[index].frame.Subshell {
			pipefail = open[index].before
		}
	}
	open = open[:keep]
	for _, frame := range scope[keep:] {
		open = append(open, openScope{frame: frame, before: pipefail})
	}
	return open, pipefail
}

// segmentRunsUnconditionally reports whether a command is certain to run in the
// shell that goes on to read the next command: it does not follow && or ||, is
// not part of a pipeline (each side of one is a child shell), and is not in a
// group that is conditional or piped.
func segmentRunsUnconditionally(current segment) bool {
	switch current.Separator {
	case "&&", "||", "|", "|&":
		return false
	}
	if current.Piped {
		return false
	}
	for _, frame := range current.Scope {
		if frame.Conditional || frame.Piped {
			return false
		}
	}
	return true
}

// statusIsMasked reports whether current's output goes through a pipe that
// hides its exit status: a pipe right after it, or one after a group around it,
// without pipefail in the shell that runs that pipeline. A group's own pipeline
// runs in the shell that opened it, so the state it was opened in decides.
func statusIsMasked(current segment, open []openScope, pipefail bool) bool {
	if current.Piped && !pipefail {
		return true
	}
	for _, scope := range open {
		if scope.frame.Piped && !scope.before {
			return true
		}
	}
	return false
}

// pipefailName reads one option name the way bash and zsh do: zsh ignores case
// and underscores and spells the opposite with a `no` prefix.
func pipefailName(word string) (isPipefail, negated bool) {
	name := strings.ReplaceAll(strings.ToLower(word), "_", "")
	return name == "pipefail" || name == "nopipefail", name == "nopipefail"
}

// pipefailSetting reads `set` and zsh's `setopt`/`unsetopt` and reports the
// state they leave pipefail in. Options are read in order and the last one that
// names pipefail wins (`set -o pipefail +o pipefail` is off). A negated spelling
// (`nopipefail`, `NO_PIPE_FAIL`) is off however it is switched, which is the
// safe reading. Words after `--` or after the first non-option word are
// positional parameters, not options.
func pipefailSetting(name string, words []string) (on, toggled bool) {
	switch name {
	case "set":
		for index := 1; index < len(words); index++ {
			word := words[index]
			if len(word) < 2 || (word[0] != '-' && word[0] != '+') || word[1] == '-' {
				break
			}
			for _, letter := range word[1:] {
				if letter != 'o' || index+1 >= len(words) {
					continue
				}
				index++
				if isPipefail, negated := pipefailName(words[index]); isPipefail {
					on, toggled = word[0] == '-' && !negated, true
				}
			}
		}
	case "setopt", "unsetopt":
		for _, word := range words[1:] {
			if isPipefail, negated := pipefailName(word); isPipefail {
				on, toggled = name == "setopt" && !negated, true
			}
		}
	}
	return on, toggled
}

// shellOwnPipefail reports whether the options of a shell invocation turn
// pipefail on for its -c payload (`bash -o pipefail -c …`, `bash -eo pipefail
// -c …`). Option words end at the first word that is not one.
func shellOwnPipefail(words []string) bool {
	on := false
	for index := 1; index < len(words); index++ {
		word := words[index]
		if len(word) < 2 || (word[0] != '-' && word[0] != '+') || word[1] == '-' {
			break
		}
		for _, letter := range word[1:] {
			if letter != 'o' || index+1 >= len(words) {
				continue
			}
			index++
			if isPipefail, negated := pipefailName(words[index]); isPipefail {
				on = word[0] == '-' && !negated
			}
		}
	}
	return on
}

// maskedPipelineRefusal is the message the agent reads: the hazard in two
// lines, then the fix. The verb that has --quiet is told to use it. A command
// that asks for --format json is feeding a parser, which --quiet does not
// help, so it is told to capture and then parse. Any other verb is told to
// redirect to a file.
func maskedPipelineRefusal(verb verbMatch) string {
	command := "wb " + strings.Join(verb.Path, " ")
	var message strings.Builder
	message.WriteString("A pipe hides the exit status of `" + command + "`: a pipeline reports only its last command's status.\n")
	message.WriteString("So a refusal from wb exits 0 here and any `&&` or `;` after it keeps running (2026-10-02: a refused create let a duplicate pull request open).\n\n")
	switch {
	case verb.JSON:
		message.WriteString("Fix: capture the document first, then parse it, so the verb's status gates the parse: x=$(" + command + " ... --format json) && jq ... <<<\"$x\"\n")
	case verb.Quiet:
		message.WriteString("Fix: drop the pipe and add --quiet, which prints only the outcome and any refusal: " + command + " ... --quiet\n")
	default:
		message.WriteString("Fix: drop the pipe and redirect to a file, then read it: " + command + " ... > out.txt 2>&1\n")
	}
	message.WriteString("Or run `set -o pipefail` earlier in the same shell (not in a `(...)`, not behind `&&`), so the verb's status survives the pipe.\n")
	return message.String()
}
