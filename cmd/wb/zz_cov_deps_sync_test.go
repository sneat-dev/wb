package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func TestCwDepsSyncOwnersRestrictionAndDiscovery(t *testing.T) {
	// An explicit -o restriction short-circuits org discovery but still
	// requires working authentication.
	owners, err := resolveSyncOwners([]string{"only-org"}, func() (string, error) { return "user", nil },
		func() ([]string, error) { return nil, errors.New("must not be called") })
	if err != nil || len(owners) != 1 || owners[0] != "only-org" {
		t.Fatalf("explicit owners = %v, %v", owners, err)
	}
	// No restriction: the authenticated user plus every member org.
	owners, err = resolveSyncOwners(nil, func() (string, error) { return "user", nil },
		func() ([]string, error) { return []string{"org-a", "org-b"}, nil })
	if err != nil || len(owners) != 3 || owners[0] != "user" {
		t.Fatalf("discovered owners = %v, %v", owners, err)
	}
	// Authentication failure is a hard error, never "no owners".
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "", errors.New("gh missing") },
		func() ([]string, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "GitHub authentication failed") {
		t.Fatalf("auth failure = %v", err)
	}
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "", nil },
		func() ([]string, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "authenticated user is empty") {
		t.Fatalf("empty user = %v", err)
	}
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "user", nil },
		func() ([]string, error) { return nil, errors.New("boom") }); err == nil || !strings.Contains(err.Error(), "could not list GitHub organizations") {
		t.Fatalf("org discovery failure = %v", err)
	}

	// syncOwners is the discover-backed wrapper.
	cwCovFakeGH(t, "cwcov-user", []string{"cwcov-org"}, `[]`)
	owners, err = syncOwners(nil)
	if err != nil || len(owners) != 2 {
		t.Fatalf("syncOwners = %v, %v", owners, err)
	}
	if only, err := syncOwners([]string{"explicit"}); err != nil || len(only) != 1 || only[0] != "explicit" {
		t.Fatalf("syncOwners(-o) = %v, %v", only, err)
	}
}

func TestCwDepsRequestedSyncOwnersReadsBothSpellings(t *testing.T) {
	inv := &invocation{}
	syncCommand := newSyncCmd(inv)
	// The command-local --org is the first source.
	if got := requestedSyncOwners(inv, syncCommand, []string{"local-org"}); len(got) != 1 || got[0] != "local-org" {
		t.Fatalf("command-local owners = %v", got)
	}
	// The root persistent --org appends the root's own selection, because
	// Cobra advertises both spellings with identical semantics.
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional GitHub owner to query")
	syncCommand = newSyncCmd(inv)
	root.AddCommand(syncCommand)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	inv.extraOrgs = []string{"root-org"}
	got := requestedSyncOwners(inv, syncCommand, []string{"local-org"})
	if len(got) != 2 || got[0] != "local-org" || got[1] != "root-org" {
		t.Fatalf("root+local owners = %v", got)
	}
	// Without the root flag changed, the root list is not consulted.
	plainInv := &invocation{}
	plainRoot := &cobra.Command{Use: "wb"}
	plainRoot.PersistentFlags().StringArray("org", nil, "additional GitHub owner to query")
	plainSync := newSyncCmd(plainInv)
	plainRoot.AddCommand(plainSync)
	if got := requestedSyncOwners(plainInv, plainSync, nil); len(got) != 0 {
		t.Fatalf("unchanged root org leaked owners: %v", got)
	}
}

