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

	// Owners and Filter record how the run was scoped. Without them the
	// report cannot distinguish "the fleet is clean" from "the two
	// repositories I looked at are clean" — and because every run overwrites
	// the same file, a scoped run would otherwise silently replace a
	// fleet-wide finding set with a false all-clear.
	Owners []string
	Filter string
	// Discovered is how many repositories the run selected; Scanned is how
	// many it finished. Fewer scanned than discovered means the run did not
	// complete, which no report may describe as health.
	Discovered int
}

// Scoped reports whether the run looked at less than the whole fleet.
func (m RunMeta) Scoped() bool { return len(m.Owners) > 0 || m.Filter != "" }

// Interrupted reports whether the run stopped before finishing every
// repository it selected — the shape a Ctrl-C in the progress UI leaves, which
// returns the results collected so far.
func (m RunMeta) Interrupted() bool { return m.Discovered > 0 && m.Scanned < m.Discovered }

// Complete reports whether this run may speak for the whole fleet. Only a
// complete, unscoped, non-dry run can honestly say everything is in sync.
func (m RunMeta) Complete() bool { return !m.Scoped() && !m.Interrupted() && !m.DryRun }

// scopeLine describes the run's selection in one line, always rendered so a
// reader never has to assume the report covered everything.
func (m RunMeta) scopeLine() string {
	var parts []string
	if len(m.Owners) > 0 {
		parts = append(parts, "owners "+strings.Join(m.Owners, ", "))
	}
	if m.Filter != "" {
		parts = append(parts, "filter "+m.Filter)
	}
	if len(parts) == 0 {
		return "**Scope:** every owner and organization this account can see"
	}
	return "**Scope:** restricted to " + strings.Join(parts, "; ") +
		" — this report says nothing about any repository outside that selection"
}

// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")

	groups := Summary(results)
	attention, _ := SummaryGroupByLabel(groups, "Needs attention")
	failures, _ := SummaryGroupByLabel(groups, "Errors")
	defects, informational := splitAttention(attention.Results)

	writeIssuesHeader(&out, meta, len(defects), len(informational), len(failures.Results))

	// A run that never scanned cannot say anything about the fleet, so it
	// reports only its own failure. Claiming "in sync" here would be a lie an
	// agent would act on.
	if meta.RunErr != nil {
		writeRunFailure(&out, meta)
		return out.String()
	}

	if len(defects) == 0 && len(informational) == 0 && len(failures.Results) == 0 {
		switch {
		case meta.Scanned == 0:
			// A --filter or --org that matched nothing looks identical to a
			// clean fleet unless said explicitly: "no issues" here is a
			// selection outcome, not evidence any repository was checked.
			out.WriteString("No repository was scanned. This is a selection result — an --org or " +
				"--filter matched nothing — not a health result: it says nothing about whether any " +
				"repository is in sync.\n")
		case meta.DryRun:
			// Diverged and Unpushed are structurally unreachable in dry-run
			// (see syncActive): a dry run never attempts the pull that would
			// reveal them. Silence here is silence, not a clean bill of
			// health.
			// The header stamp already explains what a dry run cannot see, so
			// this line only has to refuse the health claim itself.
			out.WriteString("Nothing needed attention among what a dry run can detect. " +
				"That is not a clean bill of health — see the dry-run note above.\n")
		case meta.Interrupted():
			// A Ctrl-C in the progress UI returns the results collected so
			// far, so the repositories never reached are indistinguishable
			// from clean ones unless the shortfall is stated.
			// The Incomplete banner above already gives the counts, so this
			// line only has to refuse the health claim.
			out.WriteString("Nothing needed attention among the repositories this run reached. " +
				"That is not a clean bill of health — see the incomplete note above. " +
				"Re-run to finish.\n")
		case meta.Scoped():
			// The file is overwritten every run, so a scoped run that says
			// "all repositories are in sync" replaces a fleet-wide finding
			// set with a claim it has no standing to make.
			out.WriteString("Nothing needed attention within this run's scope. That scope was " +
				"restricted (see above), so this is not a statement about the rest of the fleet.\n")
		default:
			out.WriteString("All repositories are in sync. Nothing requires attention.\n")
		}
		return out.String()
	}

	if len(defects) > 0 || len(failures.Results) > 0 {
		out.WriteString(inspectFirstNote)
	}

	if len(defects) > 0 {
		out.WriteString("## Needs attention\n\n")
		for _, result := range defects {
			writeAttentionEntry(&out, result)
		}
	}
	if len(informational) > 0 {
		writeArchivedNotPruned(&out, informational)
	}
	if len(failures.Results) > 0 {
		out.WriteString("## Errors\n\n")
		for _, result := range failures.Results {
			writeErrorEntry(&out, result)
		}
	}
	return out.String()
}

