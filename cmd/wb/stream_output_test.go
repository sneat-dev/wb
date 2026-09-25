package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

// pkp00FailingWriter fails its Nth Write call (1-indexed) and otherwise
// reports success. The stream output functions guard almost every
// fmt.Fprint* call with "if err != nil { return err }"; against a
// bytes.Buffer that arm never runs, so this is how the tests reach it and
// prove the guard actually stops output and propagates the failure instead
// of writing further lines.
type pkp00FailingWriter struct {
	calls  int
	failOn int
}

func (w *pkp00FailingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failOn {
		return 0, fmt.Errorf("pkp00 simulated write failure on call %d", w.calls)
	}
	return len(p), nil
}

func TestStreamStartOutputTextListsMembers(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	result := streams.StartResult{
		Stream: streams.Stream{Name: "demo", Members: []streams.Member{
			{Repository: "acme/app", Role: streams.RoleLibrary, Worktree: "/wt/app", PullRequest: 7},
			{Repository: "acme/lib", Role: streams.RoleConsumer, Worktree: "/wt/lib", PullRequestError: "no token"},
		}},
	}
	if err := streamStartOutput(command, "start", "text", result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"stream demo on " + streams.Branch("demo"), "acme/app", "#7", "acme/lib", "no draft PR: no token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in output, got %q", want, got)
		}
	}
}

func TestStreamEndOutputTextReportsMembersPullRequestsAndErrors(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	result := streams.EndResult{
		Stream:  "demo",
		Applied: false,
		Members: []streams.EndMemberResult{{
			Repository: "acme/app", Worktree: "/wt/app", WorktreeRemoved: true,
			DraftAction: "closed", Detail: "closed pr #3",
		}},
		AgentPullRequests: []streams.AgentPullRequestOutcome{{
			Repository: "acme/app", Number: 9, Action: "retargeted", Detail: "now targets main",
		}},
		Errors: []string{"could not delete remote branch"},
	}
	err := streamEndOutput(command, "text", result)
	if err == nil {
		t.Fatalf("want a findings error because result.Errors is non-empty")
	}
	got := out.String()
	for _, want := range []string{
		"would end stream demo", "acme/app", "worktree_removed=true", "draft=closed",
		"agent PR acme/app #9: retargeted", "! could not delete remote branch",
		"nothing was changed; re-run with --apply",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in output, got %q", want, got)
		}
	}
}

func TestStreamListOutputTextListsStreamsAndUnreadableEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := streams.OpenAt(root)
	if _, err := store.Create(streams.Stream{
		Name:    "demo",
		Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleLibrary}},
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if err := os.MkdirAll(store.Dir("broken"), 0o755); err != nil {
		t.Fatalf("mkdir broken stream dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("broken"), "stream.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write broken stream state: %v", err)
	}
	engine := &streams.Engine{Store: store}
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	if err := streamListOutput(command, "text", engine); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "demo") || !strings.Contains(got, "acme/app") {
		t.Fatalf("want the readable stream listed, got %q", got)
	}
	if !strings.Contains(got, "broken") || !strings.Contains(got, "unreadable:") {
		t.Fatalf("want the unreadable stream listed, got %q", got)
	}
}

