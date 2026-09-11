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
// `wb worktree land` / `wb pr land` for this one call — set it, rerun the
// exact same Bash call, and the override is written to
// <wbhome>/agentguard/gh-pr-merge-overrides.jsonl before the call proceeds.
//
// This mirrors --allow-saturated-host and --take-over-lane elsewhere in WB:
// an override is never silent, it is always named and recorded, so a fleet
// audit can tell "the guard was never reached" from "the guard was
// deliberately overridden, and why".
const ghPrMergeOverrideEnv = "WB_AGENTGUARD_ALLOW_GH_PR_MERGE"

// inspectGh judges one `gh ...` invocation. Only `gh pr merge` is judged;
// every other gh subcommand — `pr view`, `pr checks`, `pr list`, and so on —
// is read-only from this guard's perspective and always allowed.
//
// This is deliberately NOT gated to a canonical clone or a managed worktree.
// Three merger lanes reimplemented landing step by step with `gh pr merge`,
// `git push --delete`, and hand-rolled ancestry checks instead of the
// `wb worktree land` / `wb pr land` verbs those lanes already had a contract
// to use (rule:land-with-wb-verb, sneat-co/backstage). That failure mode has
// nothing to do with which directory the agent happened to be sitting in, so
// neither does the refusal.
func inspectGh(words []string, projectsRoot string) *finding {
	if !isGhPrMerge(words) {
		return nil
	}
	if reason := strings.TrimSpace(os.Getenv(ghPrMergeOverrideEnv)); reason != "" {
		recordGhPrMergeOverride(projectsRoot, words, reason)
		return nil
	}
	return &finding{Message: ghPrMergeRefusal(words)}
}

// isGhPrMerge reports whether words invokes `gh pr merge`, tolerant of a
// global flag (and its value) before `pr` — `gh --repo owner/repo pr merge`
// — and any flags after `merge` (`gh pr merge 123 --squash --admin`).
//
// It finds the first bare "pr" word rather than requiring it immediately
// after "gh", because a global flag's own value (`owner/repo` for `--repo`)
// is not itself flag-prefixed and would otherwise be mistaken for the
// subcommand. Once "pr" is found, only a flag may separate it from "merge".
func isGhPrMerge(words []string) bool {
	for index := 1; index < len(words); index++ {
		if words[index] != "pr" {
			continue
		}
		rest := firstNonFlag(words[index+1:])
		return len(rest) > 0 && rest[0] == "merge"
	}
	return false
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
	fmt.Fprintf(&message, "Escape hatch: set %s=\"<reason>\" and rerun this exact call to\n", ghPrMergeOverrideEnv)
	message.WriteString("bypass this refusal once; the reason is recorded.\n")
	return message.String()
}

// ghPrMergeOverride is one recorded use of the escape hatch above.
type ghPrMergeOverride struct {
	Policy     string `json:"policy"`
	Command    string `json:"command"`
	Reason     string `json:"reason"`
	RecordedAt string `json:"recorded_at"`
}

// recordGhPrMergeOverride appends one audited line for an overridden refusal.
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
