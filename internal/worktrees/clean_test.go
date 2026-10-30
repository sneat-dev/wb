package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

func cleanTestPorts(repositories ...string) (cleanPorts, *[]GCOptions, *[]BranchCleanupOptions) {
	var gcCalls []GCOptions
	var branchCalls []BranchCleanupOptions
	var mu sync.Mutex
	ports := cleanPorts{
		discover: func(string) ([]discover.Repo, error) {
			var repos []discover.Repo
			for _, slug := range repositories {
				owner, name, _ := strings.Cut(slug, "/")
				repos = append(repos, discover.Repo{Org: owner, Name: name, Path: "/projects/" + slug})
			}
			return repos, nil
		},
		gc: func(_ context.Context, options GCOptions) (GCOutcome, error) {
			mu.Lock()
			gcCalls = append(gcCalls, options)
			mu.Unlock()
			return GCOutcome{Entries: []GCEntry{{Task: "t", Repository: "acme/app", WorktreeDir: "/w/t"}}, Inventoried: []string{"/w/t", "/w/hidden-by-only"}, Totals: map[string]int{"refused": 1, "eligible": 2, "retired": 3}}, nil
		},
		branches: func(_ context.Context, options BranchCleanupOptions) (BranchCleanupOutcome, error) {
			mu.Lock()
			branchCalls = append(branchCalls, options)
			mu.Unlock()
			return BranchCleanupOutcome{Results: []BranchCleanupResult{
				{BranchEntry: BranchEntry{Repository: options.Repository, Branch: "done", Disposition: BranchReceipted}, Eligible: true},
				{BranchEntry: BranchEntry{Repository: options.Repository, Branch: "gone", Disposition: BranchContained}, Applied: true},
				{BranchEntry: BranchEntry{Repository: options.Repository, Branch: "wip", Disposition: BranchUnique}},
				{BranchEntry: BranchEntry{Repository: options.Repository, Branch: "held", Disposition: BranchInUse}},
				{BranchEntry: BranchEntry{Repository: options.Repository, Branch: "main", Disposition: BranchProtected}},
			}, Diagnostics: []string{"note"}, ReportPath: "report-" + options.Repository}, nil
		},
		orphans: func(_ context.Context, options OrphanOptions) (OrphanReport, error) {
			if !options.Select("acme/app") || !options.Skip("/w/t") || !options.Skip("/w/hidden-by-only") || options.Skip("/elsewhere") {
				panic("orphan selection predicates lost")
			}
			var rows []OrphanWorktree
			for _, row := range []OrphanWorktree{
				{Path: "/elsewhere", Layout: LayoutExternal}, {Path: "/w/legacy-unseen", Layout: LayoutLegacy},
				{Path: "/w/current", Layout: LayoutCurrent}, {Path: "/w/shared", Layout: LayoutShared}, {Path: "/w/hidden-by-only", Layout: LayoutLegacy},
			} {
				if !options.Skip(row.Path) {
					rows = append(rows, row)
				}
			}
			return OrphanReport{Families: []OrphanFamily{{Worktrees: rows}}, Unscanned: []string{"unreadable"}}, nil
		},
	}
	return ports, &gcCalls, &branchCalls
}

