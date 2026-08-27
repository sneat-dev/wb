package main

import (
	"fmt"
	"io"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/tui"
)

func newSyncCmd() *cobra.Command {
	var (
		dryRun  bool
		workers int
		only    []string
		publish bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone/pull/prune local clones to match GitHub (parallel, with a live progress UI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			owners := requestedSyncOwners(cmd, only)
			if code := runSync(projectsRoot, filterFlag, owners, workers, dryRun, publish, defaultRemoteDeps()); code != 0 {
				return &exitError{
					code:    code,
					message: "sync did not complete; see diagnostics above",
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the plan; change nothing")
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

func runSync(projectsRoot, filter string, only []string, workers int, dryRun, publish bool, deps remoteDeps) int {
	owners, err := syncOwners(only)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wb: %v\nRe-authenticate with: gh auth login -h github.com\n", err)
		return exitFindings
	}
	repos, err := fleet(projectsRoot, filter, func() []string { return owners })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
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
			results = runSyncTUI(repos, orgTotal, projectsRoot, workers, dryRun)
		} else {
			results = runSyncPlain(repos, projectsRoot, workers, dryRun)
		}

		printSyncSummary(os.Stdout, results)

		if interactive {
			if err := runResultsBrowser(results); err != nil {
				fmt.Fprintln(os.Stderr, "results browser error:", err)
			}
		}
	}

	return finishSync(results, publish, dryRun, deps, projectsRoot, filter, workers, os.Stdout, os.Stderr)
}

// finishSync maps sync results to an exit code and, when asked, publishes
// this machine's state. A publish failure is reported to errOut and never
// changes the sync exit code. dryRun short-circuits publish entirely: a
// `--dry-run --publish` sync changed nothing, so publishing its (unreal)
// outcome would be a lie.
func finishSync(results []fleetsync.Result, publish, dryRun bool, deps remoteDeps, projectsRoot, filter string, workers int, out, errOut io.Writer) int {
	hasErrors := false
	for _, res := range results {
		if res.Status == fleetsync.Failed {
			hasErrors = true
		}
	}

	if hasErrors {
		return 1
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
func runSyncPlain(repos []discover.Repo, projectsRoot string, workers int, dryRun bool) []fleetsync.Result {
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
				resultsCh <- fleetsync.Sync(r, projectsRoot, dryRun)
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
func runSyncTUI(repos []discover.Repo, orgTotal map[string]int, projectsRoot string, workers int, dryRun bool) []fleetsync.Result {
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
					res := fleetsync.Sync(r, projectsRoot, dryRun)
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

func printSyncSummary(out io.Writer, results []fleetsync.Result) {
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
		switch r.Status {
		case fleetsync.Diverged, fleetsync.NoUpstream:
			_, _ = fmt.Fprintf(out, "  ! %s — %s; not pulled\n", r.Repo.Slug(), r.Tracking.Summary())
		case fleetsync.Unpushed:
			_, _ = fmt.Fprintf(out, "  ! %s — pulled, but holds %s\n", r.Repo.Slug(), r.Detail.Summary())
		case fleetsync.ArchivedUnlandable:
			_, _ = fmt.Fprintf(out, "  ! %s — archived, so its %s can never be pushed; discard them or unarchive\n",
				r.Repo.Slug(), r.Detail.Summary())
		}
	}
	errors, _ := fleetsync.SummaryGroupByLabel(groups, "Errors")
	for _, r := range errors.Results {
		_, _ = fmt.Fprintf(out, "  ✗ %s — %s\n", r.Repo.Slug(), r.Err)
	}
}
