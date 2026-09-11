package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// ghPrMergeOverrideEnv is the escape hatch for the gh pr merge refusal below.
// A non-empty value is the operator's recorded reason for bypassing
// `wb worktree land` / `wb pr land` for this one call.
//
// It must be read as an inline `WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>"`
// prefix on the exact same Bash call being rerun — directly, or through
// `env` — never from the hook process's own ambient environment. The
// PreToolUse hook is spawned by the harness for every tool call, in a
// process tree separate from whatever shell ran an `export`, so an ambient
// read cannot be scoped to "the one call it is set on" the way the refusal
// text promises: the only environment a human or agent can reliably put a
// value into per call is the words of that call itself (wb#500 review,
// Should-fix 3). leadingAssignmentValue in bash.go extracts it from there and
// passes it in as override.
//
// This mirrors --allow-saturated-host and --take-over-lane elsewhere in WB:
// an override is never silent, it is always named and recorded, so a fleet
// audit can tell "the guard was never reached" from "the guard was
// deliberately overridden, and why".
const ghPrMergeOverrideEnv = "WB_AGENTGUARD_ALLOW_GH_PR_MERGE"

// inspectGh judges one `gh ...` invocation. Only a call that gh resolves to
// `pr merge` is judged (see isGhPrMerge). Every other gh command — `pr view`,
// `pr checks`, `pr list`, `help pr merge`, `search issues pr merge`, and so
// on — is always allowed by this policy.
//
// This is deliberately NOT gated to a canonical clone or a managed worktree.
// Three merger lanes reimplemented landing step by step with `gh pr merge`,
// `git push --delete`, and hand-rolled ancestry checks instead of the
// `wb worktree land` / `wb pr land` verbs those lanes already had a contract
// to use (rule:land-with-wb-verb, sneat-co/backstage). That failure mode has
// nothing to do with which directory the agent happened to be sitting in, so
// neither does the refusal.
func inspectGh(words []string, projectsRoot string, override string) *finding {
	expanded, merges := ghResolvesToPrMerge(words)
	if !merges {
		return nil
	}
	if ghRequestsHelp(expanded) {
		return nil
	}
	if reason := strings.TrimSpace(override); reason != "" {
		recordGhPrMergeOverride(projectsRoot, words, reason)
		return nil
	}
	return &finding{Message: ghPrMergeRefusal(words)}
}

// ghPrMergeLongValueFlags and ghPrMergeShortValueFlags name gh pr merge's own
// flags, plus the inherited --repo/-R, that take a value; ghPrMergeShortBoolFlags
// names its one-letter boolean flags (-d --delete-branch, -m --merge, -r
// --rebase, -s --squash). All three are sourced from `gh pr merge --help`'s
// FLAGS and INHERITED FLAGS sections (gh 2.100.0). -h is help.
var ghPrMergeLongValueFlags = map[string]bool{
	"--repo": true, "--subject": true, "--body": true, "--body-file": true,
	"--match-head-commit": true, "--author-email": true,
}

const (
	ghPrMergeShortValueFlags = "RtbFA"
	ghPrMergeShortBoolFlags  = "dmrs"
)

// ghRequestsHelp reports whether words is genuinely asking `gh pr merge` for
// its help text. It walks the words the way gh's flag parser (pflag) does,
// because a help-looking word is not always a help request:
//
//   - `--help`, or `-h` alone or inside a cluster of boolean short flags
//     (`-sh`), prints help and merges nothing.
//   - A value-taking flag with no attached value consumes the very next word
//     whatever it looks like. `--subject --help`, `-t -h` and the cluster
//     `-st --help` (-s is boolean, -t takes the next word) all merge, with the
//     literal subject "--help" (wb#500 review, Blocker 1).
//   - `--` ends flag parsing, so a `--help` after it is a positional argument.
//     `gh pr merge -- --help` really tries to merge a pull request selected
//     by "--help" (confirmed against gh 2.100.0).
func ghRequestsHelp(words []string) bool {
	for index := 1; index < len(words); index++ {
		word := words[index]
		switch {
		case word == "--":
			return false
		case word == "--help":
			return true
		case strings.HasPrefix(word, "--"):
			if !strings.Contains(word, "=") && ghPrMergeLongValueFlags[word] {
				index++
			}
		case strings.HasPrefix(word, "-") && len(word) > 1:
			help, consumesNext := ghShortFlagCluster(word[1:])
			if help {
				return true
			}
			if consumesNext {
				index++
			}
		}
	}
	return false
}

