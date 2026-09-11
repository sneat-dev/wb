package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/tui"
)

func newSyncCmd() *cobra.Command {
	var (
		dryRun        bool
		workers       int
		only          []string
		publish       bool
		pruneArchived bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone/pull/prune local clones to match GitHub (parallel, with a full-screen progress UI)",
		Long: `Clone missing repositories, fast-forward existing ones, and — only with
--prune-archived — delete a local clone whose repository is confirmed archived
on GitHub, exactly when it passes the same safety predicate 'wb archive clean'
uses (live-confirmed archived status, no uncommitted/untracked changes, no
stash, no unpushed commits on any branch, no local-only branch, no unpushed
tag, no linked worktree, no non-terminal WB Work Log claim, not marked
wb.skip-sync).

Without --prune-archived, an archived repository is never deleted: sync pulls
its local clone exactly like any other repository's, and the report still
names it as archived so it is never silently indistinguishable from an
ordinary clone.

When stdout is a terminal, progress uses the full terminal and the final text
report is written to stderr after the terminal is restored. Piped, CI, and
--non-interactive runs keep their report on stdout.`,
		Example: `# Preview fleet reconciliation without changing repositories
wb sync --dry-run

# Sync selected owners with bounded concurrency
wb sync --org owner-a --org owner-b --parallel 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			owners := requestedSyncOwners(cmd, only)
			if code := runSync(cmd.Context(), projectsRoot, filterFlag, owners, workers, dryRun, publish, pruneArchived, defaultRemoteDeps(), cmd.OutOrStdout(), cmd.ErrOrStderr()); code != 0 {
				return &exitError{
					code:    code,
					message: "sync did not complete; see diagnostics above",
				}
			}
			return nil
		},
	}
	setDiscoveryTerms(cmd, "sync update pull clone fleet repositories reconcile refresh prune archived")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the plan; change nothing")
	cmd.Flags().BoolVar(&pruneArchived, "prune-archived", false, "delete a local clone whose repository is confirmed archived on GitHub, but only when it passes the same safety predicate as 'wb archive clean' (default: pull archived repos like any other, never delete)")
	// --parallel is the fleet-wide name for this ceiling: six other commands
	// already spell it that way, and "workers" reads as a second noun beside
	// WB's own tasks. --workers/-j stays as a hidden deprecated alias so
	// existing scripts and muscle memory keep working.
	// GitHub can close SSH handshakes when a large fleet starts too many at
	// once. Four still keeps sync comfortably parallel while avoiding that
	// transport limit on the default path; callers that know their network can
	// opt into a higher ceiling explicitly.
	cmd.Flags().IntVar(&workers, "parallel", 4, "maximum repositories to inspect concurrently")
	cmd.Flags().IntVarP(&workers, "workers", "j", 4, "maximum repositories to inspect concurrently")
	_ = cmd.Flags().MarkDeprecated("workers", "use --parallel instead")
	cmd.Flags().StringArrayVarP(&only, "org", "o", nil, "only sync this org (repeatable); default: all your orgs + your own account")
	cmd.Flags().BoolVar(&publish, "publish", false, "after a successful sync, run wb remote publish")
	return cmd
}

func requestedSyncOwners(cmd *cobra.Command, only []string) []string {
	owners := append([]string(nil), only...)
	// `wb --org acme sync` sets the root repeatable flag, while
	// `wb sync --org acme` sets sync's command-local restriction. Both
	// spellings are advertised by Cobra and therefore have identical selection
	// semantics.
	if rootOrg := cmd.Root().PersistentFlags().Lookup("org"); rootOrg != nil && rootOrg.Changed {
		owners = append(owners, extraOrgs...)
	}
	return owners
}

// syncOwners returns only when non-empty (an explicit -o restriction);
// otherwise it auto-discovers the authenticated user plus their member orgs.
// Unlike fleetOwners, there is no "extra" concept here — -o restricts rather
// than adds.
func syncOwners(only []string) ([]string, error) {
	return resolveSyncOwners(only, discover.AuthUser, discover.MemberOrgs)
}

// resolveSyncOwners verifies GitHub authentication before selecting owners.
// Sync must never silently treat an authentication failure as "not owned":
// doing so leaves every local repository unmanaged while reporting success.
func resolveSyncOwners(
	only []string,
	authUser func() (string, error),
	memberOrgs func() ([]string, error),
) ([]string, error) {
	user, err := authUser()
	if err != nil {
		return nil, fmt.Errorf("GitHub authentication failed: %w", err)
	}
	if len(only) > 0 {
		return only, nil
	}
	if user == "" {
		return nil, fmt.Errorf("GitHub authentication failed: authenticated user is empty")
	}
	orgs, err := memberOrgs()
	if err != nil {
		return nil, fmt.Errorf("could not list GitHub organizations: %w", err)
	}
	return append([]string{user}, orgs...), nil
}

func runSync(ctx context.Context, projectsRoot, filter string, only []string, workers int, dryRun, publish, pruneArchived bool, deps remoteDeps, out, errOut io.Writer) int {
	startedAt := time.Now().UTC()
	// discovered is filled in once the fleet is known. Until then a report can
	// only describe a run that never got that far.
	discovered := 0
	meta := func(scanned int, runErr error) fleetsync.RunMeta {
		return fleetsync.RunMeta{
			StartedAt:     startedAt,
			ProjectsRoot:  projectsRoot,
			Scanned:       scanned,
			DryRun:        dryRun,
			PruneArchived: pruneArchived,
			RunErr:        runErr,
			// The selection is recorded, not just its size: every run
			// overwrites the same report, so a reader must be able to tell a
			// fleet-wide all-clear from a two-repository one.
			Owners:     only,
			Filter:     filter,
			Discovered: discovered,
		}
	}
	interactive := console.Interactive(out, nonInteractive)
	reportOut := syncReportWriter(interactive, out, errOut)

	owners, err := syncOwners(only)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "wb: %v\nRe-authenticate with: gh auth login -h github.com\n", err)
		// Broken authentication leaves every clone unmanaged. That is a
		// finding worth handing to an agent, not something to leave on stderr.
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, reportOut, errOut)
		return exitFindings
	}
	repos, err := fleet(projectsRoot, filter, func() []string { return owners })
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "discovery error:", err)
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, reportOut, errOut)
		return 1
	}
	repos = discover.ReconcileTransfers(ctx, repos, discover.ResolveCanonicalRepository)
	discovered = len(repos)

	var results []fleetsync.Result
	if len(repos) == 0 {
		_, _ = fmt.Fprintln(out, "no repos found")
	} else {
		orgTotal := map[string]int{}
		for _, r := range repos {
			orgTotal[r.Org]++
		}

		// The live progress UI takes over the full terminal, so it runs only when
		// there is a human at one. Its final text report goes to stderr after the
		// alternate screen has been restored; non-interactive output stays on
		// stdout for scripts.
		if interactive {
			results = runSyncTUI(ctx, repos, orgTotal, projectsRoot, workers, dryRun, pruneArchived, errOut)
		} else {
			results = runSyncPlain(ctx, repos, projectsRoot, workers, dryRun, pruneArchived)
		}

		printSyncSummary(reportOut, results, pruneArchived, interactive)
	}

	return finishSync(meta(len(results), nil), results, publish, dryRun, deps, projectsRoot, filter, workers, reportOut, errOut)
}

// syncReportWriter keeps an interactive run's completion report visible after
// Bubble Tea restores the terminal, without changing the stdout contract for
// pipes, CI, or --non-interactive.
func syncReportWriter(interactive bool, out, errOut io.Writer) io.Writer {
	if interactive {
		return errOut
	}
	return out
}

// finishSync maps sync results to an exit code, writes the issues report, and,
// when asked, publishes this machine's state. A publish failure is reported to
// errOut and never changes the sync exit code. dryRun short-circuits publish
// entirely: a `--dry-run --publish` sync changed nothing, so publishing its
// (unreal) outcome would be a lie.
func finishSync(meta fleetsync.RunMeta, results []fleetsync.Result, publish, dryRun bool, deps remoteDeps, projectsRoot, filter string, workers int, out, errOut io.Writer) int {
	// Written before the error short-circuit below, because a run WITH errors
	// is exactly the run whose report matters most. Unlike the checkout
	// markers, this also runs for a dry run: dry-run detection is read-only
	// and identical, so its findings are real — IssuesMarkdown stamps the
	// report so the reader knows the fleet was not actually pulled.
	writeSyncIssuesReport(meta, results, projectsRoot, out, errOut)

	hasErrors := false
	for _, res := range results {
		if res.Status == fleetsync.Failed {
			hasErrors = true
		}
	}

	if hasErrors {
		return 1
	}

	// A clone sync just created, refreshed, or moved is a checkout an agent may
	// arrive at next, so it gets its .worktree.md here. A dry run writes
	// nothing, and a marker WB could not write never fails a sync that
	// otherwise succeeded — the whole file is an orientation aid.
	if !dryRun {
		refreshSyncedCheckoutMarkers(results, projectsRoot, errOut)
	}

	if publish {
		if dryRun {
			_, _ = fmt.Fprintln(out, "dry-run: skipping remote publish")
		} else if err := runRemotePublishWithProgress(deps, projectsRoot, filter, workers, false, false, out, errOut); err != nil {
			_, _ = fmt.Fprintln(errOut, "remote publish failed (sync itself succeeded):", err)
		}
	}

	return 0
}

// runSyncPlain runs the worker pool without a TUI, for non-interactive
// (piped/CI) runs. Still parallel — --parallel applies regardless of TTY.
func runSyncPlain(ctx context.Context, repos []discover.Repo, projectsRoot string, workers int, dryRun, pruneArchived bool) []fleetsync.Result {
	jobs := make(chan discover.Repo)
	go func() {
		for _, r := range repos {
			jobs <- r
		}
		close(jobs)
	}()

	resultsCh := make(chan fleetsync.Result)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				resultsCh <- fleetsync.Sync(ctx, r, projectsRoot, dryRun, pruneArchived)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var results []fleetsync.Result
	for res := range resultsCh {
		results = append(results, res)
	}
	return results
}

// runSyncTUI runs the worker pool while a bubbletea progress program renders
// overall + per-org bars and a live tail of in-flight repos.
func runSyncTUI(ctx context.Context, repos []discover.Repo, orgTotal map[string]int, projectsRoot string, workers int, dryRun, pruneArchived bool, errOut io.Writer) []fleetsync.Result {
	p := tea.NewProgram(tui.NewProgressModel(orgTotal, workers))

	go func() {
		jobs := make(chan discover.Repo)
		go func() {
			for _, r := range repos {
				jobs <- r
			}
			close(jobs)
		}()
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for r := range jobs {
					p.Send(tui.RepoStarted{Org: r.Org, Name: r.Name})
					res := fleetsync.Sync(ctx, r, projectsRoot, dryRun, pruneArchived)
					p.Send(tui.RepoDone{Result: res})
				}
			}()
		}
		wg.Wait()
		p.Send(tui.SyncDone{})
	}()

	final, err := p.Run()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "tui error:", err)
	}
	pm, _ := final.(tui.ProgressModel)
	return pm.Results
}

type syncSummaryStyles struct {
	enabled    bool
	title      lipgloss.Style
	section    lipgloss.Style
	attention  lipgloss.Style
	failure    lipgloss.Style
	repository lipgloss.Style
	worktree   lipgloss.Style
}

func newSyncSummaryStyles(enabled bool) syncSummaryStyles {
	return syncSummaryStyles{
		enabled:    enabled,
		title:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4B5FD")),
		section:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7DD3FC")),
		attention:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FBBF24")),
		failure:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F87171")),
		repository: lipgloss.NewStyle().Bold(true),
		worktree:   lipgloss.NewStyle().Foreground(lipgloss.Color("#86EFAC")),
	}
}

func (s syncSummaryStyles) render(style lipgloss.Style, value string) string {
	if !s.enabled {
		return value
	}
	return style.Render(value)
}

func (s syncSummaryStyles) sectionHeading(section fleetsync.SummarySection) string {
	switch section {
	case fleetsync.SummaryAttention:
		return s.render(s.attention, string(section))
	case fleetsync.SummaryErrors:
		return s.render(s.failure, string(section))
	default:
		return s.render(s.section, string(section))
	}
}

func printSyncSummary(out io.Writer, results []fleetsync.Result, pruneArchived, styled bool) {
	groups := fleetsync.Summary(results)
	styles := newSyncSummaryStyles(styled)
	var summary strings.Builder
	_, _ = fmt.Fprintln(&summary)
	_, _ = fmt.Fprintln(&summary, styles.render(styles.title, "━━━ Summary ━━━"))
	printCount := func(group fleetsync.SummaryGroup) {
		line := fmt.Sprintf("%-24s%d", group.Label, len(group.Results))
		switch group.Section {
		case fleetsync.SummaryAttention:
			line = styles.render(styles.attention, line)
		case fleetsync.SummaryErrors:
			line = styles.render(styles.failure, line)
		}
		_, _ = fmt.Fprintln(&summary, line)
	}
	var section fleetsync.SummarySection
	for _, group := range groups {
		// An empty failure section followed by attention details reads as though
		// those warnings are errors. Omit it entirely when there are no errors.
		if group.Section == fleetsync.SummaryErrors && len(group.Results) == 0 {
			continue
		}
		if group.Section != section {
			section = group.Section
			_, _ = fmt.Fprintf(&summary, "\n%s\n", styles.sectionHeading(section))
		}
		printCount(group)
		switch group.Section {
		case fleetsync.SummaryAttention:
			for _, result := range group.Results {
				writeSyncAttention(&summary, styles, result)
			}
		case fleetsync.SummaryErrors:
			for _, result := range group.Results {
				marker := styles.render(styles.failure, "✗")
				repository := styles.render(styles.repository, result.Repo.Slug())
				_, _ = fmt.Fprintf(&summary, "    %s %s — %s\n", marker, repository, result.Err)
			}
		}
	}
	if pruneArchived {
		printArchivedPruning(&summary, results)
	}
	if styled {
		_, _ = lipgloss.Fprint(out, summary.String())
	} else {
		_, _ = io.WriteString(out, summary.String())
	}
}

func writeSyncAttention(out io.Writer, styles syncSummaryStyles, result fleetsync.Result) {
	marker := styles.render(styles.attention, "!")
	repository := styles.render(styles.repository, result.Repo.Slug())
	switch {
	case result.Status == fleetsync.Diverged, result.Status == fleetsync.NoUpstream:
		_, _ = fmt.Fprintf(out, "    %s %s — %s; not pulled\n", marker, repository, result.Tracking.Summary())
	case result.Status == fleetsync.Unpushed && len(result.Detail.UnpushedBranches) > 0:
		for _, branch := range result.Detail.UnpushedBranches {
			location := branch.Branch
			if branch.Worktree != "" && filepath.Clean(branch.Worktree) != filepath.Clean(result.Repo.Path) {
				location = "🌳 " + location
			}
			location = styles.render(styles.worktree, location)
			_, _ = fmt.Fprintf(out, "    %s %s %s — %s not yet pushed\n",
				marker, repository, location, commitCount(len(branch.Commits)))
		}
	case result.Status == fleetsync.Unpushed:
		_, _ = fmt.Fprintf(out, "    %s %s — %s not yet pushed\n",
			marker, repository, commitCount(len(result.Detail.Unpushed)))
	case result.Status == fleetsync.ArchivedUnlandable:
		_, _ = fmt.Fprintf(out, "    %s %s — archived, so its %s can never be pushed; discard them or unarchive\n",
			marker, repository, result.Detail.Summary())
	case result.Status == fleetsync.RepositoryTransferRequired:
		_, _ = fmt.Fprintf(out, "    %s %s → %s — %s\n", marker, result.Repo.TransferFrom, repository, result.Reason)
	case result.ArchivedNotPruned:
		_, _ = fmt.Fprintf(out, "    %s %s — archived; not pruned (pass --prune-archived to enable cleanup)\n", marker, repository)
	}
}

func commitCount(count int) string {
	if count == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", count)
}

// printArchivedPruning names, per archived repository, exactly what
// --prune-archived did or refused and why — the same "no bare count" honesty
// wb archive clean's own report gives.
func printArchivedPruning(out io.Writer, results []fleetsync.Result) {
	var archived []fleetsync.Result
	for _, r := range results {
		if r.Archived {
			archived = append(archived, r)
		}
	}
	if len(archived) == 0 {
		return
	}
	_, _ = fmt.Fprintln(out, "\nArchived (--prune-archived)")
	for _, r := range archived {
		switch r.Status {
		case fleetsync.RemovedArchived:
			// The receipt path is printed, not just written: a deletion the
			// operator can see but not later account for is the gap this
			// receipt exists to close.
			if r.ReceiptPath != "" {
				_, _ = fmt.Fprintf(out, "  deleted      %s — %s (receipt: %s)\n", r.Repo.Slug(), r.Reason, r.ReceiptPath)
			} else {
				_, _ = fmt.Fprintf(out, "  deleted      %s — %s\n", r.Repo.Slug(), r.Reason)
			}
		case fleetsync.KeptArchived, fleetsync.ArchivedUnlandable:
			_, _ = fmt.Fprintf(out, "  skipped      %s — %s\n", r.Repo.Slug(), r.Reason)
		case fleetsync.AbsentArchived:
			_, _ = fmt.Fprintf(out, "  absent       %s — not cloned locally; nothing to prune\n", r.Repo.Slug())
		case fleetsync.Failed:
			_, _ = fmt.Fprintf(out, "  failed       %s — %s\n", r.Repo.Slug(), r.Err)
		}
	}
}
