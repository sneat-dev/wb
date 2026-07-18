package main

import (
	"fmt"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/tui"
)

func newSyncCmd() *cobra.Command {
	var (
		dryRun  bool
		workers int
		only    []string
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone/pull/prune local clones to match GitHub (parallel, with a live progress UI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runSync(projectsRoot, filterFlag, only, workers, dryRun)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the plan; change nothing")
	cmd.Flags().IntVarP(&workers, "workers", "j", 8, "max concurrent git/gh operations")
	cmd.Flags().StringArrayVarP(&only, "org", "o", nil, "only sync this org (repeatable); default: all your orgs + your own account")
	return cmd
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

func runSync(projectsRoot, filter string, only []string, workers int, dryRun bool) int {
	repos, err := fleet(projectsRoot, filter, func() []string { return syncOwners(only) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		return 1
	}
	if len(repos) == 0 {
		fmt.Println("no repos found")
		return 0
	}

	orgTotal := map[string]int{}
	for _, r := range repos {
		orgTotal[r.Org]++
	}

	interactive := term.IsTerminal(int(os.Stdout.Fd()))

	var results []fleetsync.Result
	if interactive {
		results = runSyncTUI(repos, orgTotal, projectsRoot, workers, dryRun)
	} else {
		results = runSyncPlain(repos, projectsRoot, workers, dryRun)
	}

	printSyncSummary(results)

	hasErrors := false
	needsReview := false
	for _, res := range results {
		if res.Status == fleetsync.Failed {
			hasErrors = true
		}
		if tui.Reviewable(res) {
			needsReview = true
		}
	}

	if interactive && needsReview {
		if err := runResultsBrowser(results); err != nil {
			fmt.Fprintln(os.Stderr, "results browser error:", err)
		}
	}

	if hasErrors {
		return 1
	}
	return 0
}

// runSyncPlain runs the worker pool without a TUI, for non-interactive
// (piped/CI) runs. Still parallel — --workers applies regardless of TTY.
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
	fmt.Printf("Archived removed  %d\n", counts[fleetsync.RemovedArchived])
	fmt.Printf("Archived kept     %d\n", counts[fleetsync.KeptArchived])
	fmt.Printf("Archived absent   %d\n", counts[fleetsync.AbsentArchived])
	fmt.Printf("Errors            %d\n", counts[fleetsync.Failed])
	for _, r := range results {
		if r.Status == fleetsync.Failed {
			fmt.Printf("  ✗ %s — %s\n", r.Repo.Slug(), r.Err)
		}
	}
}