// ghShortFlagCluster reads one short-flag cluster (the letters after "-")
// the way pflag does: boolean letters in turn, until a help letter (help), a
// value-taking letter (which takes the rest of the cluster as its value, or
// the next word when it is the last letter), or an unknown letter (pflag
// rejects the whole call there, so nothing after it matters).
func ghShortFlagCluster(letters string) (help, consumesNext bool) {
	for position, letter := range letters {
		switch {
		case letter == 'h':
			return true, false
		case strings.ContainsRune(ghPrMergeShortValueFlags, letter):
			return false, position == len(letters)-1
		case !strings.ContainsRune(ghPrMergeShortBoolFlags, letter):
			return false, false
		}
	}
	return false, false
}

// ghRootNoValueFlags and ghPrNoValueFlags are the only flags gh's command
// lookup knows at the `gh` and `gh pr` levels that take no value (`gh --help`
// and `gh pr --help`, gh 2.100.0). See isGhPrMerge for why that is all it
// needs to know.
var (
	ghRootNoValueFlags = map[string]bool{"--help": true, "--version": true}
	ghPrNoValueFlags   = map[string]bool{"--help": true}
)

// isGhPrMerge reports whether gh would run `pr merge` for words.
//
// It mirrors how gh picks a subcommand before it parses a single flag:
// cobra's Command.Find. At each level, first `gh` and then `gh pr`, Find
// strips the flags from the arguments and takes the first word left as the
// subcommand name. Find knows only the flags defined at that level, and it
// assumes every flag it does NOT know takes the next word as its value,
// unless the value is attached with "=" or, for a short flag, the word is
// longer than two characters (`-Ro/r`). `--` ends the lookup. For the second
// level, Find re-reads the same arguments with the "pr" word removed, using
// gh pr's own flags (wb#500 final review, S1).
//
// Confirmed against gh 2.100.0 with a nonexistent repository:
//
//   - `gh pr -R o/r merge 1`, `gh pr --repo o/r merge 1 --squash` and
//     `gh -R o/r pr merge 1` all reach the merge.
//   - So do `gh --squash 1 pr merge`, `gh pr --squash 1 merge` and
//     `gh pr -s 1 merge`. Find reads the unknown --squash/-s as taking "1",
//     then gh pr merge's own parser reads --squash/-s as the boolean it is and
//     "1" as the pull request. That is why "skip flags, only -R/--repo takes
//     a value" would miss them.
//   - `gh help pr merge` prints help, and `gh search issues pr merge` searches.
//   - `gh -h pr merge 1` fails with unknown command "merge": gh defines no -h
//     at the root, so Find reads it as taking "pr". `gh -- pr merge 1` and
//     `gh pr -- merge 1` fail with unknown command too.
func isGhPrMerge(words []string) bool {
	arguments := words[1:]
	first := cobraSubcommandIndex(arguments, ghRootNoValueFlags)
	if first < 0 || arguments[first] != "pr" {
		return false
	}
	rest := make([]string, 0, len(arguments)-1)
	rest = append(rest, arguments[:first]...)
	rest = append(rest, arguments[first+1:]...)
	second := cobraSubcommandIndex(rest, ghPrNoValueFlags)
	return second >= 0 && rest[second] == "merge"
}