func TestCleanComposesWorktreesBranchesAndUnmanagedUnderOneSelection(t *testing.T) {
	t.Parallel()
	ports, gcCalls, branchCalls := cleanTestPorts("acme/app", "acme/lib", "other/svc")
	gcFn, orphansFn := ports.gc, ports.orphans
	ports.gc = func(ctx context.Context, options GCOptions) (GCOutcome, error) {
		outcome, err := gcFn(ctx, options)
		outcome.Diagnostics = []ListDiagnostic{{Path: "/w/diag"}}
		return outcome, err
	}
	ports.orphans = func(ctx context.Context, options OrphanOptions) (OrphanReport, error) {
		if !options.Skip("/w/diag") || options.Select("other/svc") {
			t.Error("a checkout the inventory diagnosed is managed, not unmanaged")
		}
		return orphansFn(ctx, options)
	}
	outcome, err := cleanWithPorts(context.Background(), CleanOptions{ProjectsRoot: t.TempDir(), Orgs: []string{"acme"}, Workers: 4, Apply: true}, ports)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.Selection.Repositories, []string{"acme/app", "acme/lib"}) {
		t.Fatalf("repositories %v", outcome.Selection.Repositories)
	}
	gc := (*gcCalls)[0]
	if gc.Filter != "acme/*" || !gc.Apply || gc.DeleteRemote || gc.Select(ListResult{Repository: "other/svc"}) || !gc.Select(ListResult{Repository: "ACME/app"}) {
		t.Fatalf("gc options %+v", gc)
	}
	if len(*branchCalls) != 2 {
		t.Fatalf("branch calls %+v", *branchCalls)
	}
	for _, call := range *branchCalls {
		if call.Scope != BranchScopeLocal || !call.Receipts || !call.Apply || call.Filter != call.Repository || call.ReportDir == "" || !strings.HasSuffix(call.ReportDir, strings.ReplaceAll(call.Repository, "/", "-")) {
			t.Fatalf("branch options %+v", call)
		}
	}
	// Under --apply an eligible branch that did not retire failed: it is kept.
	if len(outcome.Branches) != 6 || outcome.Totals["retired"] != 5 || outcome.Totals["eligible"] != 2 || outcome.Totals["refused"] != 5 {
		t.Fatalf("branches %d totals %v", len(outcome.Branches), outcome.Totals)
	}
	// A legacy-layout worktree the inventory never saw is still reported; one
	// it saw but --only hid is not.
	if len(outcome.Unmanaged) != 2 || outcome.Unmanaged[0].Path != "/elsewhere" || outcome.Unmanaged[1].Path != "/w/legacy-unseen" || outcome.Totals["unmanaged"] != 2 || len(outcome.UnmanagedUnscanned) != 1 {
		t.Fatalf("unmanaged %+v", outcome.Unmanaged)
	}
	if len(outcome.BranchDiagnostics) != 2 || len(outcome.BranchReports) != 2 || outcome.Refused() != 5 {
		t.Fatalf("diagnostics %v reports %v", outcome.BranchDiagnostics, outcome.BranchReports)
	}
}

func TestCleanScopeAndSelectorsDecideWhichEnginesRun(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		options                     CleanOptions
		wantGC, wantBranches, wantU bool
		branchScope, walkFilter     string
	}{
		"remote touches no checkout":    {CleanOptions{Scope: CleanScopeRemote}, false, true, false, BranchScopeRemote, ""},
		"all retires origin branches":   {CleanOptions{Scope: CleanScopeAll, Repository: "acme/app"}, true, true, true, BranchScopeAll, "acme/app"},
		"a task selects checkouts only": {CleanOptions{Tasks: []string{"t"}, SupersededBy: "r.json"}, true, false, false, "", ""},
		"a checkout-only class":         {CleanOptions{Only: GCClassDirty}, true, false, false, "", ""},
		"unmanaged only":                {CleanOptions{Only: CleanClassUnmanaged}, true, false, true, "", ""},
		"a branch skips unmanaged":      {CleanOptions{Branch: "topic", Repository: "acme/app", SupersededBy: "r.json"}, true, true, false, BranchScopeLocal, "acme/app"},
		"landed covers both":            {CleanOptions{Only: CleanOnlyLanded, Filter: "acme"}, true, true, false, BranchScopeLocal, "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ports, gcCalls, branchCalls := cleanTestPorts("acme/app")
			unmanaged := false
			orphans := ports.orphans
			ports.orphans = func(ctx context.Context, options OrphanOptions) (OrphanReport, error) {
				unmanaged = true
				return orphans(ctx, options)
			}
			tc.options.ProjectsRoot = t.TempDir()
			if _, err := cleanWithPorts(context.Background(), tc.options, ports); err != nil {
				t.Fatal(err)
			}
			if (len(*gcCalls) == 1) != tc.wantGC || (len(*branchCalls) == 1) != tc.wantBranches || unmanaged != tc.wantU {
				t.Fatalf("gc %d branches %d unmanaged %v", len(*gcCalls), len(*branchCalls), unmanaged)
			}
			if tc.wantGC {
				gc := (*gcCalls)[0]
				if gc.Filter != tc.walkFilter || gc.DeleteRemote != (tc.options.Scope == CleanScopeAll) ||
					(gc.SupersededBy != "") != (len(tc.options.Tasks) == 1) ||
					(tc.options.Branch != "" && (gc.Select(ListResult{Repository: "acme/app", Branch: "other"}) || !gc.Select(ListResult{Repository: "acme/app", Branch: "topic"}))) {
					t.Fatalf("gc options %+v", gc)
				}
			}
			if tc.wantBranches {
				branch := (*branchCalls)[0]
				if branch.Scope != tc.branchScope || (branch.SupersededBy != "") != (tc.options.Branch != "") || branch.ReportDir != "" {
					t.Fatalf("branch options %+v", branch)
				}
			}
		})
	}
}

