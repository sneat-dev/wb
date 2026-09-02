package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
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
		Short: "Clone/pull/prune local clones to match GitHub (parallel, with a live progress UI)",
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
ordinary clone.`,
		Example: `# Preview fleet reconciliation without changing repositories
wb sync --dry-run

# Sync selected owners with bounded concurrency
wb sync --org owner-a --org owner-b --parallel 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			owners := requestedSyncOwners(cmd, only)
			if code := runSync(cmd.Context(), projectsRoot, filterFlag, owners, workers, dryRun, publish, pruneArchived, defaultRemoteDeps()); code != 0 {
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

func runSync(ctx context.Context, projectsRoot, filter string, only []string, workers int, dryRun, publish, pruneArchived bool, deps remoteDeps) int {
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

	owners, err := syncOwners(only)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wb: %v\nRe-authenticate with: gh auth login -h github.com\n", err)
		// Broken authentication leaves every clone unmanaged. That is a
		// finding worth handing to an agent, not something to leave on stderr.
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, os.Stdout, os.Stderr)
		return exitFindings
	}
	repos, err := fleet(projectsRoot, filter, func() []string { return owners })
	discovered = len(repos)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, os.Stdout, os.Stderr)
		return 1
	}

	var results []fleetsync.Result
	if len(repos) == 0 {
		fmt.Println("no repos found")
	} else {
		orgTotal := map[string]int{}
		for _, r := range repos {
			orgTotal[r.Org]++
		}

		// The live progress UI and the results browser both take over the terminal
		// and wait for keystrokes, so they run only when there is a human at one.
		interactive := console.Interactive(os.Stdout, nonInteractive)

		if interactive {
			results = runSyncTUI(ctx, repos, orgTotal, projectsRoot, workers, dryRun, pruneArchived)
		} else {
			results = runSyncPlain(ctx, repos, projectsRoot, workers, dryRun, pruneArchived)
		}

		printSyncSummary(os.Stdout, results, pruneArchived)

		// Written here, before runResultsBrowser — not only inside finishSync
		// below. runResultsBrowser blocks on a keystroke, so without this an
		// operator who walks away with it open (or whose terminal dies there)
		// is left with a report describing the previous run, with a plausible
		// timestamp and no sign a newer sync ever finished. finishSync writes
		// again on the way out; that second write is harmless, since it
		// renders these same results and meta to the same path.
		showResultsBrowser(meta(len(results), nil), results, projectsRoot, interactive, runResultsBrowser, os.Stdout, os.Stderr)
	}

	return finishSync(meta(len(results), nil), results, publish, dryRun, deps, projectsRoot, filter, workers, os.Stdout, os.Stderr)
}

// showResultsBrowser writes the issues report and then, only when interactive,
// hands off to the (blocking) results browser. browser is a parameter — not a
// direct call to runResultsBrowser — purely so a test can assert the report
// exists before the browser runs: the real browser is a bubbletea program
// that needs a TTY and cannot run under `go test`.
func showResultsBrowser(
	meta fleetsync.RunMeta,
	results []fleetsync.Result,
	projectsRoot string,
	interactive bool,
	browser func([]fleetsync.Result) error,
	out, errOut io.Writer,
) {
	if !interactive {
		// finishSync writes the report on every path. The early write below
		// exists only to beat runResultsBrowser, which blocks on a keystroke —
		// with no browser there is nothing to beat, and writing here as well
		// would write the file twice and print its path twice.
		return
	}
	writeSyncIssuesReport(meta, results, projectsRoot, out, errOut)
	if err := browser(results); err != nil {
		_, _ = fmt.Fprintln(errOut, "results browser error:", err)
	}
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
func runSyncTUI(ctx context.Context, repos []discover.Repo, orgTotal map[string]int, projectsRoot string, workers int, dryRun, pruneArchived bool) []fleetsync.Result {
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
		fmt.Fprintln(os.Stderr, "tui error:", err)
	}
	pm, _ := final.(tui.ProgressModel)
	return pm.Results
}

func runResultsBrowser(results []fleetsync.Result) error {
	p := tea.NewProgram(tui.NewResultsModel(results))
	_, err := p.Run()
	return err
}

func printSyncSummary(out io.Writer, results []fleetsync.Result, pruneArchived bool) {
	groups := fleetsync.Summary(results)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "━━━ Summary ━━━")
	printCount := func(label string, count int) { _, _ = fmt.Fprintf(out, "%-20s%d\n", label, count) }
	var section fleetsync.SummarySection
	for _, group := range groups {
		if group.Section != section {
			section = group.Section
			_, _ = fmt.Fprintf(out, "\n%s\n", section)
		}
		printCount(group.Label, len(group.Results))
	}
	attention, _ := fleetsync.SummaryGroupByLabel(groups, "Needs attention")
	for _, r := range attention.Results {
		switch {
		case r.Status == fleetsync.Diverged, r.Status == fleetsync.NoUpstream:
			_, _ = fmt.Fprintf(out, "  ! %s — %s; not pulled\n", r.Repo.Slug(), r.Tracking.Summary())
		case r.Status == fleetsync.Unpushed:
			_, _ = fmt.Fprintf(out, "  ! %s — pulled, but holds %s\n", r.Repo.Slug(), r.Detail.Summary())
		case r.Status == fleetsync.ArchivedUnlandable:
			_, _ = fmt.Fprintf(out, "  ! %s — archived, so its %s can never be pushed; discard them or unarchive\n",
				r.Repo.Slug(), r.Detail.Summary())
		case r.ArchivedNotPruned:
			_, _ = fmt.Fprintf(out, "  ! %s — archived; not pruned (pass --prune-archived to enable cleanup)\n", r.Repo.Slug())
		}
	}
	errors, _ := fleetsync.SummaryGroupByLabel(groups, "Errors")
	for _, r := range errors.Results {
		_, _ = fmt.Fprintf(out, "  ✗ %s — %s\n", r.Repo.Slug(), r.Err)
	}
	if pruneArchived {
		printArchivedPruning(out, results)
	}
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
