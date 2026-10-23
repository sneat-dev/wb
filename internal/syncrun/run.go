package syncrun

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

type Options struct {
	ProjectsRoot, Filter                        string
	Owners                                      []string
	Workers                                     int
	DryRun, Publish, PruneArchived, Interactive bool
}
type Presentation struct {
	Interactive func(context.Context, []discover.Repo, map[string]int, Options, io.Writer) []fleetsync.Result
	Summary     func(io.Writer, []fleetsync.Result, bool, bool)
}
type Completion func(fleetsync.RunMeta, []fleetsync.Result, Options, io.Writer, io.Writer) int
type Dependencies struct {
	Discovery fleetdiscovery.Resolver
	Reconcile func(context.Context, []discover.Repo) []discover.Repo
	Dispatch  func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error)
	Now       func() time.Time
}
type Service struct {
	deps         Dependencies
	complete     Completion
	presentation Presentation
}

func New(deps Dependencies, completion Completion, presentation Presentation) Service {
	return Service{deps: deps, complete: completion, presentation: presentation}
}
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
func (service Service) Run(ctx context.Context, options Options, out, errOut io.Writer) int {
	startedAt := service.deps.Now().UTC()
	projectsRoot, filter, only, workers, dryRun, publish, pruneArchived := options.ProjectsRoot, options.Filter, options.Owners, options.Workers, options.DryRun, options.Publish, options.PruneArchived
	_ = publish
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
	interactive := options.Interactive
	reportOut := syncReportWriter(interactive, out, errOut)

	owners, err := resolveSyncOwners(only, service.deps.Discovery.AuthUser, service.deps.Discovery.MemberOrgs)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "wb: %v\nRe-authenticate with: gh auth login -h github.com\n", err)
		// Broken authentication leaves every clone unmanaged. That is a
		// finding worth handing to an agent, not something to leave on stderr.
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, reportOut, errOut)
		return 1
	}
	repos, err := service.deps.Discovery.Discover(projectsRoot, filter, func() []string { return owners })
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "discovery error:", err)
		writeSyncIssuesReport(meta(0, err), nil, projectsRoot, reportOut, errOut)
		return 1
	}
	repos = service.deps.Reconcile(ctx, repos)
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
			results = service.presentation.Interactive(ctx, repos, orgTotal, options, errOut)
		} else {
			results = fleetsync.Batch(ctx, repos, fleetsync.BatchOptions{ProjectsRoot: projectsRoot, Workers: workers, DryRun: dryRun, PruneArchived: pruneArchived}, fleetsync.BatchObserver{})
		}

		service.presentation.Summary(reportOut, results, pruneArchived, interactive)
	}

	code := service.complete(meta(len(results), nil), results, options, reportOut, errOut)
	if dryRun {
		return code
	}
	hookCode := finishSyncLifecycleHooks(ctx, results, service.deps.Dispatch, errOut)
	if code != 0 {
		return code
	}
	return hookCode
}
func finishSyncLifecycleHooks(ctx context.Context, results []fleetsync.Result, dispatch syncLifecycleDispatch, errOut io.Writer) int {
	events := syncLifecycleEvents(results)
	if len(events) == 0 {
		return 0
	}
	report, err := dispatch(ctx, events)
	for _, warning := range report.Warnings {
		_, _ = fmt.Fprintln(errOut, "warning:", warning)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "warning: lifecycle hooks were not dispatched:", err)
	}
	return 0
}
func syncLifecycleEvents(results []fleetsync.Result) []lifecyclehooks.Event {
	var events []lifecyclehooks.Event
	for _, result := range results {
		cause := ""
		switch {
		case result.Status == fleetsync.Cloned && result.HeadSHA != "":
			cause = "sync-clone"
		case result.Updated && result.HeadSHA != "":
			cause = "sync-pull"
		default:
			continue
		}
		events = append(events, lifecyclehooks.Event{
			Name: lifecyclehooks.EventCheckoutUpdated, Repository: "github.com/" + result.Repo.Org + "/" + result.Repo.Name,
			Checkout: result.Repo.Path, OldSHA: result.BeforeHeadSHA, NewSHA: result.HeadSHA, Cause: cause,
		})
	}
	return events
}

type syncLifecycleDispatch func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error)

func syncReportWriter(interactive bool, out, errOut io.Writer) io.Writer {
	if interactive {
		return errOut
	}
	return out
}