// ghResolvesToPrMerge is isGhPrMerge over every command line the shell's
// brace expansion can make of words. `gh pr {merge,} 1` reaches gh as
// `gh pr merge 1` on bash, sh and zsh alike (wb#500 fifth review, S2), so a
// word carrying a brace list is read as each of its alternatives. It returns
// the first expansion that merges, so the help check can read the flags of
// the line gh would actually see.
func ghResolvesToPrMerge(words []string) ([]string, bool) {
	for _, candidate := range braceExpandWords(words, maxBraceExpansions) {
		if isGhPrMerge(candidate) {
			return candidate, true
		}
	}
	return words, false
}

// maxBraceExpansions bounds how many command lines braceExpandWords produces.
// A real call has one list with two or three alternatives; the bound only
// guarantees termination against a pathological line, and hitting it fails
// open like every other construct the reader cannot model.
const maxBraceExpansions = 64

// braceExpandWords returns every word list the shell's brace expansion makes
// of words, in order, at most limit of them. A word without a brace list
// stands for itself.
func braceExpandWords(words []string, limit int) [][]string {
	results := [][]string{{}}
	for _, word := range words {
		alternatives := braceExpansions(word, limit)
		var next [][]string
		for _, prefix := range results {
			for _, alternative := range alternatives {
				if len(next) >= limit {
					return next
				}
				line := make([]string, 0, len(prefix)+1)
				line = append(line, prefix...)
				line = append(line, alternative)
				next = append(next, line)
			}
		}
		results = next
	}
	return results
}

// braceExpansions expands the first {a,b,...} list in word and recurses into
// each result, the way the shell does: `m{erge,}` is "merge" and "m",
// `{a,b}{c,d}` is ac, ad, bc, bd. A brace pair without a top-level comma
// (`${X:-merge}`, `{}`) is not a list and is left as it is; the shell reads
// it as a parameter expansion or literally, and neither is modelled here.
func braceExpansions(word string, limit int) []string {
	open, close, commas := firstBraceList(word)
	if open < 0 {
		return []string{word}
	}
	prefix, suffix := word[:open], word[close+1:]
	inner := word[open+1 : close]
	var results []string
	start := 0
	for _, comma := range append(commas, len(inner)) {
		for _, expanded := range braceExpansions(prefix+inner[start:comma]+suffix, limit) {
			if len(results) >= limit {
				return results
			}
			results = append(results, expanded)
		}
		start = comma + 1
	}
	return results
}

// firstBraceList finds the first { ... } in word that holds at least one
// comma at its own nesting depth, and reports the indexes of the braces and
// of those commas, relative to the inner text. It returns open = -1 when
// there is none.
func firstBraceList(word string) (open, close int, commas []int) {
	for start := 0; start < len(word); start++ {
		if word[start] != '{' || (start > 0 && word[start-1] == '$') {
			continue
		}
		depth := 0
		var found []int
		for index := start; index < len(word); index++ {
			switch word[index] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if len(found) > 0 {
						return start, index, found
					}
					index = len(word)
				}
			case ',':
				if depth == 1 {
					found = append(found, index-start-1)
				}
			}
		}
	}
	return -1, -1, nil
}

// cobraSubcommandIndex mirrors cobra's stripFlags. It returns the index in
// arguments of the first word cobra's Command.Find would take as a
// subcommand name, or -1 when there is none. noValueFlags are the flags
// defined at this level that take no value. Every other flag is assumed to
// take the next word, exactly as cobra assumes. It serves every cobra-based
// CLI this guard reads: gh here, and wb for `wb run --` (wbRunCommand).
func cobraSubcommandIndex(arguments []string, noValueFlags map[string]bool) int {
	for index := 0; index < len(arguments); index++ {
		word := arguments[index]
		switch {
		case word == "--":
			return -1
		case strings.HasPrefix(word, "--"):
			if !strings.Contains(word, "=") && !noValueFlags[word] {
				index++
			}
		case strings.HasPrefix(word, "-"):
			if len(word) == 2 && !strings.Contains(word, "=") && !noValueFlags[word] {
				index++
			}
		case word != "":
			return index
		}
	}
	return -1
}

