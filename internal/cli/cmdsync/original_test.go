package cmdsync

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/spf13/cobra"
)

func TestPrintSyncSummaryReportsFreshRemoteUpdates(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Status: fleetsync.Unpushed, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullPlanned: true},
	}, false, false)
	for _, want := range []string{
		"Final outcomes",
		"Pulled                  3",
		"Pull actions",
		"Pull planned            1",
		"Pull attempted          3",
		"Pull succeeded          3",
		"Updated from remote     2",
		"Already current         1",
		"Attention",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "Failures") || strings.Contains(out.String(), "Errors") {
		t.Fatalf("zero-error summary contains a failure section: %q", out.String())
	}
	sections := []string{"Final outcomes", "Pull actions", "Attention"}
	previous := -1
	for _, section := range sections {
		index := strings.Index(out.String(), section)
		if index <= previous {
			t.Fatalf("summary sections are not ordered %v: %q", sections, out.String())
		}
		previous = index
	}
}
func TestPrintSyncSummaryKeepsAttentionAboveFailuresAndNamesWorktree(t *testing.T) {
	t.Parallel()
	commits := make([]string, 22)
	for i := range commits {
		commits[i] = fmt.Sprintf("%07d commit", i)
	}
	attention := fleetsync.Result{
		Repo: discover.Repo{
			Org:  "openvaultdb",
			Name: "openvaultdb-go",
			Path: "/projects/openvaultdb/openvaultdb-go",
		},
		Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{
			Unpushed: commits,
			UnpushedBranches: []gitops.UnpushedBranch{{
				Branch:   "layered-acl-query",
				Worktree: "/projects/openvaultdb/openvaultdb-go/.worktrees/layered-acl-query",
				Commits:  commits,
			}},
		},
	}
	failure := fleetsync.Result{
		Repo:   discover.Repo{Org: "acme", Name: "broken"},
		Status: fleetsync.Failed,
		Err:    errors.New("network failed"),
	}

	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{failure, attention}, false, false)
	got := out.String()
	wantAttention := "    ! openvaultdb/openvaultdb-go 🌳 layered-acl-query — 22 commits not yet pushed"
	if !strings.Contains(got, wantAttention) {
		t.Fatalf("summary does not attribute unpushed commits to their worktree:\n%s", got)
	}
	attentionIndex := strings.Index(got, wantAttention)
	failuresIndex := strings.Index(got, "\nFailures\n")
	errorIndex := strings.Index(got, "    ✗ acme/broken — network failed")
	if attentionIndex < 0 || failuresIndex <= attentionIndex || errorIndex <= failuresIndex {
		t.Fatalf("attention and failures are not rendered beneath their own sections:\n%s", got)
	}
}
func TestSyncSummaryStylesColorInteractiveWarnings(t *testing.T) {
	t.Parallel()
	styles := newSyncSummaryStyles(true)
	var out bytes.Buffer
	writeSyncAttention(&out, styles, fleetsync.Result{
		Repo:   discover.Repo{Org: "acme", Name: "app"},
		Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{
			Unpushed: []string{"abc1234 work"},
			UnpushedBranches: []gitops.UnpushedBranch{{
				Branch:   "feature",
				Worktree: "/projects/acme/app/.worktrees/feature",
				Commits:  []string{"abc1234 work"},
			}},
		},
	})
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("styled attention line has no ANSI styling: %q", out.String())
	}
}

func TestCwDepsRequestedSyncOwnersReadsBothSpellings(t *testing.T) {
	t.Parallel()
	inv := &shared.Flags{}
	syncCommand := New(syncRuntime(inv), nil)
	// The command-local --org is the first source.
	if got := requestedSyncOwners(inv.ExtraOrgs, syncCommand, []string{"local-org"}); len(got) != 1 || got[0] != "local-org" {
		t.Fatalf("command-local owners = %v", got)
	}
	// The root persistent --org appends the root's own selection, because
	// Cobra advertises both spellings with identical semantics.
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringArray("org", nil, "additional GitHub owner to query")
	syncCommand = New(syncRuntime(inv), nil)
	root.AddCommand(syncCommand)
	if err := root.PersistentFlags().Set("org", "root-org"); err != nil {
		t.Fatal(err)
	}
	inv.ExtraOrgs = []string{"root-org"}
	got := requestedSyncOwners(inv.ExtraOrgs, syncCommand, []string{"local-org"})
	if len(got) != 2 || got[0] != "local-org" || got[1] != "root-org" {
		t.Fatalf("root+local owners = %v", got)
	}
	// Without the root flag changed, the root list is not consulted.
	plainInv := &shared.Flags{}
	plainRoot := &cobra.Command{Use: "wb"}
	plainRoot.PersistentFlags().StringArray("org", nil, "additional GitHub owner to query")
	plainSync := New(syncRuntime(plainInv), nil)
	plainRoot.AddCommand(plainSync)
	if got := requestedSyncOwners(plainInv.ExtraOrgs, plainSync, nil); len(got) != 0 {
		t.Fatalf("unchanged root org leaked owners: %v", got)
	}
}

func TestCwDepsCommitCountPluralizes(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
func TestCwDepsPrintSyncSummaryWithPruningSection(t *testing.T) {
	t.Parallel()
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
func syncRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return fmt.Errorf("exit %d: %s", code, message) }}
}
