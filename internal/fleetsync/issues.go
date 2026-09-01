package fleetsync

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// RunMeta describes the sync run that produced an issues report. It carries
// what the results themselves cannot say: when the run happened, whether it
// was a dry run, whether archived pruning was requested, and whether the run
// failed before it scanned anything at all.
type RunMeta struct {
	StartedAt    time.Time
	ProjectsRoot string
	Scanned      int
	DryRun       bool
	// PruneArchived records whether --prune-archived was passed. Without it
	// the ArchivedNotPruned entries cannot be explained.
	PruneArchived bool
	// RunErr is set when the run failed before scanning anything — a GitHub
	// authentication or discovery failure. Results is empty in that case, and
	// the failure is itself the issue worth reporting.
	RunErr error
}

// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")

	groups := Summary(results)
	attention, _ := SummaryGroupByLabel(groups, "Needs attention")
	defects, informational := splitAttention(attention.Results)

	writeIssuesHeader(&out, meta, len(defects), len(informational))

	if len(defects) == 0 && len(informational) == 0 {
		out.WriteString("All repositories are in sync. Nothing requires attention.\n")
		return out.String()
	}

	out.WriteString(inspectFirstNote)

	if len(defects) > 0 {
		out.WriteString("## Needs attention\n\n")
		for _, result := range defects {
			writeAttentionEntry(&out, result)
		}
	}
	if len(informational) > 0 {
		writeArchivedNotPruned(&out, informational)
	}
	return out.String()
}

// inspectFirstNote is the standing instruction to whoever reads the report.
// Resolution options are deliberately prose rather than a ready-to-paste
// command: rebasing or resetting the wrong clone is not recoverable from a
// report.
const inspectFirstNote = "> Inspection commands are read-only and safe to run as-is. Resolution " +
	"options are deliberately not copy-paste ready: inspect first, then choose.\n\n"

// splitAttention divides the attention group into genuine defects and the
// merely informational. An archived repository that was not pruned is in the
// attention group so it stays visible, but nothing about it is broken — the
// operator simply did not pass --prune-archived. Rendering it beside real
// defects would invite an agent to "fix" a repository that is fine.
//
// The two are mutually exclusive by construction: needsAttention matches the
// four defect statuses first and only falls through to ArchivedNotPruned in
// its default branch.
func splitAttention(results []Result) (defects, informational []Result) {
	for _, result := range results {
		switch result.Status {
		case Diverged, NoUpstream, Unpushed, ArchivedUnlandable:
			defects = append(defects, result)
		default:
			informational = append(informational, result)
		}
	}
	return defects, informational
}

func writeIssuesHeader(out *strings.Builder, meta RunMeta, defects, informational int) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	if defects == 0 && informational == 0 {
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	} else {
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Needs attention:** %d · **Archived, not pruned:** %d\n\n",
			meta.Scanned, defects, informational)
	}
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Findings are real, but " +
			"unpushed-commit detection normally runs after a pull and may be incomplete.\n\n")
	}
}

// writeAttentionEntry renders one defect: what it is, where it is, how to look
// at it, and what the options are.
func writeAttentionEntry(out *strings.Builder, result Result) {
	fmt.Fprintf(out, "### %s — %s\n\n", result.Repo.Slug(), result.Status)
	writeClone(out, result)

	switch result.Status {
	case Diverged, NoUpstream:
		fmt.Fprintf(out, "- **Tracking:** %s\n", result.Tracking.Summary())
		out.WriteString("- **Impact:** not pulled\n")
	case Unpushed:
		fmt.Fprintf(out, "- **Impact:** pulled, but holds %s\n", result.Detail.Summary())
	case ArchivedUnlandable:
		fmt.Fprintf(out, "- **Impact:** archived on GitHub, so its %s can never be pushed\n",
			result.Detail.Summary())
	}
	if result.Reason != "" {
		fmt.Fprintf(out, "- **Detail:** %s\n", result.Reason)
	}

	out.WriteString("\n**Inspect**\n\n```sh\n")
	for _, command := range inspectCommands(result) {
		out.WriteString(command + "\n")
	}
	out.WriteString("```\n\n**Resolve** — choose after inspecting:\n\n")
	for _, option := range resolveOptions(result) {
		out.WriteString("- " + option + "\n")
	}
	out.WriteString("\n")
}

