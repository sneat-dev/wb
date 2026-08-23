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
					message: "one or more repositories failed to sync; each failure is listed after the summary above",
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
	cmd.Flags().IntVar(&workers, "parallel", 8, "maximum repositories to inspect concurrently")
	cmd.Flags().IntVarP(&workers, "workers", "j", 8, "maximum repositories to inspect concurrently")
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
func syncOwners(only []string) []string {
	if len(only) > 0 {
		return only
	}
	var owners []string
	if user, err := discover.AuthUser(); err == nil && user != "" {
		owners = append(owners, user)
	}
	if orgs, err := discover.MemberOrgs(); err == nil {
		owners = append(owners, orgs...)
	}
	return owners
}

func runSync(projectsRoot, filter string, only []string, workers int, dryRun, publish bool, deps remoteDeps) int {
	repos, err := fleet(projectsRoot, filter, func() []string { return syncOwners(only) })
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

		printSyncSummary(results)

		needsReview := false
		for _, res := range results {
			if tui.Reviewable(res) {
				needsReview = true
			}
		}

		if interactive && needsReview {
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
		} else if err := runRemotePublish(deps, projectsRoot, filter, workers, false, false, out); err != nil {
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

func printSyncSummary(results []fleetsync.Result) {
	counts := map[fleetsync.Status]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	fmt.Printf("\n━━━ Summary ━━━\n")
	fmt.Printf("Not owned/fork    %d\n", counts[fleetsync.NoOp])
	fmt.Printf("Cloned            %d\n", counts[fleetsync.Cloned])
	fmt.Printf("Pulled            %d\n", counts[fleetsync.Pulled])
	fmt.Printf("Skipped (dirty)   %d\n", counts[fleetsync.SkippedDirty])
	fmt.Printf("Skipped (ignored) %d\n", counts[fleetsync.SkippedIgnored])
	fmt.Printf("Empty remote      %d\n", counts[fleetsync.EmptyRemote])
	fmt.Printf("Archived removed  %d\n", counts[fleetsync.RemovedArchived])
	fmt.Printf("Archived kept     %d\n", counts[fleetsync.KeptArchived])
	fmt.Printf("Archived absent   %d\n", counts[fleetsync.AbsentArchived])
	fmt.Printf("Needs attention   %d\n", counts[fleetsync.Diverged]+counts[fleetsync.NoUpstream]+
		counts[fleetsync.Unpushed]+counts[fleetsync.ArchivedUnlandable])
	fmt.Printf("Errors            %d\n", counts[fleetsync.Failed])
	for _, r := range results {
		switch r.Status {
		case fleetsync.Diverged, fleetsync.NoUpstream:
			fmt.Printf("  ! %s — %s; not pulled\n", r.Repo.Slug(), r.Tracking.Summary())
		case fleetsync.Unpushed:
			fmt.Printf("  ! %s — pulled, but holds %s\n", r.Repo.Slug(), r.Detail.Summary())
		case fleetsync.ArchivedUnlandable:
			fmt.Printf("  ! %s — archived, so its %s can never be pushed; discard them or unarchive\n",
				r.Repo.Slug(), r.Detail.Summary())
		}
	}
	for _, r := range results {
		if r.Status == fleetsync.Failed {
			fmt.Printf("  ✗ %s — %s\n", r.Repo.Slug(), r.Err)
		}
	}
}