// streamStatusOutput's text report has three "gap" sections that each print
// either a populated list or a "none" line. This fixture exercises the
// member-level missing-pull-request detail, blocked and recover branches,
// and the "none" branch of every gap.
func TestStreamStatusOutputTextReportsMissingPullRequestsAndEmptyGaps(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	status := streams.Status{
		Stream: "demo", Branch: "stream/demo",
		Members: []streams.MemberStatus{
			{Repository: "acme/app", Role: streams.RoleLibrary, Worktree: "/wt/app", PullRequestBlocked: "divergent shared branch"},
			{Repository: "acme/lib", Role: streams.RoleConsumer, Worktree: "/wt/lib", LastPublicationError: &streams.PublicationFailure{Detail: "push failed"}},
		},
		OpenAgentPullRequests: []streams.AgentPullRequest{{Repository: "acme/app", Number: 5, Title: "fix", Head: "stream/demo"}},
		Unknowns:              []string{"could not determine base branch"},
	}
	err := streamStatusOutput(command, "text", status)
	if err == nil {
		t.Fatalf("want a findings error because members are missing pull requests")
	}
	got := out.String()
	for _, want := range []string{
		"stream demo", "acme/app", "missing member pull requests:", "no draft pull request is recorded",
		"! acme/app:", "blocked: divergent shared branch", "! acme/lib:",
		"last publication attempt failed", "recover: wb stream join demo acme/lib",
		"linked consumers (gap 1):", "  none", "merged but untagged (gap 2):",
		"consumers behind (gap 3):", "open agent pull requests against the stream branch:",
		"acme/app #5 fix", "could not establish:", "? could not determine base branch",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in output, got %q", want, got)
		}
	}
}

// A write failure at any point in streamStartOutput's text report must stop
// output immediately and surface, rather than being swallowed while later
// lines keep writing.
func TestStreamStartOutputTextSurfacesWriteFailureAtEveryStatement(t *testing.T) {
	t.Parallel()
	result := streams.StartResult{
		Stream: streams.Stream{Name: "demo", Members: []streams.Member{
			{Repository: "acme/app", Role: streams.RoleLibrary, Worktree: "/wt/app", PullRequest: 7},
			{Repository: "acme/lib", Role: streams.RoleConsumer, Worktree: "/wt/lib", PullRequestError: "no token"},
		}},
	}
	for failOn := 1; failOn <= 3; failOn++ {
		writer := &pkp00FailingWriter{failOn: failOn}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := streamStartOutput(command, "start", "text", result)
		if err == nil || !strings.Contains(err.Error(), "pkp00 simulated write failure") {
			t.Fatalf("failOn=%d: want the simulated write failure surfaced, got %v", failOn, err)
		}
	}
}

// Same guarantee for streamEndOutput: every reported line (header, member,
// agent pull request, error and the final "nothing changed" notice) must
// stop and surface a write failure rather than continuing past it.
func TestStreamEndOutputTextSurfacesWriteFailureAtEveryStatement(t *testing.T) {
	t.Parallel()
	result := streams.EndResult{
		Stream:  "demo",
		Applied: false,
		Members: []streams.EndMemberResult{{
			Repository: "acme/app", Worktree: "/wt/app", WorktreeRemoved: true,
			DraftAction: "closed", Detail: "closed pr #3",
		}},
		AgentPullRequests: []streams.AgentPullRequestOutcome{{
			Repository: "acme/app", Number: 9, Action: "retargeted", Detail: "now targets main",
		}},
		Errors: []string{"could not delete remote branch"},
	}
	for failOn := 1; failOn <= 5; failOn++ {
		writer := &pkp00FailingWriter{failOn: failOn}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := streamEndOutput(command, "text", result)
		if err == nil || !strings.Contains(err.Error(), "pkp00 simulated write failure") {
			t.Fatalf("failOn=%d: want the simulated write failure surfaced, got %v", failOn, err)
		}
	}
}

// Same guarantee for streamListOutput's readable-stream row and
// unreadable-stream row.
func TestStreamListOutputTextSurfacesWriteFailureAtEveryStatement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := streams.OpenAt(root)
	if _, err := store.Create(streams.Stream{
		Name:    "demo",
		Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleLibrary}},
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if err := os.MkdirAll(store.Dir("broken"), 0o755); err != nil {
		t.Fatalf("mkdir broken stream dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("broken"), "stream.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write broken stream state: %v", err)
	}
	engine := &streams.Engine{Store: store}
	for failOn := 1; failOn <= 2; failOn++ {
		writer := &pkp00FailingWriter{failOn: failOn}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := streamListOutput(command, "text", engine)
		if err == nil || !strings.Contains(err.Error(), "pkp00 simulated write failure") {
			t.Fatalf("failOn=%d: want the simulated write failure surfaced, got %v", failOn, err)
		}
	}
}

