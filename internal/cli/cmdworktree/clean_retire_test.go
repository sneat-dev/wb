package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func cleanDeps(outcome worktrees.CleanOutcome, err error) CleanDependencies {
	return CleanDependencies{
		Progress: func(*cobra.Command, bool) InventoryProgress { return retirementProgress() },
		Run:      func(context.Context, worktrees.CleanOptions) (worktrees.CleanOutcome, error) { return outcome, err },
	}
}

func TestCleanCommandTurnsAnUnmatchedSelectionIntoAFinding(t *testing.T) {
	t.Parallel()
	root := "root"
	unmatched := fmt.Errorf("%w --filter nomatch/* under root", worktrees.ErrNoRepositoriesMatched)
	err := retirementExecute(NewClean(retirementRuntime(&root), cleanDeps(worktrees.CleanOutcome{}, unmatched)), nil, io.Discard, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "coded 1: ") || !strings.Contains(err.Error(), "0 repositories matched --filter nomatch/*") {
		t.Fatalf("an unmatched filter must exit with findings, got %v", err)
	}
	if err := retirementExecute(NewClean(retirementRuntime(&root), cleanDeps(worktrees.CleanOutcome{}, nil)), []string{"positional"}, io.Discard, io.Discard); err == nil {
		t.Fatal("clean takes selectors, not positional tasks")
	}
}

func TestCleanCommandRendersEverySection(t *testing.T) {
	t.Parallel()
	outcome := worktrees.CleanOutcome{
		Selection: worktrees.CleanSelection{Repositories: []string{"acme/app"}},
		Worktrees: &worktrees.GCOutcome{Totals: map[string]int{}},
		Branches: []worktrees.BranchCleanupResult{
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Branch: "done", Disposition: worktrees.BranchReceipted, Evidence: "merged pull request #7"}, Eligible: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Branch: "gone", Disposition: worktrees.BranchContained}, Applied: true},
			{BranchEntry: worktrees.BranchEntry{Repository: "acme/app", Branch: "wip", Disposition: worktrees.BranchUnique, Evidence: "2 unique"}, SkipReason: "carries unique work", Error: "boom"},
		},
		BranchDiagnostics:  []string{"acme/lib: unreachable"},
		BranchReports:      []string{"/reports/branch"},
		Unmanaged:          []worktrees.OrphanWorktree{{Repository: "acme/app", Branch: "old", Layout: worktrees.LayoutExternal, Disposition: worktrees.DispositionDecide, Path: "/elsewhere", Evidence: []string{"idle"}}, {Path: "/bare"}},
		UnmanagedUnscanned: []string{"/unreadable"},
		Totals:             map[string]int{"eligible": 1, "retired": 1, "refused": 1, "unmanaged": 2},
	}
	for _, apply := range []bool{false, true} {
		outcome.Apply = apply
		var out bytes.Buffer
		if err := printClean(cleanCmdWriting(&out), outcome); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		for _, want := range []string{
			"selected 1 repositories", "worktrees", "no WB worktrees", "branches without a worktree",
			"done", "would retire", "merged pull request #7", "retired", "keep", "kept: carries unique work", "error: boom",
			"warning: acme/lib: unreachable", "branch report: /reports/branch",
			"unmanaged worktrees (reported, never removed)", "/elsewhere", "idle", "warning: not scanned: /unreadable",
			map[bool]string{false: "1 would retire, 1 kept, 2 unmanaged", true: "1 retired, 1 kept, 2 unmanaged"}[apply],
		} {
			if !strings.Contains(text, want) {
				t.Errorf("apply=%v output lacks %q:\n%s", apply, want, text)
			}
		}
	}
	var out bytes.Buffer
	if err := printClean(cleanCmdWriting(&out), worktrees.CleanOutcome{Totals: map[string]int{}}); err != nil || !strings.Contains(out.String(), "none\n") {
		t.Fatalf("a remote-only pass prints no worktree section and says no branches: %q %v", out.String(), err)
	}
	for allow := 0; allow < 4; allow++ {
		if err := printClean(cleanCmdWriting(&cwWtFailWriter{Allow: allow}), outcome); !errors.Is(err, errCwWtWrite) {
			t.Fatalf("allow=%d: %v", allow, err)
		}
	}
}