// inspectFirstNote is the standing instruction to whoever reads the report.
// The report deliberately never picks a resolution for the reader — every
// option is a choice that deserves inspection first.
const inspectFirstNote = "> Inspection commands are read-only and safe to run as-is. Resolution " +
	"options are choices, not a script — read the inspection output before running any of them.\n\n"

// splitAttention divides the attention group into genuine defects and the
// merely informational. An archived repository that was not pruned is
// informational ONLY when its own sync outcome was itself benign (Pulled,
// NoOp, Cloned, AbsentArchived) — the operator simply did not pass
// --prune-archived, and nothing about the repository is broken. Any other
// status the attention group selects — one of the four recognized defect
// statuses, or an archived-not-pruned repository whose own sync outcome was
// itself a fault such as Failed or SkippedDirty — is a genuine defect and
// must never be filed as merely informational: an agent reading "Nothing is
// broken" would skip a repository that actually needs help.
//
// The previous version of this function and its doc comment claimed the two
// buckets were "mutually exclusive by construction" because needsAttention
// supposedly matched the four defect statuses before ever falling through to
// ArchivedNotPruned. That was false: needsAttention's default arm returns
// ArchivedNotPruned regardless of status, so a Failed or SkippedDirty
// archived-not-pruned repository reached this function's old default branch
// and was misfiled as informational — while also, independently, matching
// the Errors group, so it was rendered (and counted) twice.
func splitAttention(results []Result) (defects, informational []Result) {
	for _, result := range results {
		switch {
		case result.Status == Diverged, result.Status == NoUpstream,
			result.Status == Unpushed, result.Status == ArchivedUnlandable:
			defects = append(defects, result)
		case result.ArchivedNotPruned && isBenignStatus(result.Status):
			informational = append(informational, result)
		default:
			// A result that reaches here matched needsAttention (so
			// something about it is worth surfacing) but is neither a
			// recognized defect status nor genuinely benign — e.g. a
			// SkippedDirty or Failed archived-not-pruned repository, should
			// needsAttention ever again select one. Keep it visible as a
			// defect rather than silently dropping it or mislabeling it
			// informational.
			defects = append(defects, result)
		}
	}
	return defects, informational
}

// isBenignStatus reports whether status, on its own, describes nothing wrong
// with a repository. It is the same list needsAttention uses to decide
// whether ArchivedNotPruned alone should count as attention-worthy.
func isBenignStatus(status Status) bool {
	switch status {
	case Pulled, NoOp, Cloned, AbsentArchived:
		return true
	default:
		return false
	}
}