func TestCwDepsSyncLifecycleEventsSelectsMovedCheckouts(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "clone"}, Status: fleetsync.Cloned, HeadSHA: "aaa", BeforeHeadSHA: ""},
		{Repo: discover.Repo{Org: "acme", Name: "pull"}, Status: fleetsync.NoOp, Updated: true, HeadSHA: "bbb", BeforeHeadSHA: "aaa"},
		{Repo: discover.Repo{Org: "acme", Name: "unchanged"}, Status: fleetsync.NoOp},
		{Repo: discover.Repo{Org: "acme", Name: "cloned-no-sha"}, Status: fleetsync.Cloned},
	}
	events := syncLifecycleEvents(results)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per moved checkout", events)
	}
	if events[0].Name != lifecyclehooks.EventCheckoutUpdated ||
		events[0].Repository != "github.com/acme/clone" || events[0].Cause != "sync-clone" {
		t.Errorf("clone event = %+v", events[0])
	}
	if events[1].Cause != "sync-pull" || events[1].OldSHA != "aaa" || events[1].NewSHA != "bbb" {
		t.Errorf("pull event = %+v", events[1])
	}
	if events := syncLifecycleEvents(nil); len(events) != 0 {
		t.Errorf("no results produced events: %+v", events)
	}
}

func TestCwDepsFinishSyncLifecycleHooksReportsWarnings(t *testing.T) {
	var errOut bytes.Buffer
	// No events at all: the dispatcher must not even be called.
	called := false
	code := finishSyncLifecycleHooks(context.Background(), nil, func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		called = true
		return lifecyclehooks.Report{}, nil
	}, &errOut)
	if code != 0 || called {
		t.Fatalf("empty results: code=%d called=%t", code, called)
	}
	// Warnings and a dispatch error are both surfaced as warnings, and a hook
	// problem never changes sync's own exit code.
	results := []fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "app"}, Status: fleetsync.Cloned, HeadSHA: "sha"}}
	code = finishSyncLifecycleHooks(context.Background(), results, func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		return lifecyclehooks.Report{Warnings: []string{"hook env is incomplete"}}, errors.New("dispatcher unavailable")
	}, &errOut)
	if code != 0 {
		t.Fatalf("hook failure changed the exit code: %d", code)
	}
	for _, want := range []string{"warning: hook env is incomplete", "warning: lifecycle hooks were not dispatched: dispatcher unavailable"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
}

func TestCwDepsSyncReportWriterPicksTheVisibleStream(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := syncReportWriter(false, &out, &errOut); got != io.Writer(&out) {
		t.Error("a non-interactive run must keep its report on stdout")
	}
	if got := syncReportWriter(true, &out, &errOut); got != io.Writer(&errOut) {
		t.Error("an interactive run must move its report to stderr after the TUI restores the terminal")
	}
}

func TestCwDepsCommitCountPluralizes(t *testing.T) {
	if got := commitCount(1); got != "1 commit" {
		t.Errorf("commitCount(1) = %q", got)
	}
	if got := commitCount(0); got != "0 commits" {
		t.Errorf("commitCount(0) = %q", got)
	}
	if got := commitCount(3); got != "3 commits" {
		t.Errorf("commitCount(3) = %q", got)
	}
}

func TestCwDepsSyncSummaryStylesRenderOnlyWhenEnabled(t *testing.T) {
	plain := newSyncSummaryStyles(false)
	if got := plain.render(plain.title, "text"); got != "text" {
		t.Errorf("disabled style rendered %q", got)
	}
	styled := newSyncSummaryStyles(true)
	for _, section := range []fleetsync.SummarySection{fleetsync.SummaryAttention, fleetsync.SummaryErrors, fleetsync.SummaryFinalOutcomes, fleetsync.SummaryPullActions} {
		heading := styled.sectionHeading(section)
		if !strings.Contains(heading, string(section)) && string(section) != "" {
			t.Errorf("sectionHeading(%q) = %q", section, heading)
		}
	}
}