func cleanCmdWriting(out io.Writer) *cobra.Command {
	command := NewClean(retirementRuntime(new(string)), CleanDependencies{})
	command.SetOut(out)
	return command
}

func retireDeps() RetireDependencies {
	deps := retirementCleanupDeps(worktrees.CleanupOutcome{})
	deps.Resolve = func(context.Context, string, string, string) (string, error) { return "", nil }
	deps.Branch = func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error) {
		return worktrees.BranchCleanupOutcome{}, nil
	}
	return deps
}

func TestRetireResolvesABranchNameToItsTask(t *testing.T) {
	t.Parallel()
	root := "root"
	deps := retireDeps()
	var ran [][]string
	deps.Run = func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		ran = append(ran, append([]string(nil), options.Tasks...))
		if options.Tasks[0] == "batch5" {
			return worktrees.CleanupOutcome{}, &worktrees.TaskNotFoundError{Task: "batch5"}
		}
		if !options.AllowResidue {
			t.Fatal("--allow-residue lost")
		}
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "batch5-assembly"}}}}, nil
	}
	deps.Resolve = func(_ context.Context, projectsRoot, filter, name string) (string, error) {
		if projectsRoot != "root" || filter != "only" || name != "batch5" {
			t.Fatal(projectsRoot, filter, name)
		}
		return "batch5-assembly", nil
	}
	var errOut bytes.Buffer
	if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), []string{"batch5", "--allow-residue"}, io.Discard, &errOut); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ran, [][]string{{"batch5"}, {"batch5-assembly"}}) || !strings.Contains(errOut.String(), "batch5 is the branch of task batch5-assembly") {
		t.Fatalf("ran %v stderr %q", ran, errOut.String())
	}
	if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), []string{"batch5"}, io.Discard, &cwWtFailWriter{}); !errors.Is(err, errCwWtWrite) {
		t.Fatalf("the resolution note's write error must surface: %v", err)
	}
	failing := retireDeps()
	failing.Run = deps.Run
	resolveErr := errors.New("inventory unreadable")
	failing.Resolve = func(context.Context, string, string, string) (string, error) { return "", resolveErr }
	if err := retirementExecute(NewRetire(retirementRuntime(&root), failing), []string{"batch5"}, io.Discard, io.Discard); err != resolveErr {
		t.Fatal(err)
	}
	other := errors.New("not a lookup failure")
	failing.Run = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{}, other
	}
	if err := retirementExecute(NewRetire(retirementRuntime(&root), failing), []string{"batch5"}, io.Discard, io.Discard); err != other {
		t.Fatal(err)
	}
}