func TestCleanRefusesInvalidSelectionsBeforeAnyWork(t *testing.T) {
	t.Parallel()
	for name, options := range map[string]CleanOptions{
		"malformed glob":          {Filter: "acme/["},
		"repo without owner":      {Repository: "app"},
		"repo with bad segment":   {Repository: "acme/a b"},
		"bad org":                 {Orgs: []string{"a b"}},
		"bad scope":               {Scope: "everywhere"},
		"bad class":               {Only: "finished"},
		"task with remote scope":  {Tasks: []string{"t"}, Scope: CleanScopeRemote},
		"negative age":            {OlderThan: -time.Second},
		"supersession needs item": {SupersededBy: "r.json"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ports, gcCalls, _ := cleanTestPorts("acme/app")
			if _, err := cleanWithPorts(context.Background(), options, ports); err == nil || len(*gcCalls) != 0 {
				t.Fatalf("accepted %+v (gc calls %d)", options, len(*gcCalls))
			}
		})
	}
}

func TestCleanReportsEngineFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	ports, _, _ := cleanTestPorts("acme/app")
	ports.discover = func(string) ([]discover.Repo, error) { return nil, boom }
	if _, err := cleanWithPorts(context.Background(), CleanOptions{}, ports); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ports, _, _ = cleanTestPorts("acme/app")
	ports.gc = func(context.Context, GCOptions) (GCOutcome, error) { return GCOutcome{}, boom }
	if _, err := cleanWithPorts(context.Background(), CleanOptions{}, ports); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ports, _, _ = cleanTestPorts("acme/app")
	ports.orphans = func(context.Context, OrphanOptions) (OrphanReport, error) { return OrphanReport{}, boom }
	if _, err := cleanWithPorts(context.Background(), CleanOptions{}, ports); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ports, _, _ = cleanTestPorts("acme/app")
	ports.branches = func(context.Context, BranchCleanupOptions) (BranchCleanupOutcome, error) {
		return BranchCleanupOutcome{}, boom
	}
	outcome, err := cleanWithPorts(context.Background(), CleanOptions{Scope: CleanScopeRemote}, ports)
	if err != nil || len(outcome.BranchDiagnostics) != 1 || !strings.Contains(outcome.BranchDiagnostics[0], "acme/app: boom") || outcome.Refused() != 1 {
		t.Fatalf("a failing repository must be a diagnostic, not abort the sweep: %v %v", err, outcome.BranchDiagnostics)
	}
	ports, _, _ = cleanTestPorts("acme/app")
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanWithPorts(context.Background(), CleanOptions{ProjectsRoot: filepath.Join(blocked, "projects"), Apply: true, Scope: CleanScopeRemote}, ports); err == nil {
		t.Fatal("an unresolvable report home must refuse an apply")
	}
}