func TestCwDepsPrintSyncSummaryRendersEveryAttentionShape(t *testing.T) {
	transferred := discover.Repo{Org: "acme", Name: "moved", TransferFrom: "old-org"}
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "cloned"}, Status: fleetsync.Cloned},
		{Repo: discover.Repo{Org: "acme", Name: "diverged"}, Status: fleetsync.Diverged,
			Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 1, Behind: 2}},
		{Repo: discover.Repo{Org: "acme", Name: "noupstream"}, Status: fleetsync.NoUpstream,
			Tracking: gitops.TrackingState{Branch: "main"}},
		{Repo: discover.Repo{Org: "acme", Name: "unpushed"}, Status: fleetsync.Unpushed,
			Detail: gitops.RepoStatus{UnpushedBranches: []gitops.UnpushedBranch{
				{Branch: "main", Commits: []string{"aaa work"}},
				{Branch: "side", Worktree: "/tmp/other-worktree", Commits: []string{"bbb work", "ccc work"}},
			}}},
		{Repo: discover.Repo{Org: "acme", Name: "unpushed-nobranch"}, Status: fleetsync.Unpushed,
			Detail: gitops.RepoStatus{Unpushed: []string{"ddd work"}}},
		{Repo: discover.Repo{Org: "acme", Name: "archived-unlandable"}, Status: fleetsync.ArchivedUnlandable,
			Detail: gitops.RepoStatus{Modified: []string{"a.go"}}},
		{Repo: transferred, Status: fleetsync.RepositoryTransferRequired, Reason: "the repository was renamed"},
		{Repo: discover.Repo{Org: "acme", Name: "archived"}, Status: fleetsync.NoOp, Archived: true, ArchivedNotPruned: true},
		{Repo: discover.Repo{Org: "acme", Name: "boom"}, Status: fleetsync.Failed, Err: errors.New("pull refused")},
	}

	var plain bytes.Buffer
	printSyncSummary(&plain, results, false, false)
	text := plain.String()
	for _, want := range []string{
		"Summary", "diverged", "not pulled", "noupstream", "not yet pushed",
		"1 commit", "2 commits", "🌳 side", "archived, so its", "old-org → acme/moved",
		"archived; not pruned", "boom", "pull refused",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plain summary missing %q:\n%s", want, text)
		}
	}

	// Styled output goes through lipgloss but must still carry the same
	// repository names and the errors; an empty errors section is omitted.
	var styled bytes.Buffer
	printSyncSummary(&styled, results, false, true)
	if !strings.Contains(styled.String(), "acme/boom") {
		t.Errorf("styled summary lost the failure section:\n%s", styled.String())
	}
	var noResults bytes.Buffer
	printSyncSummary(&noResults, nil, false, false)
	if !strings.Contains(noResults.String(), "Summary") || strings.Contains(noResults.String(), "Errors") {
		t.Errorf("empty summary rendered an errors section:\n%s", noResults.String())
	}
}

func TestCwDepsPrintArchivedPruningNamesEveryOutcome(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "deleted"}, Archived: true, Status: fleetsync.RemovedArchived,
			Reason: "confirmed archived and clean", ReceiptPath: "/tmp/receipt.json"},
		{Repo: discover.Repo{Org: "acme", Name: "deleted-noreceipt"}, Archived: true, Status: fleetsync.RemovedArchived,
			Reason: "confirmed archived and clean"},
		{Repo: discover.Repo{Org: "acme", Name: "kept"}, Archived: true, Status: fleetsync.KeptArchived, Reason: "unpushed commits"},
		{Repo: discover.Repo{Org: "acme", Name: "unlandable"}, Archived: true, Status: fleetsync.ArchivedUnlandable, Reason: "cannot push"},
		{Repo: discover.Repo{Org: "acme", Name: "absent"}, Archived: true, Status: fleetsync.AbsentArchived},
		{Repo: discover.Repo{Org: "acme", Name: "failed"}, Archived: true, Status: fleetsync.Failed, Err: errors.New("delete refused")},
		{Repo: discover.Repo{Org: "acme", Name: "live"}, Archived: false, Status: fleetsync.Cloned},
	}
	var out bytes.Buffer
	printArchivedPruning(&out, results)
	text := out.String()
	for _, want := range []string{
		"Archived (--prune-archived)",
		"deleted      acme/deleted", "receipt: /tmp/receipt.json",
		"deleted      acme/deleted-noreceipt",
		"skipped      acme/kept", "skipped      acme/unlandable",
		"absent       acme/absent", "failed       acme/failed", "delete refused",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("archived pruning report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "acme/live") {
		t.Errorf("a live repository appeared in the archived section:\n%s", text)
	}
	// Nothing archived means no section at all.
	var empty bytes.Buffer
	printArchivedPruning(&empty, []fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "live"}, Status: fleetsync.Cloned}})
	if empty.Len() != 0 {
		t.Errorf("empty archived section wrote %q", empty.String())
	}
}

