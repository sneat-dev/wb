package worktreeclaims

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func TestClaimsIdentityStateAndEnv(t *testing.T) {
	t.Parallel()
	var state IdentityState
	values := map[string]string{EnvAgentRuntime: " codex ", EnvAgentID: " worker ", EnvAgentPID: "123", EnvAgentModel: " model ", EnvSessionID: " session "}
	env := IdentityFromEnv(func(key string) string { return values[key] })
	if !env.Declared() || env.PID != 123 || env.Agent() != "codex/worker" {
		t.Fatalf("env: %+v", env)
	}
	if got := state.CurrentIdentity(env); got != env {
		t.Fatalf("environment identity: %+v", got)
	}
	registered := AgentIdentity{Runtime: "registered", PID: 456, Registered: true, WBSessionID: "live"}
	state.SetSessionResolver(func() (AgentIdentity, bool) { return registered, true })
	if got := state.CurrentIdentity(env); got != registered {
		t.Fatalf("registered should win: %+v", got)
	}
	if got, ok := state.RegisteredIdentity(); !ok || got != registered {
		t.Fatalf("registered query: %+v %t", got, ok)
	}
	restore := state.SetMutationInitiator(" operator ")
	if state.MutationInitiator() != "operator" {
		t.Fatal("initiator not normalized")
	}
	restore()
	if state.MutationInitiator() != "" {
		t.Fatal("initiator not restored")
	}
	state.SetInvokedCommand("worktree create")
	if state.InvokedCommand() != "worktree create" {
		t.Fatal("command not retained")
	}
	if !strings.Contains(UndeclaredOwnerWarning("/worktree"), "/worktree") {
		t.Fatal("warning omitted path")
	}
}
func TestClaimsOwnerRepeatedCustodyAndWarnings(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	var mu sync.Mutex
	events := []worktreejournal.LocalWorkLogEvent{}
	warnings := &OwnerWarnings{}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := OwnerPorts{Version: func() string { return "wb-v" }, Now: func() time.Time { return now }, MutationInitiator: func() string { return "operator" }, Warnings: warnings,
		CurrentIdentity: func() AgentIdentity { return AgentIdentity{Runtime: "codex", PID: 123} }, InvokedCommand: func() string { return "set" },
		AppendEvent: func(_ string, event worktreejournal.LocalWorkLogEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event)
			return nil
		},
		ReadEvents: func(string) ([]worktreejournal.LocalWorkLogEvent, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]worktreejournal.LocalWorkLogEvent(nil), events...), nil
		},
		ProcessStatus: func(pid int) error {
			if pid == 123 {
				return nil
			}
			return errors.New("unknown")
		},
	}
	identity := AgentIdentity{Runtime: "codex", AgentID: "one", Model: "model", PID: 123}
	if err := p.RecordCustody(root, "effort", "create", identity); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordCustody(root, "", "set", identity); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("repeat custody wrote %d events", len(events))
	}
	next := identity
	next.Model = "other"
	if err := p.RecordCustody(root, "", "set", next); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Owner.Effort != "effort" {
		t.Fatalf("handover: %+v", events)
	}
	views, err := p.OwnerViews(root)
	if err != nil || len(views) != 2 || WorktreeOwnerState(views) != "active" {
		t.Fatalf("views: %+v %v", views, err)
	}
	state, agent, pid := p.DeclaredOwner(root)
	if state != OwnerLive || agent != "codex/one" || pid != 123 {
		t.Fatalf("declared owner: %s %s %d", state, agent, pid)
	}
	if err := p.RecordCustody(root, "", "set", AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	if got := warnings.TakeOwnerWarnings(); len(got) != 1 || got[0] != root {
		t.Fatalf("warnings: %v", got)
	}
	if got := warnings.TakeOwnerWarnings(); len(got) != 0 {
		t.Fatalf("warning replay: %v", got)
	}
	if ExtraString(map[string]any{"value": " padded "}, "value") != "padded" {
		t.Fatal("extra string")
	}
}
func TestClaimsHeartbeatSignalsAndDirectoryScope(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	journal := filepath.Join(root, ".wb", "local")
	if err := os.MkdirAll(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journal, "manifest.yaml"), []byte("manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(root, "README.md")
	if err := os.WriteFile(changed, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var heartbeat []byte
	p := HeartbeatPorts{OpenJournal: func(string, bool) (*os.File, error) { return os.Open(journal) }, ReadBytesAt: func(*os.File, string) ([]byte, error) { return heartbeat, nil }, WriteAtomicAt: func(_ *os.File, _ string, bytes []byte, _ os.FileMode) error {
		heartbeat = append([]byte(nil), bytes...)
		return nil
	}, Now: func() time.Time { return now }, PID: func() int { return 123 },
		GitRaw: func(context.Context, string, ...string) ([]byte, error) { return []byte(" M README.md\n"), nil }, Lstat: os.Lstat, Getwd: func() (string, error) { return filepath.Join(root, "nested"), nil }, Abs: filepath.Abs, Stat: os.Stat,
	}
	p.TouchHeartbeat(root, " status ")
	if got := p.HeartbeatAt(root); !got.Equal(now) {
		t.Fatalf("heartbeat: %v", got)
	}
	if got := p.NewestChangedFileTime(context.Background(), root); got.IsZero() {
		t.Fatal("changed file missing")
	}
	if got, err := p.GitRawOutput(context.Background(), root, "status"); err != nil || got != " M README.md\n" {
		t.Fatalf("raw git: %q %v", got, err)
	}
	if got, err := p.WorktreeRootOf(filepath.Join(root, "nested")); err != nil || got != root {
		t.Fatalf("root: %q %v", got, err)
	}
	p.TouchHeartbeatForCurrentDirectory("show")
	if got := p.LastActivity(context.Background(), ActivitySnapshot{WorktreeDir: root, LastCommit: now.Add(-time.Hour)}); got.Before(now.Add(-time.Minute)) {
		t.Fatalf("activity: %v", got)
	}
}
func TestClaimsPortableClaimIdentityAndRoutes(t *testing.T) {
	t.Parallel()
	result := CreationResult{Repository: "owner/repo", Branch: "feature", Base: "main", BaseSHA: strings.Repeat("a", 40)}
	first := WorkLogClaimID("effort", result)
	result.WorktreeDir = "/another/location"
	if got := WorkLogClaimID("effort", result); got != first || !ValidClaimID(got) {
		t.Fatalf("portable claim ID %q", got)
	}
	if got := WorkLogClaimID("other", result); got == first {
		t.Fatal("effort not bound")
	}
	claim := ClaimIdentity{EffortID: "effort", Repository: result.Repository, Branch: result.Branch, Base: result.Base, BaseSHA: result.BaseSHA}
	if got, err := ExpectedWorkLogClaimID(claim, nil, nil); err != nil || got != first {
		t.Fatalf("root claim: %q %v", got, err)
	}
	claim.ParentClaimID = first
	claim.AcquiredVia = "handoff"
	claim.AgentID = "next"
	claim.Version = 2
	claim.Model = "model"
	got, err := ExpectedWorkLogClaimID(claim, nil, nil)
	if err != nil || got == first {
		t.Fatalf("successor: %q %v", got, err)
	}
	claim.AcquiredVia = "external_handoff"
	if got, err := ExpectedWorkLogClaimID(claim, func() (string, error) { return "external", nil }, nil); err != nil || got != "external" {
		t.Fatalf("external: %q %v", got, err)
	}
	if err := ValidateNewExecutionIdentity(ClaimExecutionIdentity{Model: "model"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"ghp_abcdefghijklmnop", "token", "a@b"} {
		if ValidExecutionIdentifier(value, false) {
			t.Fatalf("accepted credential %q", value)
		}
	}
	if _, err := ExpectedWorkLogClaimID(ClaimIdentity{ParentClaimID: first, AcquiredVia: "bad"}, nil, nil); err == nil {
		t.Fatal("accepted bad acquisition")
	}
}

func TestClaimsHeartbeatFaultPorts(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	open := func(string, bool) (*os.File, error) { return os.Open(root) }
	p := HeartbeatPorts{OpenJournal: open, ReadBytesAt: func(*os.File, string) ([]byte, error) { return nil, os.ErrNotExist }, WriteAtomicAt: func(*os.File, string, []byte, os.FileMode) error { return nil }, Now: func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, PID: func() int { return 1 },
		GitRaw: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("git failed") }, Lstat: os.Lstat, Getwd: func() (string, error) { return "", errors.New("cwd") }, Abs: filepath.Abs, Stat: os.Stat}
	p.TouchHeartbeat(root, "command") // an out-of-range timestamp cannot be encoded
	if got := p.NewestChangedFileTime(context.Background(), root); !got.IsZero() {
		t.Fatalf("git error activity: %v", got)
	}
	if _, err := p.GitRawOutput(context.Background(), root, "status"); err == nil || !strings.Contains(err.Error(), "git status") {
		t.Fatalf("raw git error: %v", err)
	}
	p.TouchHeartbeatForCurrentDirectory("command")
	p.Getwd = func() (string, error) { return root, nil }
	p.Abs = func(string) (string, error) { return "", errors.New("abs") }
	if _, err := p.WorktreeRootOf(root); err == nil {
		t.Fatal("accepted failed absolute path")
	}
	p.TouchHeartbeatForCurrentDirectory("command")
	p.Abs = filepath.Abs
	p.Rewind = func(*os.File) error { return errors.New("rewind") }
	if got := p.NewestWorkLogEventTime(root); !got.IsZero() {
		t.Fatal(got)
	}
	p.Rewind = nil
	p.ReadDir = func(*os.File) ([]os.DirEntry, error) { return nil, errors.New("read dir") }
	if got := p.NewestWorkLogEventTime(root); !got.IsZero() {
		t.Fatal(got)
	}
	p.ReadDir = nil
	if err := os.WriteFile(filepath.Join(root, "entry"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	p.EntryInfo = func(os.DirEntry) (os.FileInfo, error) { return nil, errors.New("info") }
	if got := p.NewestWorkLogEventTime(root); !got.IsZero() {
		t.Fatal(got)
	}
	p.OpenJournal = func(string, bool) (*os.File, error) { return nil, os.ErrNotExist }
	if got := p.NewestWorkLogEventTime(root); !got.IsZero() {
		t.Fatal(got)
	}
}

func TestClaimsChangedFilePorcelainCases(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	rename := filepath.Join(root, "new.txt")
	if err := os.WriteFile(rename, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	p := HeartbeatPorts{GitRaw: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" M .git/internal\n M old.txt -> new.txt\n M missing.txt\n M \n"), nil
	}, Lstat: os.Lstat}
	if got := p.NewestChangedFileTime(context.Background(), root); got.IsZero() {
		t.Fatal("rename destination did not count")
	}
}

func TestClaimsOwnerFaultsAndLegacyInspection(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	warnings := &OwnerWarnings{}
	warnings.NoteUndeclared(root)
	warnings.NoteUndeclared(root)
	if got := warnings.TakeOwnerWarnings(); len(got) != 1 {
		t.Fatalf("duplicate warning: %v", got)
	}
	p := OwnerPorts{Warnings: warnings, Version: func() string { return "v" }, Now: time.Now, MutationInitiator: func() string { return "" }, ProcessStatus: func(int) error { return errors.New("unknown") },
		ReadEvents:    func(string) ([]worktreejournal.LocalWorkLogEvent, error) { return nil, errors.New("invalid events") },
		ActiveHandoff: func(string, string) (LegacyHandoff, bool) { return LegacyHandoff{}, false },
	}
	if err := p.RecordCustody(root, "", "", AgentIdentity{}); err == nil {
		t.Fatal("custody ignored reader error")
	}
	if got := p.OwnerPIDStatus(123); got != "unknown" {
		t.Fatalf("unknown PID: %s", got)
	}
	if _, err := p.LifecycleOwnerViews("home", root); err == nil {
		t.Fatal("legacy inspection without handoff")
	}
	evidence := LegacyHandoff{HandoffID: "handoff", MemberID: "member", Repository: "repo", PredecessorWBSessionID: "before", AgentID: "after", SourceWorkLogReference: "source", TargetWorkLogReference: "target", RequestDigest: "digest"}
	p.ActiveHandoff = func(string, string) (LegacyHandoff, bool) { return evidence, true }
	p.ExpectedCompletionID = func(member, digest string) string {
		if member != "member" || digest != "digest" {
			t.Fatalf("wrong lineage: %s %s", member, digest)
		}
		return "completed"
	}
	event := worktreejournal.LocalWorkLogEvent{ID: "completed", Extra: map[string]any{"resume_id": "handoff", "member_id": "member", "repository": "repo", "predecessor_wb_session_id": "before", "successor_wb_session_id": "after", "source_work_log_reference": "source", "target_work_log_reference": "target"}}
	p.ReadForInspection = func(_ string, accept func(worktreejournal.LocalWorkLogEvent) bool) ([]worktreejournal.LocalWorkLogEvent, bool, error) {
		if accept(worktreejournal.LocalWorkLogEvent{ID: "wrong"}) {
			t.Fatal("accepted wrong ID")
		}
		if !accept(event) {
			t.Fatal("rejected exact lineage")
		}
		return []worktreejournal.LocalWorkLogEvent{event}, true, nil
	}
	if views, err := p.LifecycleOwnerViews("home", root); err != nil || len(views) != 0 {
		t.Fatalf("legacy inspection: %+v %v", views, err)
	}
	p.ReadForInspection = func(string, func(worktreejournal.LocalWorkLogEvent) bool) ([]worktreejournal.LocalWorkLogEvent, bool, error) {
		return nil, false, errors.New("inspection")
	}
	if _, err := p.LifecycleOwnerViews("home", root); err == nil {
		t.Fatal("inspection error lost")
	}
	p.ReadEvents = func(string) ([]worktreejournal.LocalWorkLogEvent, error) {
		owner := worktreejournal.OwnerRegistration{Agent: "agent", PID: 123}
		return []worktreejournal.LocalWorkLogEvent{{Owner: &owner}}, nil
	}
	state, agent, pid := p.DeclaredOwner(root)
	if state != OwnerUnstated || agent != "agent" || pid != 123 {
		t.Fatalf("unknown PID owner: %s %s %d", state, agent, pid)
	}
}
