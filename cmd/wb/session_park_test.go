package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
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

func TestSessionParkUsesImmutableClaimSessionLink(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/unowned-projection", WorkLogSessionID: "wbs-source"}
	if !ownedBySession(result, source) {
		t.Fatal("claim linked to live source session was not selected for park")
	}
}

func TestSessionParkDoesNotUseDifferentClaimSessionLink(t *testing.T) {
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/other-session", WorkLogSessionID: "wbs-other"}
	if ownedBySession(result, source) {
		t.Fatal("claim linked to another session was attributed to source")
	}
}

func TestSessionParkPublicOutputDoesNotContainContinuation(t *testing.T) {
	secret := "private continuation must never be printed"
	raw, err := json.Marshal(sessionParkOutput{
		ParkedSessionID: "park-test", Status: string(sessionpark.StatusParked), MemberCount: 2,
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

func TestSessionResumeLocalZeroMemberLaunchesOnceAndReplays(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	parkedID := "park-local-zero"
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	source := session.Record{PID: 41, WBSessionID: "wbs-parked-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
	if _, err := store.Create(sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID, Source: source,
		Continuation: "PRIVATE-LOCAL-CONTINUATION", ParkedAt: time.Unix(11, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	launches, attachments, projections := 0, 0, 0
	deps := defaultSessionResumeDependencies()
	deps.withLocalCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, replayAttemptID string, proceed func(*worktrees.ParkedLocalCustody) error) error {
		if replayAttemptID != "" {
			t.Fatalf("fresh zero-member resume admitted replay attempt %q", replayAttemptID)
		}
		return proceed(nil)
	}
	deps.inspectLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
	}
	deps.inspectPrepared = func(context.Context, sessionlaunch.Options) (string, error) {
		return "", sessionlaunch.ErrNotReleased
	}
	deps.attachLocal = func(_ context.Context, _ *worktrees.ParkedLocalCustody, successor session.Record, attemptID string, attemptIndex uint64) error {
		attachments++
		if successor.WBSessionID == "" || attemptID != "000001-test" || attemptIndex != 1 {
			t.Fatalf("attachment successor=%#v attempt=%s/%d", successor, attemptID, attemptIndex)
		}
		return nil
	}
	deps.startLocal = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		launches++
		if options.Authority == nil || options.Authority.RootMode != sessionauthority.LaunchRootParkedNeutral || options.Authority.PinnedCommit != "" {
			t.Fatalf("neutral launch authority = %#v", options.Authority)
		}
		wantRoot := filepath.Join(options.StoreRoot, parkedID, sessionpark.LocalNeutralDirName)
		if options.WorktreeDir != wantRoot {
			t.Fatalf("neutral launch root = %q, want %q", options.WorktreeDir, wantRoot)
		}
		info, err := os.Lstat(wantRoot)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("neutral launch root info=%#v err=%v", info, err)
		}
		contextInfo, err := os.Stat(options.Authority.ContinuationPath)
		if err != nil || contextInfo.Mode().Perm() != 0o600 {
			t.Fatalf("private context info=%#v err=%v", contextInfo, err)
		}
		raw, err := os.ReadFile(options.Authority.ContinuationPath)
		if err != nil || !bytes.Contains(raw, []byte("PRIVATE-LOCAL-CONTINUATION")) || !bytes.Contains(raw, []byte("Retained local worktrees:\n- none")) {
			t.Fatalf("private context = %q err=%v", raw, err)
		}
		record := session.Record{PID: 5151, WBSessionID: options.Authority.SuccessorWBSessionID,
			PredecessorWBSessionID: source.WBSessionID, Machine: source.Machine, Runtime: source.Runtime, StartedAt: time.Unix(20, 0).UTC()}
		if _, err := options.BeforeRelease(ctx, sessionlaunch.Prepared{Session: record, AttemptID: "000001-test", AttemptIndex: 1}); err != nil {
			return sessionlaunch.Result{}, err
		}
		return sessionlaunch.Result{HandoffID: parkedID, WBSessionID: record.WBSessionID, PredecessorWBSessionID: record.PredecessorWBSessionID,
			TargetMachine: record.Machine, PID: record.PID, AttemptID: "000001-test", AttemptIndex: 1, Runtime: record.Runtime, StartedAt: record.StartedAt}, nil
	}
	deps.markResumed = func(_ string, pid int, parkID, successorID string) (session.Record, error) {
		projections++
		if pid != source.PID || parkID != parkedID || successorID == "" {
			t.Fatalf("resumed projection pid=%d park=%q successor=%q", pid, parkID, successorID)
		}
		return source, nil
	}
	run := func() sessionResumeOutput {
		stdout := new(bytes.Buffer)
		command := newSessionResumeCmdWithDependencies(deps)
		command.SetArgs([]string{parkedID, "--format", "json"})
		command.SetOut(stdout)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stdout.String(), "PRIVATE-LOCAL-CONTINUATION") || strings.Contains(stdout.String(), "continuation") {
			t.Fatalf("public output disclosed private continuation: %s", stdout.String())
		}
		var output sessionResumeOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	first, second := run(), run()
	if first.Replay || !second.Replay || first.SuccessorWBSessionID != second.SuccessorWBSessionID || launches != 1 || attachments != 1 || projections != 2 {
		t.Fatalf("first=%#v second=%#v launches=%d attachments=%d projections=%d", first, second, launches, attachments, projections)
	}
}

func TestSessionResumeLocalInterruptionReusesAuthenticatedAttempt(t *testing.T) {
	for _, crashPoint := range []string{"before release", "after release before source finalize"} {
		t.Run(crashPoint, func(t *testing.T) {
			previousProjectsRoot := projectsRoot
			projectsRoot = t.TempDir()
			t.Cleanup(func() { projectsRoot = previousProjectsRoot })
			home := filepath.Join(t.TempDir(), "wb-home")
			t.Setenv("WB_HOME", home)
			parkedID := "park-local-interruption"
			source := session.Record{PID: 41, WBSessionID: "wbs-local-interrupted-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
			store := sessionpark.NewStore(filepath.Join(home, sessionpark.SourceDirName))
			if _, err := store.Create(sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID,
				Source: source, Continuation: "private interrupted continuation", ParkedAt: time.Unix(11, 0).UTC()}); err != nil {
				t.Fatal(err)
			}
			crash := errors.New("injected coordinator crash")
			starts, attaches, afterCalls, projections := 0, 0, 0, 0
			var stable sessionlaunch.Result
			var replayIDs []string
			deps := defaultSessionResumeDependencies()
			deps.withLocalCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, replayAttemptID string, proceed func(*worktrees.ParkedLocalCustody) error) error {
				replayIDs = append(replayIDs, replayAttemptID)
				return proceed(nil)
			}
			deps.attachLocal = func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error {
				attaches++
				return nil
			}
			deps.inspectLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
				if crashPoint == "after release before source finalize" && stable.AttemptID != "" {
					stable.Reused = true
					return stable, nil
				}
				return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
			}
			deps.inspectPrepared = func(context.Context, sessionlaunch.Options) (string, error) {
				if crashPoint == "before release" && stable.AttemptID != "" {
					return stable.AttemptID, nil
				}
				return "", sessionlaunch.ErrNotReleased
			}
			deps.startLocal = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
				starts++
				record := session.Record{PID: 6161, WBSessionID: options.Authority.SuccessorWBSessionID,
					PredecessorWBSessionID: source.WBSessionID, Machine: source.Machine, Runtime: source.Runtime, StartedAt: time.Unix(20, 0).UTC()}
				stable = sessionlaunch.Result{HandoffID: parkedID, WBSessionID: record.WBSessionID, PredecessorWBSessionID: record.PredecessorWBSessionID,
					TargetMachine: record.Machine, PID: record.PID, AttemptID: "000001-stable", AttemptIndex: 1, Runtime: record.Runtime, StartedAt: record.StartedAt}
				if _, err := options.BeforeRelease(ctx, sessionlaunch.Prepared{Session: record, AttemptID: stable.AttemptID, AttemptIndex: stable.AttemptIndex}); err != nil {
					return sessionlaunch.Result{}, err
				}
				if crashPoint == "before release" && starts == 1 {
					return sessionlaunch.Result{}, crash
				}
				return stable, nil
			}
			deps.afterLocalLaunch = func(sessionlaunch.Result) error {
				afterCalls++
				if crashPoint == "after release before source finalize" && afterCalls == 1 {
					return crash
				}
				return nil
			}
			deps.markResumed = func(string, int, string, string) (session.Record, error) {
				projections++
				return source, nil
			}
			run := func() error {
				command := newSessionResumeCmdWithDependencies(deps)
				command.SetArgs([]string{parkedID, "--format", "json"})
				command.SetOut(new(bytes.Buffer))
				return command.Execute()
			}
			if err := run(); !errors.Is(err, crash) {
				t.Fatalf("first interruption error = %v", err)
			}
			interrupted, err := store.Load(parkedID)
			if err != nil || interrupted.Status != sessionpark.StatusParked || interrupted.ResumeRoute == nil || interrupted.ResumeRoute.Mode != sessionpark.ResumeRouteLocal {
				t.Fatalf("interrupted state=%#v err=%v", interrupted, err)
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			wantStarts := 2
			if crashPoint == "after release before source finalize" {
				wantStarts = 1
			}
			if starts != wantStarts || attaches != 2 || projections != 1 || len(replayIDs) != 2 || replayIDs[0] != "" || replayIDs[1] != stable.AttemptID {
				t.Fatalf("starts=%d attaches=%d projections=%d replayIDs=%#v stable=%#v", starts, attaches, projections, replayIDs, stable)
			}
			final, err := store.Load(parkedID)
			if err != nil || final.Status != sessionpark.StatusResumed || final.Successor == nil || final.Successor.WBSessionID != stable.WBSessionID {
				t.Fatalf("final state=%#v err=%v", final, err)
			}
		})
	}
}