// Repo2TransferFrom was avoided; see the transferred fixture above.

func TestCwDepsPrintSyncSummaryWithPruningSection(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "archived"}, Archived: true, Status: fleetsync.RemovedArchived,
			Reason: "clean", ReceiptPath: "/tmp/receipt.json"},
	}
	var out bytes.Buffer
	printSyncSummary(&out, results, true, false)
	if !strings.Contains(out.String(), "Archived (--prune-archived)") {
		t.Errorf("prune-archived summary did not render the archived section:\n%s", out.String())
	}
}

// TestCwDepsFinishSyncReportsIssuesAndPublishIntent covers the exit-code
// mapping and the dry-run publish refusal without touching the network.
func TestCwDepsFinishSyncReportsIssuesAndPublishIntent(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	projects := t.TempDir()
	meta := fleetsync.RunMeta{StartedAt: time.Now().UTC(), ProjectsRoot: projects, Scanned: 1}

	// A failed repository is a findings exit even when every other result
	// succeeded.
	var out, errOut bytes.Buffer
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "ok"}, Status: fleetsync.NoOp},
		{Repo: discover.Repo{Org: "acme", Name: "bad"}, Status: fleetsync.Failed, Err: errors.New("pull refused")},
	}
	if code := finishSync(&invocation{}, meta, results, false, true, remoteDeps{}, projects, "", 1, &out, &errOut); code != 1 {
		t.Fatalf("finishSync with a failed repository = %d, want 1", code)
	}

	// A dry-run --publish states that nothing was published rather than
	// publishing an outcome that never happened.
	out.Reset()
	errOut.Reset()
	clean := []fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "ok"}, Status: fleetsync.NoOp}}
	if code := finishSync(&invocation{}, meta, clean, true, true, remoteDeps{}, projects, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("dry-run publish finishSync = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "dry-run: skipping remote publish") {
		t.Errorf("dry-run publish did not say it skipped: %q", out.String())
	}
}

// TestCwDepsRunSyncPlainSyncsLocalClonesInParallel drives the non-TUI worker
// pool directly against scratch clones: dry-run keeps it read-only.
func TestCwDepsRunSyncPlainSyncsLocalClonesInParallel(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/one", "acme/two", "beta/three")
	repos, err := discover.ScanLocal(projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("scan found %d repositories", len(repos))
	}
	results := runSyncPlain(context.Background(), repos, projects, 2, true, false)
	if len(results) != 3 {
		t.Fatalf("results = %+v, want one per repository", results)
	}
	for _, result := range results {
		if result.Status == fleetsync.Failed {
			t.Errorf("%s failed unexpectedly: %v", result.Repo.Slug(), result.Err)
		}
	}
}

// TestCwDepsRunSyncReportsFleetState covers the whole runSync pipeline with a
// hermetic gh: a dry run over local clones reports and exits without mutating.
func TestCwDepsRunSyncReportsFleetState(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app", "acme/other")
	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)

	var out, errOut bytes.Buffer
	code := runSync(context.Background(), &invocation{nonInteractive: true}, projects, "", []string{"cwcov-user", "acme"}, 2, true, false, false,
		remoteDeps{}, &out, &errOut)
	if code != 0 {
		t.Fatalf("dry-run sync exit = %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	// Both local clones are discovered and classified; the summary counts
	// them rather than listing every repository.
	for _, want := range []string{"Summary", "Final outcomes", "Not owned               2", "Sync issues: 0 records"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run sync report missing %q:\n%s", want, out.String())
		}
	}
}