func writeClone(out *strings.Builder, result Result) {
	if result.Repo.Path == "" {
		out.WriteString("- **Clone:** not present locally\n")
		return
	}
	fmt.Fprintf(out, "- **Clone:** `%s`\n", result.Repo.Path)
}

// inspectCommands returns read-only commands that show the reader exactly what
// state the repository is in. Nothing here mutates a repository.
func inspectCommands(result Result) []string {
	at := "git -C " + shellQuote(result.Repo.Path)
	switch result.Status {
	case Diverged:
		branch := result.Tracking.Branch
		return []string{
			fmt.Sprintf("%s log --oneline --left-right %s...%s", at, branch, result.Tracking.Upstream),
			fmt.Sprintf("%s cherry -v %s %s", at, result.Tracking.Upstream, branch),
			at + " status -sb",
		}
	case NoUpstream:
		return []string{
			at + " log --oneline origin/main..HEAD",
			at + " status -sb",
			at + " branch -vv",
		}
	case Unpushed, ArchivedUnlandable:
		return []string{
			at + " log --oneline --branches --not --remotes",
			at + " status -sb",
		}
	default:
		return []string{at + " status -sb"}
	}
}

// resolveOptions describes the ways out, with their consequences. A canonical
// clone is expected to sit on its base branch with nothing unpushed, so every
// option ends by pointing real work at a worktree rather than the clone.
func resolveOptions(result Result) []string {
	slug := result.Repo.Slug()
	worktree := fmt.Sprintf("Move real work off the canonical clone: `wb worktree create <task> %s`", slug)
	switch result.Status {
	case Diverged:
		return []string{
			"If the local commits are unlanded work, replay them onto the upstream: " +
				fmt.Sprintf("`git -C %s rebase %s`", shellQuote(result.Repo.Path), result.Tracking.Upstream),
			"If `git cherry` above marked every commit `-`, they already landed upstream under different SHAs; " +
				"reset the clone to its upstream instead of rebasing",
			worktree,
		}
	case NoUpstream:
		branch := result.Tracking.Branch
		return []string{
			fmt.Sprintf("Publish the branch: `git push -u origin %s`", branch),
			"Or, if the work already landed upstream under a squashed commit, return the clone to its " +
				"base branch and delete the leftover branch",
			"Or `wb repo init-remote " + shellQuote(result.Repo.Path) + "` if the branch was never published at all",
			worktree,
		}
	case Unpushed:
		return []string{
			"Push the commits if they are finished work",
			"Or open a PR from a worktree if they still need review",
			"Or discard them if they were superseded — confirm with the log above first",
			worktree,
		}
	case ArchivedUnlandable:
		return []string{
			"unarchive the repository on GitHub if the commits must land",
			"Or discard the commits and let `wb sync --prune-archived` remove the clone",
			"Never force the push: the remote is read-only while archived",
		}
	default:
		return []string{worktree}
	}
}

// writeArchivedNotPruned renders the informational section: archived
// repositories that were pulled like any other clone because pruning was not
// requested.
func writeArchivedNotPruned(out *strings.Builder, results []Result) {
	out.WriteString("## Archived, not pruned\n\n")
	out.WriteString("Informational: these are archived on GitHub and were pulled like any other clone " +
		"because `--prune-archived` was not passed. Nothing is broken.\n\n")
	for _, result := range results {
		fmt.Fprintf(out, "- `%s` — pass `--prune-archived` to evaluate it for cleanup\n", result.Repo.Slug())
	}
	out.WriteString("\n")
}

// safeShellWord matches a path that needs no quoting, so the common case reads
// as a plain command a human can scan.
var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote renders value as a single POSIX shell word.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if safeShellWord.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