func TestSessionResumeLocalActualCustodyRefusalDoesNotClaimRoute(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	previousProjectsRoot := projectsRoot
	projectsRoot = projects
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	source := session.Record{PID: os.Getpid(), WBSessionID: "wbs-local-refusal-source", Machine: "source",
		Runtime: "codex", Model: "test", StartedAt: time.Now().UTC().Add(-time.Minute)}
	t.Setenv(worktrees.EnvAgentPID, fmt.Sprint(source.PID))
	t.Setenv(worktrees.EnvAgentRuntime, source.Runtime)
	t.Setenv(worktrees.EnvAgentModel, source.Model)
	t.Setenv(worktrees.EnvAgentID, source.WBSessionID)
	prompt := writeOriginalPromptFixture(t, "create parked refusal fixture")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := run([]string{"--projects-root", projects, "worktree", "create", "park-refusal", "acme/app", "--model", source.Model,
		"--original-prompt-file", prompt}, stdout, stderr); code != exitOK {
		t.Fatalf("worktree create code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	home := os.Getenv("WB_HOME")
	worktree := filepath.Join(home, "worktrees", "park-refusal", "acme", "app")
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: projects, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	var member sessionpark.Worktree
	for _, result := range listed {
		if result.Repository == "acme/app" && result.Task == "park-refusal" {
			worktree = result.WorktreeDir
			result.Repository = ""
			member, err = worktrees.CaptureParkedSessionWorktree(context.Background(), projects, result, source)
			break
		}
	}
	if err != nil || member.WorktreeDir == "" {
		t.Fatalf("capture parked member=%#v err=%v listed=%#v", member, err, listed)
	}
	parkedID := "park-local-actual-refusal"
	store := sessionpark.NewStore(filepath.Join(home, sessionpark.SourceDirName))
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID, Source: source,
		Continuation: "private refusal continuation", Worktrees: []sessionpark.Worktree{member}, ParkedAt: time.Now().UTC()}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	if err := worktrees.RecordCustody(worktree, "", "newer sequential session", worktrees.AgentIdentity{
		Runtime: "codex", AgentID: "newer", Model: "test", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTrees(t, worktree, filepath.Join(home, session.DirName))
	deps := defaultSessionResumeDependencies()
	deps.startLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		t.Fatal("launcher reached after actual custody refusal")
		return sessionlaunch.Result{}, nil
	}
	deps.markResumed = func(string, int, string, string) (session.Record, error) {
		t.Fatal("registry projection reached after actual custody refusal")
		return session.Record{}, nil
	}
	command := newSessionResumeCmdWithDependencies(deps)
	command.SetArgs([]string{parkedID})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "newer session custody") {
		t.Fatalf("local custody refusal error = %v", err)
	}
	after := snapshotTrees(t, worktree, filepath.Join(home, session.DirName))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("actual custody refusal mutated registry or Work Log: before=%#v after=%#v", before, after)
	}
	state, err := store.Load(parkedID)
	if err != nil || state.ResumeRoute != nil || state.Status != sessionpark.StatusParked || len(state.Events) != 0 {
		t.Fatalf("refused source state=%#v err=%v", state, err)
	}
	for _, name := range []string{"resume-route.json", sessionpark.SuccessorContextFileName} {
		if _, err := os.Stat(filepath.Join(store.Root, parkedID, name)); !os.IsNotExist(err) {
			t.Fatalf("refusal published %s: %v", name, err)
		}
	}
	lock, err := store.Acquire(context.Background(), parkedID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), sessionmove.SSHConfig{Host: "target.example", User: "ai"}, time.Now().UTC()); err != nil {
		t.Fatalf("subsequent remote route could not claim after zero-mutation local refusal: %v", err)
	}
}

