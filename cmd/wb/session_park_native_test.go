package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionResumeLocalActualCustodyRefusalDoesNotClaimRoute(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
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
	// setUpRenameCLIFixture's origin is a generic "remote.git" whose path does
	// not itself carry "acme/app" identity. Resume now resolves the canonical
	// clone from the member's repository through repopath, so origin must
	// name the real repository, exactly like a genuine GitHub clone would;
	// retarget it to an identically-seeded bare repo at the real placement.
	root := filepath.Dir(projects)
	identityRemote := filepath.Join(root, "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(identityRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "clone", "--bare", filepath.Join(root, "remote.git"), identityRemote).CombinedOutput(); err != nil {
		t.Fatalf("clone identity remote: %v\n%s", err, output)
	}
	canonical := filepath.Join(projects, "acme", "app")
	if output, err := exec.Command("git", "-C", canonical, "remote", "set-url", "origin", identityRemote).CombinedOutput(); err != nil {
		t.Fatalf("retarget origin to identity remote: %v\n%s", err, output)
	}
	home := filepath.Join(projects, ".wb")
	worktree := filepath.Join(home, "worktrees", "park-refusal", "acme", "app")
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: projects, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	var member sessionpark.Worktree
	for _, result := range listed {
		if result.Repository == "acme/app" && result.Task == "park-refusal" {
			worktree = result.WorktreeDir
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
	deps := sessionrun.DefaultResumeDependencies()
	deps.StartLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
		t.Fatal("launcher reached after actual custody refusal")
		return sessionlaunch.Result{}, nil
	}
	deps.MarkResumed = func(string, int, string, string) (session.Record, error) {
		t.Fatal("registry projection reached after actual custody refusal")
		return session.Record{}, nil
	}
	command := cmdsession.NewResume(newCLIRuntime(&invocation{projectsRoot: projects}), cmdsession.Dependencies{Resume: sessionrun.NewResume(deps).Resume})
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

func TestSessionPickupIsResumeAlias(t *testing.T) {
	root := newRootCmd()
	command, _, err := root.Find([]string{"session", "pickup"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "resume" {
		t.Fatalf("session pickup resolved to %q, want resume", command.Name())
	}
}