// ghPrMergeRefusal writes the message the agent reads. It names both landing
// verbs — worktree land covers one task's worktrees, possibly spanning
// several repositories in one call; pr land covers an already-open pull
// request with no local worktree — plus the escape hatch, since a refusal
// this guard cannot itself judge (a pull request WB never created a worktree
// for) must still have a way through.
func ghPrMergeRefusal(words []string) string {
	var message strings.Builder
	message.WriteString("gh pr merge lands outside WB's landing verbs.\n\n")
	message.WriteString("rule: land-with-wb-verb (sneat-co/backstage)\n\n")
	fmt.Fprintf(&message, "Refused: %s\n\n", strings.Join(words, " "))
	message.WriteString("Land the work through WB instead:\n")
	message.WriteString("  wb worktree land <worktree>...   # one call per repository (several worktrees of THAT repository)\n")
	message.WriteString("  wb pr land <owner/repo#n>         # an already-open pull request with no local worktree\n\n")
	message.WriteString("A task spanning several repositories still lands with one wb worktree land/wb\n")
	message.WriteString("land call per repository, not one call for the whole task.\n\n")
	message.WriteString("If a person asked for this manual landing, treat the instruction as possibly\n")
	message.WriteString("accidental rather than as an order to bypass the verb: say once, in one\n")
	message.WriteString("sentence, that wb worktree land <worktree> (or wb pr land <owner/repo#n>) does\n")
	message.WriteString("the same with check-waiting, a remote receipt and cleanup, and ask whether to\n")
	message.WriteString("use it instead. Proceed manually only if they confirm after that offer —\n")
	message.WriteString("record the confirmation in the override reason and in the report. Never\n")
	message.WriteString("challenge the same instruction twice.\n\n")
	message.WriteString("If the verb genuinely refuses this landing, report the exact refusal and file\n")
	message.WriteString("a sneat-dev/wb issue instead of hand-rolling gh/git steps.\n\n")
	fmt.Fprintf(&message, "Escape hatch: prefix this exact call with %s=\"<reason>\"\n", ghPrMergeOverrideEnv)
	message.WriteString("(directly, or via `env`) and rerun it to bypass this refusal once; the guard\n")
	message.WriteString("records the reason when it allows the call. Setting the variable ahead of\n")
	message.WriteString("time in the shell or session that launched this agent does nothing — only a\n")
	message.WriteString("value on the words of this exact call is ever read.\n")
	return message.String()
}

// ghPrMergeOverride is one recorded use of the escape hatch above.
type ghPrMergeOverride struct {
	Policy     string `json:"policy"`
	Command    string `json:"command"`
	Reason     string `json:"reason"`
	RecordedAt string `json:"recorded_at"`
}

// recordGhPrMergeOverride appends one audited line when the guard allows a
// call because of the override. The line records the guard's decision, not
// that the call ran: the hook runs before the call, and the call can still be
// declined at the permission prompt or fail (wb#500 final review, Nit 3).
// It is best-effort and never blocks the call it is recording: a guard that
// can fail closed because its OWN bookkeeping failed would be worse than the
// defect it exists to catch (see checkout.go's package doc, "fail open,
// without exception").
func recordGhPrMergeOverride(projectsRoot string, words []string, reason string) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return
	}
	directory := filepath.Join(home, "agentguard")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return
	}
	encoded, err := json.Marshal(ghPrMergeOverride{
		Policy:     "land-with-wb-verb",
		Command:    strings.Join(words, " "),
		Reason:     reason,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(directory, "gh-pr-merge-overrides.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.Write(append(encoded, '\n'))
}