// Every fmt.Fprint* call in streamStatusOutput's text report is guarded the
// same way; drive a write failure through each one across the "missing pull
// requests" section (blocked, last-publication-failure and recover lines)
// and the empty-gap branches.
func TestStreamStatusOutputTextSurfacesWriteFailureAtEveryStatement(t *testing.T) {
	t.Parallel()
	status := streams.Status{
		Stream: "demo", Branch: "stream/demo",
		Members: []streams.MemberStatus{
			{Repository: "acme/app", Role: streams.RoleLibrary, Worktree: "/wt/app", PullRequestBlocked: "divergent shared branch"},
			{Repository: "acme/lib", Role: streams.RoleConsumer, Worktree: "/wt/lib", LastPublicationError: &streams.PublicationFailure{Detail: "push failed"}},
		},
		OpenAgentPullRequests: []streams.AgentPullRequest{{Repository: "acme/app", Number: 5, Title: "fix", Head: "stream/demo"}},
		Unknowns:              []string{"could not determine base branch"},
	}
	for failOn := 1; failOn <= 19; failOn++ {
		writer := &pkp00FailingWriter{failOn: failOn}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := streamStatusOutput(command, "text", status)
		if err == nil || !strings.Contains(err.Error(), "pkp00 simulated write failure") {
			t.Fatalf("failOn=%d: want the simulated write failure surfaced, got %v", failOn, err)
		}
	}
}

// The populated-gap branches (linked consumers, merged-untagged, consumers
// behind) are only reached with a different fixture; drive a write failure
// through each of their lines too.
func TestStreamStatusOutputTextSurfacesWriteFailureInPopulatedGapSections(t *testing.T) {
	t.Parallel()
	status := streams.Status{
		Stream: "demo", Branch: "stream/demo",
		LinkedConsumers: []streams.LinkedConsumer{{Repository: "acme/app", Worktree: "/wt/app", Library: "acme/lib", Identity: "1.2.3"}},
		MergedUntagged:  &streams.MergedUntagged{Repository: "acme/lib", Base: "main", LatestTag: "v1.2.0", Commits: []string{"deadbeef"}},
		ConsumersBehind: []streams.ConsumerBehind{{Repository: "acme/app", Identity: "go", Manifest: "go.mod", Declared: "v1.0.0", Published: "v1.1.0"}},
	}
	for failOn := 1; failOn <= 7; failOn++ {
		writer := &pkp00FailingWriter{failOn: failOn}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := streamStatusOutput(command, "text", status)
		if err == nil || !strings.Contains(err.Error(), "pkp00 simulated write failure") {
			t.Fatalf("failOn=%d: want the simulated write failure surfaced, got %v", failOn, err)
		}
	}
}

// A second fixture exercises the populated branch of each of the three gap
// sections, which the empty-gap fixture above deliberately leaves untouched.
func TestStreamStatusOutputTextReportsPopulatedGaps(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	status := streams.Status{
		Stream: "demo", Branch: "stream/demo",
		LinkedConsumers: []streams.LinkedConsumer{{
			Repository: "acme/app", Worktree: "/wt/app", Library: "acme/lib",
			Identity: "1.2.3", PreviousVersion: "1.2.2", ContentHash: "abc123",
		}},
		MergedUntagged: &streams.MergedUntagged{Repository: "acme/lib", Base: "main", LatestTag: "v1.2.0", Commits: []string{"deadbeef"}},
		ConsumersBehind: []streams.ConsumerBehind{{
			Repository: "acme/app", Identity: "go", Manifest: "go.mod", Declared: "v1.0.0", Published: "v1.1.0",
		}},
	}
	if err := streamStatusOutput(command, "text", status); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"acme/app → acme/lib", "acme/lib: 1 commit(s) on main after v1.2.0",
		"acme/app declares go v1.0.0 in go.mod; published is v1.1.0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in output, got %q", want, got)
		}
	}
}