func TestRetireRetiresABareBranchNoCheckoutHolds(t *testing.T) {
	t.Parallel()
	root := "root"
	notFound := func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{}, &worktrees.TaskNotFoundError{Task: "datatug-io-cutover"}
	}
	for name, tc := range map[string]struct {
		args    []string
		results []worktrees.BranchCleanupResult
		scope   string
		want    string
		wantErr string
	}{
		"planned":              {[]string{"datatug-io-cutover", "--remote", "--repo=datatug/websites", "--json"}, []worktrees.BranchCleanupResult{{BranchEntry: worktrees.BranchEntry{Branch: "datatug-io-cutover"}, Eligible: true}}, worktrees.BranchScopeAll, `"eligible": true`, ""},
		"retired":              {[]string{"datatug-io-cutover", "--apply", "--repo=datatug/websites"}, []worktrees.BranchCleanupResult{{BranchEntry: worktrees.BranchEntry{Repository: "datatug/websites", Scope: "local", Branch: "datatug-io-cutover", Evidence: "receipt"}, Applied: true}}, worktrees.BranchScopeLocal, "retired datatug/websites local datatug-io-cutover: receipt", ""},
		"kept":                 {[]string{"datatug-io-cutover"}, []worktrees.BranchCleanupResult{{BranchEntry: worktrees.BranchEntry{Branch: "datatug-io-cutover"}, SkipReason: "unique"}}, worktrees.BranchScopeLocal, "keep", "was kept"},
		"absent":               {[]string{"datatug-io-cutover"}, nil, worktrees.BranchScopeLocal, "", "needs --remote"},
		"absent on origin too": {[]string{"datatug-io-cutover", "--remote"}, nil, worktrees.BranchScopeAll, "", "selected repositories"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := retireDeps()
			deps.Run = notFound
			deps.Branch = func(_ context.Context, options worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error) {
				if options.Branch != "datatug-io-cutover" || options.Scope != tc.scope || !options.Receipts || options.Filter != "only" ||
					!reflect.DeepEqual(options.PeerEvidence, []string{"peer.json"}) || !reflect.DeepEqual(options.RequireHosts, []string{"mac"}) {
					t.Fatalf("options %+v", options)
				}
				return worktrees.BranchCleanupOutcome{Results: tc.results}, nil
			}
			var out bytes.Buffer
			err := retirementExecute(NewRetire(retirementRuntime(&root), deps), append(tc.args, "--peer-evidence=peer.json", "--require-host=mac"), &out, io.Discard)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("err %v out %q", err, out.String())
			}
		})
	}
	deps := retireDeps()
	deps.Run = notFound
	boom := errors.New("branch sweep failed")
	deps.Branch = func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error) {
		return worktrees.BranchCleanupOutcome{}, boom
	}
	if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), []string{"datatug-io-cutover"}, io.Discard, io.Discard); err != boom {
		t.Fatal(err)
	}
	deps.Branch = func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error) {
		return worktrees.BranchCleanupOutcome{Results: []worktrees.BranchCleanupResult{{Eligible: true}}}, nil
	}
	for _, args := range [][]string{{"datatug-io-cutover"}, {"datatug-io-cutover", "--format=json"}} {
		if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), args, &cwWtFailWriter{}, io.Discard); !errors.Is(err, errCwWtWrite) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), []string{"datatug-io-cutover", "--apply"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "coded 2: ") || !strings.Contains(err.Error(), "needs --repo") {
		t.Fatalf("a fleet-wide bare-branch apply must be refused: %v", err)
	}
	if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), []string{"datatug-io-cutover", "other-task"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "Retire a bare branch on its own") {
		t.Fatalf("a bare branch among several names must be named alone: %v", err)
	}
}

func TestFirstNonEmptyPicksTheFirstReason(t *testing.T) {
	t.Parallel()
	if firstNonEmpty("", "b", "c") != "b" || firstNonEmpty() != "" {
		t.Fatal("firstNonEmpty")
	}
}

func TestCleanCommandWarnsAboutEveryMalformedCandidate(t *testing.T) {
	t.Parallel()
	root := "root"
	outcome := worktrees.CleanOutcome{Totals: map[string]int{}, Worktrees: &worktrees.GCOutcome{Totals: map[string]int{}, Diagnostics: []worktrees.ListDiagnostic{{Task: "renamed", Path: "/w/renamed", Message: "repository name mismatch"}}}}
	var errOut bytes.Buffer
	if err := retirementExecute(NewClean(retirementRuntime(&root), cleanDeps(outcome, nil)), nil, io.Discard, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "warning: clean skipped malformed candidate in task renamed: /w/renamed: repository name mismatch") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if err := retirementExecute(NewClean(retirementRuntime(&root), cleanDeps(outcome, nil)), nil, io.Discard, &cwWtFailWriter{}); !errors.Is(err, errCwWtWrite) {
		t.Fatalf("a failing stderr must surface: %v", err)
	}
}
