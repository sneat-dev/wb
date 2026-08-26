package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSessionParkCapturesAllOwnedWorktrees(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	results := []worktrees.ListResult{{WorktreeDir: "/tmp/a", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(11, 0)}}}}, {WorktreeDir: "/tmp/b", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(12, 0)}}}}, {WorktreeDir: "/tmp/old", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(9, 0)}}}}}
	count := 0
	for _, result := range results {
		if ownedBySession(result, source) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("owned worktrees = %d, want 2", count)
	}
}

func TestSessionParkDoesNotTreatDifferentPIDAsOwned(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/other", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 42, At: time.Unix(11, 0)}}}}
	if ownedBySession(result, source) {
		t.Fatal("different session worktree attributed")
	}
}

func TestSessionParkPublicOutputDoesNotContainContinuation(t *testing.T) {
	secret := "private continuation must never be printed"
	raw, err := json.Marshal(sessionParkOutput{
		ParkedSessionID: "park-test", Status: string(sessionpark.StatusParked),
		Source: session.Record{WBSessionID: "wbs-source"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "continuation") {
		t.Fatalf("public park output disclosed private continuation: %s", raw)
	}
}

func TestSessionParkCommandKeepsPrivateContextOutOfPublicAndWorkLogSurfaces(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	dir, err := sessionDir()
	if err != nil {
		t.Fatal(err)
	}
	source, err := session.Register(dir, session.Record{PID: os.Getpid(), WBSessionID: "wbs-private-park-source", Machine: "test-machine", Runtime: "codex", StartedAt: time.Now().UTC().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	secret := "PRIVATE-PARK-CONTEXT-7bff4f8c"
	workLogProbe := filepath.Join(home, "worklogs", "privacy-probe", "events.ndjson")
	if err := os.MkdirAll(filepath.Dir(workLogProbe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workLogProbe, []byte("existing custody evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workLogBefore, err := os.ReadFile(workLogProbe)
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(t.TempDir(), "continuation.md")
	if err := os.WriteFile(contextPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	arguments := []string{"--context-file", contextPath, "--format", "json"}
	command := newSessionParkCmd()
	command.SetArgs(arguments)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for surface, value := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "argv": strings.Join(arguments, "\x00")} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s disclosed private continuation", surface)
		}
	}
	var output sessionParkOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	state, err := store.Load(output.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Bundle.Source.WBSessionID != source.WBSessionID || state.Bundle.Continuation != secret {
		t.Fatalf("private source continuation was not durably preserved: %#v", state.Bundle)
	}
	workLogs := filepath.Join(home, "worklogs")
	_ = filepath.Walk(workLogs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("private continuation leaked into Work Log %s", path)
		}
		return nil
	})
	workLogAfter, err := os.ReadFile(workLogProbe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(workLogBefore, workLogAfter) {
		t.Fatal("session park changed existing Work Log evidence while storing private continuation")
	}
}

func TestSessionParkCrashRetryRefusesChangedImmutableInputsBeforeLifecycleMarking(t *testing.T) {
	for _, test := range []struct {
		name                string
		storedContinuation  string
		currentContinuation string
		storedMembers       []sessionpark.Worktree
		currentlyCaptured   []sessionpark.Worktree
	}{
		{name: "continuation changed", storedContinuation: "original continuation", currentContinuation: "changed continuation"},
		{name: "member evidence changed", storedContinuation: "same continuation", currentContinuation: "same continuation",
			storedMembers: []sessionpark.Worktree{{
				Repository: "acme/app", WorktreeDir: "/tmp/original-park-member", Branch: "feature/park",
				Head: strings.Repeat("a", 40),
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousProjectsRoot := projectsRoot
			projectsRoot = t.TempDir()
			t.Cleanup(func() { projectsRoot = previousProjectsRoot })
			home := filepath.Join(t.TempDir(), "wb-home")
			t.Setenv("WB_HOME", home)
			dir, err := sessionDir()
			if err != nil {
				t.Fatal(err)
			}
			source, err := session.Register(dir, session.Record{
				PID: os.Getpid(), WBSessionID: "wbs-park-retry-" + strings.ReplaceAll(test.name, " ", "-"),
				Machine: "test-machine", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			bundle := sessionpark.Bundle{
				SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-retry-" + strings.ReplaceAll(test.name, " ", "-"),
				Source: source, Continuation: test.storedContinuation, Worktrees: test.storedMembers, ParkedAt: time.Unix(20, 0).UTC(),
			}
			store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
			if _, err := store.Create(bundle); err != nil {
				t.Fatal(err)
			}
			oldCapture := captureParkedSessionAggregate
			captureParkedSessionAggregate = func(_ context.Context, _ string, _ []worktrees.ListResult, _ session.Record, persist func([]sessionpark.Worktree) error) error {
				return persist(test.currentlyCaptured)
			}
			t.Cleanup(func() { captureParkedSessionAggregate = oldCapture })
			contextPath := filepath.Join(t.TempDir(), "continuation.md")
			if err := os.WriteFile(contextPath, []byte(test.currentContinuation+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := newSessionParkCmd()
			command.SetArgs([]string{"--context-file", contextPath})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "conflicts with the current source, continuation, or member evidence") {
				t.Fatalf("retry error = %v, want immutable-input conflict", err)
			}
			state, err := store.Load(bundle.ParkedSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !sessionpark.EqualBundle(state.Bundle, bundle) || state.Status != sessionpark.StatusParked || len(state.Events) != 0 {
				t.Fatalf("retry mutated immutable aggregate: %#v", state)
			}
			if _, err := os.Stat(filepath.Join(dir, "lifecycle", source.WBSessionID+".parked.json")); !os.IsNotExist(err) {
				t.Fatalf("retry marked source parked despite mismatched evidence: %v", err)
			}
		})
	}
}

func TestSessionParkRejectsContentBearingContinuationFlags(t *testing.T) {
	for _, flag := range []string{"--summary", "--validation", "--remaining"} {
		command := newSessionParkCmd()
		command.SetArgs([]string{flag, "private value"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("flag %s error = %v, want unknown flag", flag, err)
		}
	}
}

func TestSessionResumeLocalFailsClosedWithoutMutation(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	parkedID := "park-local-fail-closed"
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	source := session.Record{PID: 41, WBSessionID: "wbs-parked-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
	if _, err := store.Create(sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID, Source: source,
		Continuation: "private continuation", ParkedAt: time.Unix(11, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := sessionDir()
	if err != nil {
		t.Fatal(err)
	}
	successor := session.Record{PID: os.Getpid(), WBSessionID: "wbs-local-successor", Runtime: "codex", StartedAt: time.Now().UTC()}
	if _, err := session.Register(dir, successor); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	beforeRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	command := newSessionResumeCmd()
	command.SetArgs([]string{parkedID})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("local resume error = %v, want fail-closed coordinator checkpoint", err)
	}
	afterRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeRecord, afterRecord) {
		t.Fatal("local resume mutated the successor session registry")
	}
	state, err := store.Load(parkedID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != sessionpark.StatusParked || len(state.Events) != 0 {
		t.Fatalf("local resume mutated parked aggregate: status=%s events=%d", state.Status, len(state.Events))
	}
}

func TestSessionParkRemoteReconstructabilityRefusesDirtyEvidence(t *testing.T) {
	bundle := sessionpark.Bundle{ParkedSessionID: "park-dirty", Worktrees: []sessionpark.Worktree{{WorktreeDir: "/tmp/dirty", Head: "a", RemoteHead: "a", Dirty: true}}}
	err := validateParkedRemoteBundle(bundle, "hetzner-vm1")
	if err == nil || !strings.Contains(err.Error(), "/tmp/dirty") || !strings.Contains(err.Error(), "dirty=true") {
		t.Fatalf("error = %v", err)
	}
}

// These command-level cases deliberately exercise the public park/resume
// boundary rather than the old reconstructability helper. Remote delivery is
// intentionally fail-closed until the coordinator/courier path is wired.
func TestSessionResumeRemoteSingleWorktreeFailsClosed(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{cleanParkedWorktree("/tmp/one", "feature/one")})
	assertRemoteResumeFailsClosedWithoutMutation(t, fixture)
}

func assertRemoteResumeFailsClosedWithoutMutation(t *testing.T, fixture remoteResumeTestFixture) {
	t.Helper()
	beforeTree := snapshotTrees(t, fixture.home, fixture.custodyRoot)
	store := sessionpark.NewStore(filepath.Join(fixture.home, "parked-sessions"))
	beforeState, err := store.Load(fixture.parkedID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	deliver := func(context.Context, sessionpark.State, string) error {
		deliveries++
		return nil
	}
	command := newSessionResumeCmdWithRemoteDelivery(deliver)
	command.SetArgs([]string{fixture.parkedID, "--to", "target", "--via", "ssh", "--config", fixture.config})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "gated") {
		t.Fatalf("remote single-worktree resume = %v; want explicit fail-closed gate", err)
	}
	if deliveries != 0 {
		t.Fatalf("fail-closed remote resume reached delivery seam %d times", deliveries)
	}
	afterState, err := store.Load(fixture.parkedID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeState, afterState) {
		t.Fatalf("remote refusal mutated parked aggregate: before=%#v after=%#v", beforeState, afterState)
	}
	if afterTree := snapshotTrees(t, fixture.home, fixture.custodyRoot); !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("remote refusal mutated session registry, Work Log, or custody tree:\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
}

func TestSessionResumeRemoteBundleFailsClosed(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{
		cleanParkedWorktree("/tmp/one", "feature/one"),
		cleanParkedWorktree("/tmp/two", "feature/two"),
	})
	assertRemoteResumeFailsClosedWithoutMutation(t, fixture)
}

type remoteResumeTestFixture struct {
	parkedID, config, home, custodyRoot string
}

func remoteResumeFixture(t *testing.T, worktrees []sessionpark.Worktree) remoteResumeTestFixture {
	t.Helper()
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	custodyRoot := filepath.Join(projectsRoot, "custody")
	for index := range worktrees {
		worktrees[index].WorktreeDir = filepath.Join(custodyRoot, fmt.Sprintf("member-%d", index+1))
		journal := filepath.Join(worktrees[index].WorktreeDir, ".wb", "local", "worklog")
		if err := os.MkdirAll(journal, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(journal, "events.ndjson"), []byte("source custody evidence\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parkedID := "park-remote"
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID,
		Source:       session.Record{PID: 41, WBSessionID: "wbs-parked-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()},
		Continuation: "continue the parked task", Worktrees: worktrees, ParkedAt: time.Unix(11, 0).UTC(),
	}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	dir, err := sessionDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Register(dir, session.Record{PID: os.Getpid(), WBSessionID: "wbs-remote-coordinator", Machine: "source", Runtime: "codex", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return remoteResumeTestFixture{parkedID: parkedID, config: config, home: home, custodyRoot: custodyRoot}
}

func snapshotTrees(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte)
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[path] = append([]byte(nil), raw...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func cleanParkedWorktree(path, branch string) sessionpark.Worktree {
	head := strings.Repeat("a", 40)
	return sessionpark.Worktree{
		Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
		WorktreeDir: path, Branch: branch, Head: head, RemoteHead: head,
		WorkLogReference: "worklog:parked/remote/" + strings.Repeat("b", 64),
	}
}
