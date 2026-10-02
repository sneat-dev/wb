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
// cannot read, a wrapper it does not see through (ssh, watch), or a verb that
// is not in statefulWBVerbs resolves to "no finding".

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
// so `worktree merge` covers every merge leaf.
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
	{Path: []string{"worktree", "gc"}, NeedsApply: true},
	{Path: []string{"worktree", "relocate"}, NeedsApply: true},
	{Path: []string{"branch", "cleanup"}, NeedsApply: true},
	{Path: []string{"archive", "clean"}, NeedsApply: true},
	{Path: []string{"layout", "clean"}, NeedsApply: true},
	{Path: []string{"layout", "migrate"}, NeedsApply: true},
	{Path: []string{"stream", "start"}},
	{Path: []string{"stream", "join"}},
	{Path: []string{"stream", "sync"}},
	{Path: []string{"stream", "end"}, NeedsApply: true},
	{Path: []string{"stream", "delete"}, NeedsApply: true},
}

// rootFlagsWithValue are the persistent root flags that take the next word.
var rootFlagsWithValue = setOf("--projects-root", "--filter", "--org")

// statefulWBVerb reports which state-changing verb words (a stripped wb
// command, program name first) invoke. It is a read when the invocation only
// asks for help or a dry run, or when the verb plans by default and --apply is
// absent.
func statefulWBVerb(words []string) (statefulVerb, bool) {
	var path []string
	apply, readOnly := false, false
	for index := 1; index < len(words); index++ {
		word := words[index]
		switch {
		case word == "--help" || word == "-h" || word == "--dry-run" || word == "--apply=false":
			readOnly = true
		case word == "--apply" || word == "--apply=true":
			apply = true
		case rootFlagsWithValue[word]:
			index++
		case strings.HasPrefix(word, "-"):
		case len(path) < maxVerbPathWords:
			path = append(path, word)
		}
	}
	if readOnly || len(path) == 0 {
		return statefulVerb{}, false
	}
	if path[0] == "wt" || path[0] == "worktrees" {
		path[0] = "worktree"
	}
	for _, verb := range statefulWBVerbs {
		if !hasWordPrefix(path, verb.Path) {
			continue
		}
		if verb.NeedsApply && !apply {
			return statefulVerb{}, false
		}
		return verb, true
	}
	return statefulVerb{}, false
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
// followed by a pipe, unless the same command line makes the status observable.
func inspectMaskedPipelines(command string) *finding {
	return inspectMaskedPipelinesDepth(command, false, 0)
}

func inspectMaskedPipelinesDepth(command string, pipefail bool, depth int) *finding {
	if readsPipeStatus(command) {
		return nil
	}
	segments := splitSegments(command)
	for index, current := range segments {
		words := stripCommandPrefixes(current.Words).Words
		if len(words) == 0 {
			continue
		}
		name := programName(words[0])
		if on, toggled := pipefailToggle(name, words); toggled {
			pipefail = on
			continue
		}
		if readings, ok := shellInterpreters[name]; ok && depth < maxShellUnwrapDepth {
			if payloads := shellDashCPayloads(words, readings); len(payloads) > 0 {
				inherited := pipefail || shellEnablesPipefail(words)
				for _, payload := range payloads {
					if result := inspectMaskedPipelinesDepth(payload, inherited, depth+1); result != nil {
						return result
					}
				}
				continue
			}
		}
		if pipefail || name != "wb" || !pipesOnward(segments, index) {
			continue
		}
		if verb, ok := statefulWBVerb(words); ok {
			return &finding{Message: maskedPipelineRefusal(verb)}
		}
	}
	return nil
}

// pipesOnward reports whether the output of segments[index] is piped into the
// next command, which is what makes the pipeline's status the next command's.
func pipesOnward(segments []segment, index int) bool {
	if index+1 >= len(segments) {
		return false
	}
	separator := segments[index+1].Separator
	return separator == "|" || separator == "|&"
}

// readsPipeStatus reports whether the command line reads a pipeline's per-command
// statuses (bash PIPESTATUS, zsh pipestatus), which is how a caller checks the
// verb's own status behind a pipe.
func readsPipeStatus(command string) bool {
	return strings.Contains(command, "PIPESTATUS") || strings.Contains(command, "pipestatus")
}

// pipefailToggle recognises `set -o pipefail` (any option cluster ending in o,
// as in -eo and -euo), `set +o pipefail`, and zsh's `setopt`/`unsetopt`, and
// reports the new state.
func pipefailToggle(name string, words []string) (on, toggled bool) {
	switch name {
	case "set":
		for index := 1; index+1 < len(words); index++ {
			word := words[index]
			if len(word) < 2 || (word[0] != '-' && word[0] != '+') || word[1] == '-' {
				continue
			}
			if strings.HasSuffix(word, "o") && words[index+1] == "pipefail" {
				return word[0] == '-', true
			}
		}
	case "setopt", "unsetopt":
		for _, word := range words[1:] {
			if strings.ReplaceAll(strings.ToLower(word), "_", "") == "pipefail" {
				return name == "setopt", true
			}
		}
	}
	return false, false
}

// shellEnablesPipefail reports whether a shell invocation's own options turn
// pipefail on for its -c payload (`bash -o pipefail -c …`, `bash -eo pipefail
// -c …`).
func shellEnablesPipefail(words []string) bool {
	for index := 1; index+1 < len(words); index++ {
		word := words[index]
		if strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") && strings.HasSuffix(word, "o") && words[index+1] == "pipefail" {
			return true
		}
	}
	return false
}

// maskedPipelineRefusal is the message the agent reads: the hazard in two
// lines, then the fix. The verb that has --quiet is told to use it; one that
// does not is told the two forms that make its status visible.
func maskedPipelineRefusal(verb statefulVerb) string {
	command := "wb " + strings.Join(verb.Path, " ")
	var message strings.Builder
	message.WriteString("A pipe hides the exit status of `" + command + "`: a pipeline reports only its last command's status.\n")
	message.WriteString("So a refusal from wb exits 0 here and any `&&` or `;` after it keeps running (2026-10-02: a refused create let a duplicate pull request open).\n\n")
	if verb.Quiet {
		message.WriteString("Fix: drop the pipe and add --quiet, which prints only the outcome and any refusal: " + command + " ... --quiet\n")
	} else {
		message.WriteString("Fix: drop the pipe and redirect to a file, then read it: " + command + " ... > out.txt 2>&1\n")
	}
	message.WriteString("Or run `set -o pipefail` earlier in the same command, so the verb's status survives the pipe.\n")
	return message.String()
}