func TestCleanDescribesTheSelectionThatMatchedNothing(t *testing.T) {
	t.Parallel()
	ports, _, _ := cleanTestPorts("acme/app", "acme/app")
	for options, want := range map[*CleanOptions]string{
		{Orgs: []string{"zed"}, Repository: "zed/x"}: "--org zed --repo zed/x",
	} {
		_, err := cleanWithPorts(context.Background(), *options, ports)
		if !errors.Is(err, ErrNoRepositoriesMatched) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v", err)
		}
	}
	empty, gcCalls, _ := cleanTestPorts()
	if outcome, err := cleanWithPorts(context.Background(), CleanOptions{}, empty); err != nil || len(*gcCalls) != 1 || !(*gcCalls)[0].Select(ListResult{Repository: "moved/away"}) || len(outcome.Selection.Repositories) != 0 {
		t.Fatalf("without a selector an empty root still sweeps WB's own residue: %v %v", outcome.Selection, err)
	}
	outcome, err := cleanWithPorts(context.Background(), CleanOptions{}, ports)
	if err != nil || !reflect.DeepEqual(outcome.Selection.Repositories, []string{"acme/app"}) {
		t.Fatalf("duplicate clones must be one repository: %v %v", outcome.Selection.Repositories, err)
	}
}

func TestCleanClassSelectedGroupsLandedClasses(t *testing.T) {
	t.Parallel()
	for _, class := range []string{GCClassContained, GCClassLandedClean, GCClassLandedResidue, GCClassDetachedReview, BranchReceipted, BranchSuperseded} {
		if !CleanClassSelected(CleanOnlyLanded, class) {
			t.Fatalf("landed must select %s", class)
		}
	}
	if CleanClassSelected(CleanOnlyLanded, GCClassUnpushed) || !CleanClassSelected("", GCClassUnpushed) ||
		!CleanClassSelected(GCClassDirty, GCClassDirty) || CleanClassSelected(GCClassDirty, GCClassOpenPR) {
		t.Fatal("class selection")
	}
}

func TestResolveRetireNameMapsABranchToItsTask(t *testing.T) {
	t.Parallel()
	list := func(results ...ListResult) func(context.Context, ListOptions) (ListOutcome, error) {
		return func(_ context.Context, options ListOptions) (ListOutcome, error) {
			if options.Filter != "acme" || !options.IncludeDetached {
				t.Fatalf("options %+v", options)
			}
			return ListOutcome{Results: results}, nil
		}
	}
	task, err := resolveRetireNameWith(context.Background(), list(ListResult{Task: "batch5-assembly", Branch: "batch5"}, ListResult{Task: "batch5-assembly", Branch: "batch5"}, ListResult{Task: "x", Branch: "y"}), "", "acme", "batch5")
	if err != nil || task != "batch5-assembly" {
		t.Fatal(task, err)
	}
	if task, err := resolveRetireNameWith(context.Background(), list(), "", "acme", "batch5"); err != nil || task != "" {
		t.Fatal(task, err)
	}
	if _, err := resolveRetireNameWith(context.Background(), list(ListResult{Task: "b", Branch: "batch5"}, ListResult{Task: "a", Branch: "batch5"}), "", "acme", "batch5"); err == nil || !strings.Contains(err.Error(), "tasks a, b") {
		t.Fatal(err)
	}
	failing := func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, errors.New("boom") }
	if _, err := resolveRetireNameWith(context.Background(), failing, "", "", "batch5"); err == nil {
		t.Fatal("a failing inventory must not resolve")
	}
}

func TestGCApplyNeverRunsTwoRetirementsForOneRepositoryOrTaskAtOnce(t *testing.T) {
	t.Parallel()
	entries := []GCEntry{
		{Task: "a", Repository: "acme/x", Eligible: true}, {Task: "b", Repository: "acme/x", Eligible: true},
		{Task: "c", Repository: "acme/y", Eligible: true}, {Task: "c", Repository: "acme/z", Eligible: true},
		{Task: "d", Repository: "acme/w", Eligible: false},
	}
	var mu sync.Mutex
	busy := map[string]bool{}
	var ran []int
	runGCApplyConcurrently(entries, 4, func(index int) {
		entry := entries[index]
		mu.Lock()
		if busy["r"+entry.Repository] || busy["t"+entry.Task] {
			t.Errorf("entry %d overlapped another retirement", index)
		}
		busy["r"+entry.Repository], busy["t"+entry.Task] = true, true
		ran = append(ran, index)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		busy["r"+entry.Repository], busy["t"+entry.Task] = false, false
		mu.Unlock()
	})
	if len(ran) != 4 {
		t.Fatalf("ran %v, want every eligible entry once", ran)
	}
	runGCApplyConcurrently(nil, 0, func(int) { t.Fatal("nothing to apply") })
}
