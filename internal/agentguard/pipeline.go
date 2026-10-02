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
	"--defer-direct-ci-pr", "--derived-path", "--disposition", "--effort", "--exclude",
	"--expected-candidate", "--expected-current-source", "--expected-current-target",
	"--expected-historical-refresh-source", "--expected-immutable-claim-sha256",
	"--expected-receipt-sha256", "--expected-source-sha", "--expected-supersession-sha256",
	"--expected-target", "--filter", "--format",
	"--github-dir", "--go-private", "--handover-file", "--harness", "--hold", "--include-task",
	"--initiator", "--keep-commits", "--lane-reason", "--library", "--manifest", "--match",
	"--max-waves", "--merge-method", "--message", "--mode", "--model", "--module-ref",
	"--new-worktree", "--note", "--older-than", "--on-failure", "--org", "--original-prompt-file",
	"--override-secret", "--parallel", "--peer-evidence", "--pid", "--poll-interval",
	"--prepare-timeout", "--profile", "--projects-root", "--provider", "--reason", "--rebatch-receipt", "--ref",
	"--refresh-after", "--regex", "--release-poll", "--remaining", "--repo", "--report-dir",
	"--require-host", "--residue-depth", "--retry", "--review-comment",
	"--review-comment-file", "--role", "--route", "--run", "--runtime", "--scope",
	"--session-freshness", "--sha", "--shard-attempt-timeout", "--stale", "--subject", "--successor",
	"--summary", "--superseded-by", "--target", "--task", "--task-file", "--timeout", "--title",
	"--to", "--ttl", "--undo", "--use-worktree", "--validation", "--version", "--via",
	"--wb-session-id", "--workers", "-j", "-m", "-o",
)

// ScopedFlag is a flag whose reading depends on the verb it is given to: Path
// is the command path (or a prefix of it) the reading holds for.
type ScopedFlag struct {
	Name string
	Path []string
}

// scopedValueFlags take a value on some verbs and are boolean on others
// (`wb session move --resume <session>` against `wb migrate --resume
// --apply`), so listing them in valueFlags would step over the next word of
// a verb where it is a flag of its own, which can be `--apply`.
var scopedValueFlags = []ScopedFlag{
	{Name: "--resume", Path: []string{"session", "move"}},
	{Name: "--verify", Path: []string{"migrate"}},
}

// scopedReadOnlyFlags are short or verb-specific spellings of a read-only
// request, beside the --help, -h and --dry-run every verb is read as taking.
// TestScopedReadOnlyFlagsAreBooleanFlagsOfTheirVerb in cmd/wb checks them.
var scopedReadOnlyFlags = []ScopedFlag{
	{Name: "-n", Path: []string{"sync"}},
	{Name: "--check", Path: []string{"self-update"}},
}

// MaskedPipelineValueFlag reports whether the policy knows name (a flag as
// typed, with its dashes) as taking a value on the verb at path.
func MaskedPipelineValueFlag(path []string, name string) bool {
	return valueFlags[name] || scopedFlagApplies(scopedValueFlags, path, name)
}

// MaskedPipelineScopedReadOnlyFlags lists the read-only markers that belong to
// one verb, so a test in the command package can check each one against the
// real command tree.
func MaskedPipelineScopedReadOnlyFlags() []ScopedFlag {
	return append([]ScopedFlag(nil), scopedReadOnlyFlags...)
}

func scopedFlagApplies(flags []ScopedFlag, path []string, name string) bool {
	for _, flag := range flags {
		if flag.Name == name && hasWordPrefix(verbPathAlias(path), flag.Path) {
			return true
		}
	}
	return false
}