func writeIssuesHeader(out *strings.Builder, meta RunMeta, defects, informational, failures int) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	switch {
	case meta.RunErr != nil:
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Run failed before scanning**\n\n", meta.Scanned)
	case meta.Scanned == 0:
		out.WriteString("**Scanned:** 0 repositories · **Selection matched nothing**\n\n")
	case defects == 0 && informational == 0 && failures == 0:
		fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	default:
		fmt.Fprintf(out,
			"**Scanned:** %d repositories · **Needs attention:** %d · **Archived, not pruned:** %d · **Errors:** %d\n\n",
			meta.Scanned, defects, informational, failures)
	}
	out.WriteString(meta.scopeLine() + "\n\n")
	if meta.Interrupted() {
		fmt.Fprintf(out, "**Incomplete:** this run finished %d of %d selected repositories. "+
			"The rest were never checked and are absent from this report — their absence is not "+
			"evidence they are healthy.\n\n", meta.Scanned, meta.Discovered)
	}
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Diverged and Unpushed are only " +
			"detected after a real pull, so this run cannot classify either — their absence below is " +
			"not evidence the fleet is clean. Re-run without --dry-run to classify them.\n\n")
	}
}

// writeAttentionEntry renders one defect: what it is, where it is, how to look
// at it, and what the options are.
func writeAttentionEntry(out *strings.Builder, result Result) {
	fmt.Fprintf(out, "### %s — %s\n\n", result.Repo.Slug(), result.Status)
	writeClone(out, result)

	switch result.Status {
	case Diverged, NoUpstream:
		fmt.Fprintf(out, "- **Tracking:** %s\n", oneLine(result.Tracking.Summary()))
		out.WriteString("- **Impact:** not pulled\n")
	case Unpushed:
		fmt.Fprintf(out, "- **Impact:** pulled, but holds %s\n", result.Detail.Summary())
	case ArchivedUnlandable:
		fmt.Fprintf(out, "- **Impact:** archived on GitHub, so its %s can never be pushed\n",
			result.Detail.Summary())
	}
	if result.Archived {
		// The default (non --prune-archived) sync path can leave an archived
		// repository at Diverged, NoUpstream or Unpushed — Status alone never
		// says "archived", and an agent told to "push the commits" or
		// "discard them if superseded" on a read-only remote would either
		// fail loudly or destroy commits that exist nowhere else.
		out.WriteString("- **Archived:** this repository is archived on GitHub — its remote is " +
			"read-only, so any commits held only here can never be pushed to it\n")
	}
	if result.Reason != "" {
		fmt.Fprintf(out, "- **Detail:** %s\n", oneLine(result.Reason))
	}
	writeHead(out, result)

	out.WriteString("\n**Inspect**\n\n```sh\n")
	if check := driftCheck(result); check != "" {
		out.WriteString(check + "\n")
	}
	for _, command := range inspectCommands(result) {
		out.WriteString(command + "\n")
	}
	out.WriteString("```\n\n**Resolve** — choose after inspecting:\n\n")
	for _, option := range resolveOptions(result) {
		out.WriteString("- " + option + "\n")
	}
	out.WriteString("\n")
}

// writeHead records the commit this finding was made against. Everything else
// in the entry describes the repository as it was at that commit; if HEAD has
// moved since, the finding may no longer be true and a destructive remedy —
// resetting to upstream, discarding commits — can take work that did not exist
// when the report was written.
func writeHead(out *strings.Builder, result Result) {
	if result.HeadSHA == "" {
		return
	}
	fmt.Fprintf(out, "- **HEAD when reported:** `%s`\n", result.HeadSHA)
}