// TestCwDepsRunSyncWithoutRepositoriesSaysSo keeps the empty-fleet path
// explicit: sync reports it rather than printing an empty summary.
func TestCwDepsRunSyncWithoutRepositoriesSaysSo(t *testing.T) {
	projects := t.TempDir()
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	var out, errOut bytes.Buffer
	if code := runSync(context.Background(), &invocation{nonInteractive: true}, projects, "", []string{"cwcov-user"}, 1, true, false, false,
		remoteDeps{}, &out, &errOut); code != 0 {
		t.Fatalf("empty sync exit = %d\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no repos found") {
		t.Errorf("empty fleet report = %q", out.String())
	}
}

// TestCwDepsSyncCommandDispatchesToRunSyncInProcess proves that "wb sync"
// reaches requestedSyncOwners and runSync through the real command tree,
// threading its invocation through both, not just through direct unit calls.
func TestCwDepsSyncCommandDispatchesToRunSyncInProcess(t *testing.T) {
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	projectsRoot := t.TempDir()
	stdout, _, err := cwCovExec(t, projectsRoot, func() *cobra.Command {
		return newSyncCmd(&invocation{projectsRoot: projectsRoot, nonInteractive: true})
	},
		"--dry-run")
	if err != nil {
		t.Fatalf("wb sync --dry-run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "no repos found") {
		t.Errorf("empty fleet report = %q", stdout)
	}
}

// TestCwDepsRunSyncRefusesBrokenAuthentication proves an authentication
// failure is a finding, not a silently unmanaged-but-successful sync.
func TestCwDepsRunSyncRefusesBrokenAuthentication(t *testing.T) {
	binDir := t.TempDir()
	if err := cwCovWriteExecutable(filepath.Join(binDir, "gh"), "#!/bin/sh\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	var out, errOut bytes.Buffer
	code := runSync(context.Background(), &invocation{nonInteractive: true}, t.TempDir(), "", nil, 1, true, false, false, remoteDeps{}, &out, &errOut)
	if code != exitFindings {
		t.Fatalf("broken auth sync exit = %d, want findings\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "Re-authenticate with: gh auth login") {
		t.Errorf("broken auth stderr = %q", errOut.String())
	}
}

// TestCwDepsRunQueueSummaryKeepsTelemetryPrivate covers the privacy-safe label
// derivation used by the CPU queue receipts.
func TestCwDepsRunQueueSummaryKeepsTelemetryPrivate(t *testing.T) {
	if got := runQueueSummary(nil); got != "unknown" {
		t.Errorf("runQueueSummary(nil) = %q", got)
	}
	if got := runQueueSummary([]string{"/usr/local/bin/go", "test", "./..."}); got != "go test" {
		t.Errorf("runQueueSummary(go test) = %q", got)
	}
	if got := runQueueSummary([]string{"go", "-race", "test"}); got != "go test" {
		t.Errorf("runQueueSummary(skipping flags) = %q", got)
	}
	if got := runQueueSummary([]string{"/usr/bin/make"}); got != "make" {
		t.Errorf("runQueueSummary(makeless) = %q", got)
	}
}

// TestCwDepsPrintRunQueueRendersRunningAndWaitingSeats covers both queue
// listing shapes in text and JSON.
func TestCwDepsPrintRunQueueRendersRunningAndWaitingSeats(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	if err := printRunQueue(&invocation{}, command, false); err != nil {
		t.Fatalf("printRunQueue text: %v", err)
	}
	for _, want := range []string{"WB CPU queue", "running (", "waiting ("} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("queue listing missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	command = cwDepsNewOutCommand(&out)
	if err := printRunQueue(&invocation{}, command, true); err != nil {
		t.Fatalf("printRunQueue json: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("queue JSON: %v\n%s", err, out.String())
	}
}

// cwCovWriteExecutable writes a small executable script for PATH scoping.
func cwCovWriteExecutable(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return testenv.WriteExecutableFile(path, []byte(body), 0o755)
}