// verbPathAlias spells the `wt` and `worktrees` aliases of worktree out.
func verbPathAlias(path []string) []string {
	if len(path) > 0 && (path[0] == "wt" || path[0] == "worktrees") {
		return append([]string{"worktree"}, path[1:]...)
	}
	return path
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
		case word == "--help" || word == "-h" || word == "--dry-run" || word == "--apply=false" ||
			scopedFlagApplies(scopedReadOnlyFlags, path, word):
			readOnly = true
		case word == "--apply" || word == "--apply=true":
			apply = true
		case word == "--format=json":
			asJSON = true
		case MaskedPipelineValueFlag(path, word):
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
	path = verbPathAlias(path)
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

// pipelineWalk is the state of a walk over one command line's simple commands:
// the groups and compound commands it is inside, the pipefail state the shell
// has at the command being read, and how many shell -c payloads deep it is.
type pipelineWalk struct {
	open     []openScope
	loops    []openCompound
	pipefail bool
	depth    int
}

// inspectMaskedPipelinesDepth walks command's simple commands in order, keeping
// the pipefail state the shell would have at each one. pipefail is the state at
// the start of command.
func inspectMaskedPipelinesDepth(command string, pipefail bool, depth int) *finding {
	walk := &pipelineWalk{pipefail: pipefail, depth: depth}
	for _, current := range splitSegments(command) {
		walk.open, walk.pipefail = syncScopes(walk.open, current.Scope, walk.pipefail)
		for _, body := range current.Substitutions {
			if result := inspectMaskedPipelinesDepth(body, walk.pipefail, depth); result != nil {
				return result
			}
		}
		if len(current.Words) == 0 {
			continue
		}
		result, settled := walk.segment(current)
		if result != nil {
			return result
		}
		// Pipefail is only ever relied on when it is certain: a command that
		// names it and is not a plain switch-on (`if x; then set -o pipefail;
		// fi`, `command set +o pipefail`, `eval 'set +o pipefail'`, `shopt -u
		// -o pipefail`, `emulate sh`) leaves it unknown, which counts as off.
		if !settled && mentionsPipefail(current.Words) {
			walk.pipefail = false
		}
	}
	return nil
}

// segment checks one command. settled is set when the command has itself set
// the pipefail state the walk goes on with: a switch, or a child shell, whose
// options never reach this one.
func (w *pipelineWalk) segment(current segment) (result *finding, settled bool) {
	if finding := trackCompound(&w.loops, current, w.pipefail); finding != nil {
		return finding, false
	}
	if on, toggled := pipefailSetting(programName(current.Words[0]), current.Words); toggled {
		// Only a certain switch-on counts. A conditional `set +o pipefail`
		// may or may not have run, so pipefail is not relied on afterwards
		// either way.
		w.pipefail = on && segmentRunsUnconditionally(current, len(w.loops) > 0)
		return nil, true
	}
	words := stripCommandPrefixes(current.Words).Words
	if len(words) == 0 {
		return nil, false
	}
	name := programName(words[0])
	if readings, ok := shellInterpreters[name]; ok && w.depth < maxShellUnwrapDepth {
		if payloads := shellDashCPayloads(words, readings); len(payloads) > 0 {
			// The payload runs in a child shell, so a pipe after the command
			// hides the status of whatever it runs last.
			if verb, found := firstWatchedVerb(payloads, w.depth+1); found {
				if statusIsMasked(current, w.open, w.pipefail) {
					return &finding{Message: maskedPipelineRefusal(verb)}, false
				}
				markCompounds(w.loops, verb)
			}
			// A child shell does not inherit the parent's pipefail: its
			// payload starts with it off unless the shell's own options or
			// the payload's own body turn it on.
			own := shellOwnPipefail(words)
			for _, payload := range payloads {
				if result := inspectMaskedPipelinesDepth(payload, own, w.depth+1); result != nil {
					return result, false
				}
			}
			return nil, true
		}
	}
	if name != "wb" {
		return nil, false
	}
	if verb, ok := statefulWBVerb(words); ok {
		if statusIsMasked(current, w.open, w.pipefail) {
			return &finding{Message: maskedPipelineRefusal(verb)}, false
		}
		markCompounds(w.loops, verb)
	}
	return nil, false
}

// mentionsPipefail reports whether any word names the pipefail option in any
// spelling bash or zsh accept (pipefail, PIPE_FAIL, NO_PIPE_FAIL), or is
// `emulate`, which resets every option.
func mentionsPipefail(words []string) bool {
	for _, word := range words {
		name := strings.ReplaceAll(strings.ToLower(word), "_", "")
		if strings.Contains(name, "pipefail") || name == "emulate" {
			return true
		}
	}
	return false
}

// openCompound is a for, while, until, select, if or case compound command the
// walk is inside, with the pipefail state it was opened in and the first
// watched verb seen in it.
type openCompound struct {
	before  bool
	watched *verbMatch
}

var (
	compoundOpeners = setOf("for", "select", "while", "until", "if", "case")
	compoundClosers = setOf("done", "fi", "esac")
	// compoundLeaders may stand in front of an opener on the same command.
	compoundLeaders = setOf("then", "do", "else", "elif", "!", "time")
)

// trackCompound keeps the compound commands the walk is inside. A pipe after
// `done`, `fi` or `esac` pipes the whole compound command, so, like a piped
// group, it hides the status of the watched verb inside it unless pipefail was
// on where the compound command began.
func trackCompound(loops *[]openCompound, current segment, pipefail bool) *finding {
	words := current.Words
	for len(words) > 1 && compoundLeaders[words[0]] {
		words = words[1:]
	}
	switch {
	case compoundOpeners[words[0]]:
		*loops = append(*loops, openCompound{before: pipefail})
	case compoundClosers[words[0]] && len(*loops) > 0:
		last := (*loops)[len(*loops)-1]
		*loops = (*loops)[:len(*loops)-1]
		if current.Piped && !last.before && last.watched != nil {
			return &finding{Message: maskedPipelineRefusal(*last.watched)}
		}
	}
	return nil
}

// markCompounds records a watched verb in every compound command it is inside.
func markCompounds(loops []openCompound, verb verbMatch) {
	for index := range loops {
		if loops[index].watched == nil {
			loops[index].watched = &verb
		}
	}
}

// firstWatchedVerb returns the first watched verb that any of the command
// lines would run, looking through shell -c payloads and the bodies of
// double-quoted substitutions. Whether a pipe hides its status is the
// caller's question.
func firstWatchedVerb(commands []string, depth int) (verbMatch, bool) {
	if depth > maxShellUnwrapDepth {
		return verbMatch{}, false
	}
	for _, command := range commands {
		for _, current := range splitSegments(command) {
			if verb, found := firstWatchedVerb(current.Substitutions, depth+1); found {
				return verb, true
			}
			words := stripCommandPrefixes(current.Words).Words
			if len(words) == 0 {
				continue
			}
			name := programName(words[0])
			if name == "wb" {
				if verb, ok := statefulWBVerb(words); ok {
					return verb, true
				}
			}
			if readings, ok := shellInterpreters[name]; ok {
				if verb, found := firstWatchedVerb(shellDashCPayloads(words, readings), depth+1); found {
					return verb, true
				}
			}
		}
	}
	return verbMatch{}, false
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
// shell that goes on to read the next command: it does not follow && or || (on
// the same line or the one before), is not backgrounded, is not part of a
// pipeline (each side of one is a child shell), is not in a compound command's
// body (then, else, do, a case arm), and is not in a group that is
// conditional, piped, backgrounded or a function body.
func segmentRunsUnconditionally(current segment, inCompound bool) bool {
	switch current.Separator {
	case "&&", "||", "|", "|&":
		return false
	}
	if current.Piped || current.Behind || current.Background || inCompound {
		return false
	}
	for _, frame := range current.Scope {
		if frame.Conditional || frame.Piped || frame.Background {
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
// positional parameters, not options. bash is case sensitive and has no
// underscores in the name, so `set` reads only the exact name `pipefail`; any
// other spelling of it (`set -o PIPEFAIL`) is not a setting, and the caller
// treats the command as one that names pipefail without setting it.
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
				if isPipefail, _ := pipefailName(words[index]); !isPipefail {
					continue
				}
				if words[index] != "pipefail" {
					return false, false
				}
				on, toggled = word[0] == '-', true
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