// driftCheck is the first thing a reader should run: it proves the repository
// still sits where the report says before anything is changed. Empty when
// there is no clone or no recorded commit to compare against.
func driftCheck(result Result) string {
	if result.Repo.Path == "" || result.HeadSHA == "" {
		return ""
	}
	return fmt.Sprintf("git -C %s rev-parse HEAD   # must equal %s, or this entry is stale",
		shellQuote(result.Repo.Path), result.HeadSHA)
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
	if result.Repo.Path == "" {
		// shellQuote("") is "''", and `git -C '' <anything>` silently runs
		// against the current working directory instead of failing — so a
		// missing-clone entry must never emit a `git -C` command at all.
		return []string{"gh repo view " + result.Repo.Slug()}
	}
	at := "git -C " + shellQuote(result.Repo.Path)
	switch result.Status {
	case Diverged:
		branch := shellQuote(result.Tracking.Branch)
		upstream := shellQuote(result.Tracking.Upstream)
		return []string{
			fmt.Sprintf("%s log --oneline --left-right %s...%s", at, branch, upstream),
			fmt.Sprintf("%s cherry -v %s %s", at, upstream, branch),
			at + " status -sb",
		}
	case NoUpstream:
		return []string{
			// Branch-agnostic, matching the Unpushed form below: a hardcoded
			// origin/main..HEAD fails with "unknown revision" on any clone
			// whose default branch is master, develop, or anything else.
			at + " log --oneline --branches --not --remotes",
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
// non-archived option ends by pointing real work at a worktree rather than
// the clone.
func resolveOptions(result Result) []string {
	if result.Archived {
		// Every path here — Diverged, NoUpstream, Unpushed, or the dedicated
		// ArchivedUnlandable status — shares one fact that overrides all of
		// them: the remote is read-only. Offering push, rebase-and-push, or
		// publish-the-branch would either fail or, worse, an agent could
		// misread the failure and reach for something destructive instead.
		return []string{
			"Unarchive the repository on GitHub if the commits must land",
			"Or discard the commits and let `wb sync --prune-archived` remove the clone",
			"Never force the push: the remote is read-only while archived",
		}
	}

	// wb worktree create requires --model and --original-prompt-file (a WB
	// Work Log claim), so the single-line command this used to render fails
	// exactly as typed. State the move as prose instead of a broken
	// copy-pasteable command.
	worktree := "Move real work off the canonical clone into a WB worktree " +
		"(`wb worktree create` — see `$wb-worktrees`)."
	switch result.Status {
	case Diverged:
		return []string{
			"If the local commits are unlanded work, replay them onto the upstream: " +
				fmt.Sprintf("`git -C %s rebase %s`", shellQuote(result.Repo.Path), shellQuote(result.Tracking.Upstream)),
			"If `git cherry` above marked every commit `-`, they already landed upstream under different SHAs; " +
				"reset the clone to its upstream instead of rebasing",
			worktree,
		}
	case NoUpstream:
		branch := result.Tracking.Branch
		if branch == "" {
			// A detached HEAD has no branch to publish, so every
			// branch-shaped remedy below is meaningless for it.
			return []string{
				"Identify the commit and put it on a branch before anything else: " +
					fmt.Sprintf("`git -C %s switch -c <branch>`", shellQuote(result.Repo.Path)),
				"Or, if the detached commit is already reachable from a branch, return the clone to its base branch",
				worktree,
			}
		}
		return []string{
			fmt.Sprintf("Publish the branch: `git -C %s push -u origin %s`",
				shellQuote(result.Repo.Path), shellQuote(branch)),
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
	default:
		return []string{worktree}
	}
}

// writeArchivedNotPruned renders the informational section: archived
// repositories that were pulled like any other clone because pruning was not
// requested, and whose own sync outcome was itself benign.
func writeArchivedNotPruned(out *strings.Builder, results []Result) {
	out.WriteString("## Archived, not pruned\n\n")
	out.WriteString("Informational: these are archived on GitHub and were pulled like any other clone " +
		"because `--prune-archived` was not passed. Nothing is broken.\n\n")
	for _, result := range results {
		fmt.Fprintf(out, "- `%s` — pass `--prune-archived` to evaluate it for cleanup\n", result.Repo.Slug())
	}
	out.WriteString("\n")
}

// writeErrorEntry renders one failed repository. The error value is reproduced
// verbatim: a re-worded error is a different error, and the reader may need to
// match it against Git's or gh's own output. It is fenced, not quoted inline,
// because git's combined output is multi-line and may contain blank lines —
// an inline code span breaks on the first one, and everything after becomes
// document structure in a file an AI agent reads as instructions.
func writeErrorEntry(out *strings.Builder, result Result) {
	fmt.Fprintf(out, "### %s — failed\n\n", result.Repo.Slug())
	writeClone(out, result)
	out.WriteString("- **Error:**\n\n")
	out.WriteString(fencedBlock(fmt.Sprintf("%v", result.Err)))

	out.WriteString("\n**Inspect**\n\n```sh\n")
	out.WriteString("gh auth status -h github.com\n")
	if result.Repo.Path == "" {
		// No local clone exists yet (e.g. the clone itself failed), so there
		// is no working tree for `git -C` to inspect — and git -C '' would
		// silently inspect the current directory instead of failing loudly.
		out.WriteString("gh repo view " + result.Repo.Slug() + "\n")
	} else {
		at := "git -C " + shellQuote(result.Repo.Path)
		out.WriteString(at + " config --get remote.origin.url\n")
		out.WriteString(at + " status -sb\n")
	}
	out.WriteString("```\n\n**Resolve** — choose after inspecting:\n\n")
	out.WriteString("- Re-authenticate if the error is about credentials: `gh auth login -h github.com`\n")
	out.WriteString("- Switch the remote to SSH if this clone was created outside WB with an HTTPS URL\n")
	if result.Repo.Path != "" {
		// `wb sync --filter <name>` runs a full sync, which ends in
		// finishSync and overwrites last-sync-issues.md — destroying the
		// very report the reader is working through, with no history kept
		// by design. A plain, scoped git pull shows the same full output
		// without that side effect.
		out.WriteString("- Re-run to see the full git output: " +
			fmt.Sprintf("`git -C %s pull --ff-only`\n", shellQuote(result.Repo.Path)))
	}
	out.WriteString("\n")
}

// writeRunFailure reports a run that failed before scanning. Broken GitHub
// authentication leaves every clone unmanaged, so it is exactly the finding
// worth handing to an agent — and until now it existed only on stderr.
func writeRunFailure(out *strings.Builder, meta RunMeta) {
	out.WriteString("## Run failed\n\n")
	out.WriteString("The sync failed before it reached the fleet, so no repository was scanned and " +
		"nothing below reflects the state of any clone.\n\n")
	out.WriteString("- **Error:**\n\n")
	out.WriteString(fencedBlock(fmt.Sprintf("%v", meta.RunErr)))
	out.WriteString("\n**Inspect**\n\n```sh\ngh auth status -h github.com\n```\n\n")
	out.WriteString("**Resolve** — choose after inspecting:\n\n")
	out.WriteString("- Re-authenticate: `gh auth login -h github.com`\n")
	out.WriteString("- Then re-run `wb sync`; every clone is unmanaged until it succeeds\n")
}

// oneLine collapses newlines in untrusted, inline-rendered text to spaces.
// Every other value this file renders as untrusted content sits inside a
// fenced block (see fencedBlock); result.Reason and TrackingState.Summary
// are instead rendered inline in a single list item, where even one embedded
// blank line would end that list item and let the remainder become document
// structure in a file an AI agent reads as instructions.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// fencedBlock renders untrusted text as a fenced code block whose fence is
// longer than the longest run of consecutive backticks inside content, so
// nothing in content can terminate the fence early and spill into document
// structure. Git errors carry a repository's own bytes — tree entry names,
// branch names, remote URLs — into a file an AI agent reads as instructions,
// so they must stay data here, never markup, regardless of what they
// contain: blank lines, headings, or backtick runs meant to defeat a fixed
// three-backtick fence.
func fencedBlock(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fenceLen := longest + 1
	if fenceLen < 3 {
		fenceLen = 3
	}
	fence := strings.Repeat("`", fenceLen)
	return fence + "text\n" + strings.TrimRight(content, "\n") + "\n" + fence + "\n"
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