func TestSessionResumeLocalPreflightsBeforeClaimingRouteOrCustody(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	parkedID := "park-local-preflight"
	source := session.Record{PID: 41, WBSessionID: "wbs-local-preflight-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
	store := sessionpark.NewStore(filepath.Join(home, sessionpark.SourceDirName))
	if _, err := store.Create(sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkedID,
		Source: source, Continuation: "private continuation", ParkedAt: time.Unix(11, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("fixed tmux executable is unavailable")
	deps := defaultSessionResumeDependencies()
	deps.preflightLocal = func(session.Record) error { return want }
	deps.withLocalCustody = func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error {
		t.Fatal("custody reached after preflight failure")
		return nil
	}
	command := newSessionResumeCmdWithDependencies(deps)
	command.SetArgs([]string{parkedID})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); !errors.Is(err, want) {
		t.Fatalf("resume error = %v, want %v", err, want)
	}
	state, err := store.Load(parkedID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != sessionpark.StatusParked || state.ResumeRoute != nil || len(state.Events) != 0 {
		t.Fatalf("preflight failure mutated parked state: %#v", state)
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
// boundary rather than the old reconstructability helper. Both bundle sizes
// must reach the transport seam after the complete source preflight.
func TestSessionResumeRemoteSingleWorktreeReachesTransport(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{cleanParkedWorktree("/tmp/one", "feature/one")})
	assertRemoteResumeReachesTransport(t, fixture)
}

func assertRemoteResumeReachesTransport(t *testing.T, fixture remoteResumeTestFixture) {
	t.Helper()
	beforeTree := snapshotTrees(t, filepath.Join(fixture.home, session.DirName), fixture.custodyRoot)
	store := sessionpark.NewStore(filepath.Join(fixture.home, "parked-sessions"))
	beforeState, err := store.Load(fixture.parkedID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	transportReached := errors.New("transport reached")
	deps := defaultSessionResumeDependencies()
	deps.withRemoteCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error {
		return proceed()
	}
	deps.deliverSSH = func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error) {
		deliveries++
		return sessionparkcourier.Result{}, transportReached
	}
	command := newSessionResumeCmdWithDependencies(deps)
	command.SetArgs([]string{fixture.parkedID, "--to", "target", "--via", "ssh", "--config", fixture.config})
	command.SetOut(new(bytes.Buffer))
	if err := command.Execute(); !errors.Is(err, transportReached) {
		t.Fatalf("remote resume = %v; want transport sentinel", err)
	}
	if deliveries != 1 {
		t.Fatalf("remote resume reached delivery seam %d times, want once", deliveries)
	}
	afterState, err := store.Load(fixture.parkedID)
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Status != sessionpark.StatusParked || len(afterState.Events) != 0 || afterState.Successor != nil || afterState.RemoteReceipt != nil ||
		afterState.ResumeRoute == nil || afterState.ResumeRoute.Mode != sessionpark.ResumeRouteRemote || afterState.ResumeRoute.TargetMachine != "target" ||
		!sessionpark.EqualBundle(beforeState.Bundle, afterState.Bundle) {
		t.Fatalf("remote ambiguous delivery changed source beyond its durable route/envelope: before=%#v after=%#v", beforeState, afterState)
	}
	if afterTree := snapshotTrees(t, filepath.Join(fixture.home, session.DirName), fixture.custodyRoot); !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("remote refusal mutated session registry, Work Log, or custody tree:\nbefore=%#v\nafter=%#v", beforeTree, afterTree)
	}
}

func TestSessionResumeRemoteBundleReachesTransport(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{
		cleanParkedWorktree("/tmp/one", "feature/one"),
		cleanParkedWorktree("/tmp/two", "feature/two"),
	})
	assertRemoteResumeReachesTransport(t, fixture)
}

func TestSessionResumeRemoteRetryUsesRetainedSSHEndpoint(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{cleanParkedWorktree("/tmp/one", "feature/one")})
	store := sessionpark.NewStore(filepath.Join(fixture.home, sessionpark.SourceDirName))
	lock, err := store.Acquire(context.Background(), fixture.parkedID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	state, err := store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	transportReached := errors.New("transport reached")
	var delivered []sessionmove.SSHConfig
	deps := defaultSessionResumeDependencies()
	deps.withRemoteCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error { return proceed() }
	deps.deliverSSH = func(_ context.Context, config sessionmove.SSHConfig, _ []byte, _ sessionparkcourier.Options) (sessionparkcourier.Result, error) {
		delivered = append(delivered, config)
		return sessionparkcourier.Result{}, transportReached
	}
	if _, err := resumeParkedRemote(context.Background(), deps, store, lock, state, "target", "ssh", fixture.config, time.Unix(100, 0), io.Discard, t.TempDir()); !errors.Is(err, transportReached) {
		t.Fatalf("first delivery error = %v", err)
	}
	state, err = store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumeParkedRemote(context.Background(), deps, store, lock, state, "target", "ssh", "", time.Unix(200, 0), io.Discard, t.TempDir()); !errors.Is(err, transportReached) {
		t.Fatalf("retained-route replay error = %v", err)
	}
	if len(delivered) != 2 || delivered[0].Host != "target" || delivered[1] != delivered[0] {
		t.Fatalf("delivery endpoints = %#v", delivered)
	}
	driftConfig := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(driftConfig, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: changed.example\n        user: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = resumeParkedRemote(context.Background(), deps, store, lock, state, "target", "ssh", driftConfig, time.Unix(300, 0), io.Discard, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "differs from the retained route") {
		t.Fatalf("explicit endpoint drift error = %v", err)
	}
	if strings.Contains(err.Error(), "changed.example") || strings.Contains(err.Error(), "other") || len(delivered) != 2 {
		t.Fatalf("endpoint drift leaked or delivered: error=%q endpoints=%#v", err, delivered)
	}
}

func TestSessionResumeRemoteReceiptRetryRepairsRegistryWithoutRedelivery(t *testing.T) {
	fixture := remoteResumeFixture(t, []sessionpark.Worktree{
		cleanParkedWorktree("/tmp/one", "feature/one"),
		cleanParkedWorktree("/tmp/two", "feature/two"),
	})
	deliveries, projections := 0, 0
	projectionCrash := errors.New("crash after source finalization before registry projection")
	deps := defaultSessionResumeDependencies()
	deps.withRemoteCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error { return proceed() }
	deps.deliverSSH = func(_ context.Context, _ sessionmove.SSHConfig, raw []byte, _ sessionparkcourier.Options) (sessionparkcourier.Result, error) {
		deliveries++
		return validRemoteCourierResult(t, raw), nil
	}
	deps.markResumed = func(string, int, string, string) (session.Record, error) {
		projections++
		if projections == 1 {
			return session.Record{}, projectionCrash
		}
		return session.Record{}, nil
	}
	first := newSessionResumeCmdWithDependencies(deps)
	first.SetArgs([]string{fixture.parkedID, "--to", "target", "--via", "ssh", "--config", fixture.config, "--format", "json"})
	first.SetOut(new(bytes.Buffer))
	if err := first.Execute(); !errors.Is(err, projectionCrash) {
		t.Fatalf("first resume error = %v", err)
	}
	store := sessionpark.NewStore(filepath.Join(fixture.home, sessionpark.SourceDirName))
	interrupted, err := store.Load(fixture.parkedID)
	if err != nil || interrupted.Status != sessionpark.StatusResumed || interrupted.RemoteReceipt == nil {
		t.Fatalf("interrupted state=%#v err=%v", interrupted, err)
	}
	stdout := new(bytes.Buffer)
	retry := newSessionResumeCmdWithDependencies(deps)
	retry.SetArgs([]string{fixture.parkedID, "--to", "target", "--via", "ssh", "--config", fixture.config, "--format", "json"})
	retry.SetOut(stdout)
	if err := retry.Execute(); err != nil {
		t.Fatal(err)
	}
	var output sessionResumeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || projections != 2 || !output.Replay || output.ReceiptDigest == "" ||
		output.ReceiptDigest != publicReceiptDigest(*interrupted.RemoteReceipt) {
		t.Fatalf("deliveries=%d projections=%d output=%#v interrupted=%#v", deliveries, projections, output, interrupted)
	}
}

func validRemoteCourierResult(t *testing.T, raw []byte) sessionparkcourier.Result {
	t.Helper()
	envelope, err := sessionpark.DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	request := envelope.Request
	digest := sessionmove.DigestBytes(raw)
	runtime, model := sessionpark.RequestedRuntimeModel(request)
	receipt := sessionpark.Receipt{SchemaVersion: sessionpark.ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: digest,
		ParkedSessionID: request.ParkedSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: runtime, Model: model,
		AttemptID: "000001-" + strings.Repeat("d", 32), AttemptIndex: 1, PID: 8181, StartedAt: time.Unix(50, 0).UTC(),
		Members: make([]sessionpark.ReceiptMember, len(request.Members))}
	for index, member := range request.Members {
		reference, err := sessionpark.TargetWorkLogReference(request, digest, member)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Members[index] = sessionpark.ReceiptMember{MemberID: member.MemberID, Repository: member.Repository,
			TargetPath: "/target/" + member.MemberID, Pin: sessionpark.MemberPin(request.ResumeID, member.MemberID),
			Commit: member.Commit, TargetWorkLogReference: reference}
	}
	if err := sessionpark.ValidateReceipt(receipt, request, digest); err != nil {
		t.Fatal(err)
	}
	return sessionparkcourier.Result{Receipt: receipt}
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
		OwnerEventID:     strings.Repeat("c", 64),
	}
}

// registerParkChecklistSession sets up a registered source session in a private
// WB home so the park command reaches its continuation handling.
func registerParkChecklistSession(t *testing.T, wbSessionID string) string {
	t.Helper()
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", home)
	dir, err := sessionDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Register(dir, session.Record{
		PID: os.Getpid(), WBSessionID: wbSessionID, Machine: "test-machine",
		Runtime: "codex", StartedAt: time.Now().UTC().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestSessionParkChecklistPromptsJudgmentOnStderrWithoutTouchingStdout(t *testing.T) {
	home := registerParkChecklistSession(t, "wbs-checklist-park-source")
	contextPath := filepath.Join(t.TempDir(), "continuation.md")
	if err := os.WriteFile(contextPath, []byte("carrying the campaign forward\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command := newSessionParkCmd()
	command.SetArgs([]string{"--context-file", contextPath, "--format", "json"})
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	// stdout must stay exactly the machine-readable record: a prompt that
	// polluted stdout would break every --format json consumer.
	var output sessionParkOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("checklist corrupted --format json stdout: %v", err)
	}
	if output.ParkedSessionID == "" {
		t.Fatal("park did not report a parked session ID")
	}
	for _, category := range parkJudgmentCategories {
		if !strings.Contains(stderr.String(), category) {
			t.Fatalf("stderr is missing judgment category %q", category)
		}
		if strings.Contains(stdout.String(), category) {
			t.Fatalf("judgment category %q leaked into stdout", category)
		}
	}
	// The highest-value category is the one an agent forgets: corrections.
	if !strings.Contains(stderr.String(), "corrections") {
		t.Fatal("checklist must prompt for corrections to earlier disproved claims")
	}
	_ = home
}

func TestSessionParkMissingContextFilePromptsJudgmentChecklist(t *testing.T) {
	registerParkChecklistSession(t, "wbs-checklist-missing-context")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command := newSessionParkCmd()
	command.SetArgs([]string{"--format", "json"})
	command.SetOut(stdout)
	command.SetErr(stderr)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--context-file") {
		t.Fatalf("park without a continuation error = %v, want a --context-file refusal", err)
	}
	for _, category := range parkJudgmentCategories {
		if !strings.Contains(stderr.String(), category) {
			t.Fatalf("refusal did not prompt judgment category %q", category)
		}
	}
	if strings.Contains(stdout.String(), "corrections") {
		t.Fatal("judgment checklist must not be written to stdout")
	}
}

// TestSessionParkRefusesContinuationContainingNamedSecretPattern proves
// invariant 1 (fail closed) and invariant 5 (the write is the damage): a
// continuation carrying a named secret shape must be refused before the
// parked-session store gets a single byte, since a stored park continuation
// is immutable from that point on.
func TestSessionParkRefusesContinuationContainingNamedSecretPattern(t *testing.T) {
	withDeterministicSecretScanner(t)
	home := registerParkChecklistSession(t, "wbs-secret-scan-park")
	contextPath := filepath.Join(t.TempDir(), "continuation.md")
	secretLine := "leftover debug line: AWS_ACCESS_KEY_ID=" + fakeAWSAccessKeyID()
	if err := os.WriteFile(contextPath, []byte(secretLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command := newSessionParkCmd()
	command.SetArgs([]string{"--context-file", contextPath, "--format", "json"})
	command.SetOut(stdout)
	command.SetErr(stderr)
	err := command.Execute()
	if err == nil {
		t.Fatal("expected a refusal, got nil error")
	}
	if !strings.Contains(err.Error(), "aws-access-token") || !strings.Contains(err.Error(), "--override-secret") {
		t.Fatalf("refusal error = %v", err)
	}
	for _, surface := range []string{err.Error(), stdout.String(), stderr.String()} {
		if strings.Contains(surface, fakeAWSAccessKeyID()) {
			t.Fatalf("secret scan surface leaked the matched value: %q", surface)
		}
	}
	store := sessionpark.NewStore(filepath.Join(home, "parked-sessions"))
	if _, found, findErr := store.FindBySource("wbs-secret-scan-park"); findErr != nil || found {
		t.Fatalf("a refused park must never create a parked-session record: found=%v err=%v", found, findErr)
	}
}

// TestSessionParkAcceptsOverriddenSecretFindingAndLogsAdvisory proves the
// override contract for park: the exact finding key from a refusal lets the
// park proceed, and the acknowledgement is visible on stderr, never silent.
func TestSessionParkAcceptsOverriddenSecretFindingAndLogsAdvisory(t *testing.T) {
	withDeterministicSecretScanner(t)
	secretLine := "leftover debug line: AWS_ACCESS_KEY_ID=" + fakeAWSAccessKeyID()
	empty := ""
	scanner, _, err := secretscan.LoadDefault(secretscan.LoadOptions{EnvExtraRulesPath: &empty})
	if err != nil {
		t.Fatal(err)
	}
	blocking := scanner.Scan(secretscan.Segment{Name: "continuation", Content: []byte(secretLine)}).Blocking(nil)
	if len(blocking) != 1 {
		t.Fatalf("expected exactly one blocking finding to override, got %+v", blocking)
	}
	overrideKey := blocking[0].Key()

	home := registerParkChecklistSession(t, "wbs-secret-override-park")
	contextPath := filepath.Join(t.TempDir(), "continuation.md")
	if err := os.WriteFile(contextPath, []byte(secretLine), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command := newSessionParkCmd()
	command.SetArgs([]string{"--context-file", contextPath, "--format", "json", "--override-secret", overrideKey})
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("park with an exact override should succeed: %v", err)
	}
	if !strings.Contains(stderr.String(), "secret scan advisory") || !strings.Contains(stderr.String(), "aws-access-token") {
		t.Fatalf("override must be logged as a visible advisory, stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), fakeAWSAccessKeyID()) || strings.Contains(stdout.String(), fakeAWSAccessKeyID()) {
		t.Fatal("advisory or output echoed the matched secret")
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
	if state.Bundle.Continuation != secretLine {
		t.Fatalf("overridden continuation was not durably preserved: %#v", state.Bundle)
	}
}
