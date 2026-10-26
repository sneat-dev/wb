//go:build e2e

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

// rebindReuseFixture builds one real canonical clone and returns the projects
// root WB resolves its private state from.
func rebindReuseFixture(t *testing.T) (root, projects, canonical string, inv *invocation) {
	t.Helper()
	testenv.Isolate(t)
	root = t.TempDir()
	projects = filepath.Join(root, "projects")
	canonical = filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, root, "app", canonical)
	t.Setenv("WB_PROJECTS_ROOT", projects)
	return root, projects, canonical, &invocation{projectsRoot: projects}
}

// retainReusedRegistration creates the first linked worktree, retires it, and
// returns the current checkout that Git bound to the same administrative
// directory, which is what makes the collaboration checkout ID reusable.
func retainReusedRegistration(t *testing.T, projects, root, canonical string) (retired, current worktreecollab.Checkout, retiredRoot, currentRoot string) {
	t.Helper()
	service, err := newCollaborationService(&invocation{projectsRoot: projects})
	if err != nil {
		t.Fatal(err)
	}
	retiredRoot = filepath.Join(root, "retired", "checkout")
	if err := os.MkdirAll(filepath.Dir(retiredRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	gcGit(t, canonical, "worktree", "add", "-b", "first", retiredRoot, "main")
	if retired, err = service.Ports.Resolve(context.Background(), retiredRoot); err != nil {
		t.Fatal(err)
	}
	gcGit(t, canonical, "worktree", "remove", "--force", retiredRoot)
	currentRoot = filepath.Join(root, "current", "checkout")
	if err := os.MkdirAll(filepath.Dir(currentRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	gcGit(t, canonical, "worktree", "add", "-b", "second", currentRoot, "main")
	if current, err = service.Ports.Resolve(context.Background(), currentRoot); err != nil {
		t.Fatal(err)
	}
	if current.ID != retired.ID || current.GitDir != retired.GitDir || current.Root == retired.Root {
		t.Fatalf("Git did not reuse the linked registration: retired %+v current %+v", retired, current)
	}
	if _, err := os.Lstat(retiredRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired root still exists: %v", err)
	}
	return retired, current, retiredRoot, currentRoot
}

// initializeStaleOwnership publishes the stale snapshot for a recorded owner,
// exactly as the retired checkout left it.
func initializeStaleOwnership(t *testing.T, service worktreecollab.Service, retired worktreecollab.Checkout, owner string) {
	t.Helper()
	if _, err := service.Store.WithLocked(context.Background(), retired, func(state *worktreecollab.State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(worktreecollab.TakeRequest{Caller: owner, ExpectedOwner: worktreecollab.NoOwner, At: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
}

func staleSnapshotPath(projects string, checkout worktreecollab.Checkout) string {
	return filepath.Join(projects, ".wb", "worktree-collaboration", checkout.ID+".json")
}

func gitSnapshot(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func waitForOwnerStatus(t *testing.T, service worktreecollab.Service, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := service.Ports.OwnerStatus(id)
		if err == nil && status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner %s status never became %q: status=%q err=%v", id, want, status, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestE2ECollaborationRebindRecoversReusedGitRegistrationWithInactiveOwner is
// the native recovery journey: the preserved defect refuses, the explicit
// exactly-bound recovery succeeds once against a real registry, real lock, and
// real filesystem, and the recorded owner is corroborated inactive rather than
// assumed.
//
//nolint:paralleltest // The fixture configures process-wide Git and WB environment.
func TestE2ECollaborationRebindRecoversReusedGitRegistrationWithInactiveOwner(t *testing.T) {
	root, projects, canonical, inv := rebindReuseFixture(t)
	service, err := newCollaborationService(inv)
	if err != nil {
		t.Fatal(err)
	}
	retired, current, retiredRoot, currentRoot := retainReusedRegistration(t, projects, root, canonical)
	sessions := filepath.Join(projects, ".wb", session.DirName)
	retiredOwner, err := session.Register(sessions, session.Record{PID: 1 << 29, Runtime: "codex", NativeHarnessID: "retired-owner"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := session.Register(sessions, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "recovery-caller"})
	if err != nil {
		t.Fatal(err)
	}
	initializeStaleOwnership(t, service, retired, retiredOwner.WBSessionID)
	if status, err := service.Ports.OwnerStatus(retiredOwner.WBSessionID); err != nil || status != "inactive" {
		t.Fatalf("recorded owner status = %q, %v; want inactive", status, err)
	}
	if id, err := service.Ports.Caller(); err != nil || id != live.WBSessionID {
		t.Fatalf("invoking session = %q, %v; want %q", id, err, live.WBSessionID)
	}
	headBefore := gitSnapshot(t, currentRoot, "rev-parse", "HEAD")
	branchBefore := gitSnapshot(t, currentRoot, "rev-parse", "--abbrev-ref", "HEAD")
	statusBefore := gitSnapshot(t, currentRoot, "status", "--porcelain=v1")
	registrationBefore := gitSnapshot(t, canonical, "worktree", "list", "--porcelain")
	refsBefore := gitSnapshot(t, currentRoot, "show-ref", "--head")
	snapshotBefore, err := os.ReadFile(staleSnapshotPath(projects, retired))
	if err != nil {
		t.Fatal(err)
	}

	// The preserved defect: neither existing route accepts a stale root plus a
	// recorded owner that is not the caller.
	if _, err := service.Take(context.Background(), currentRoot, retiredOwner.WBSessionID, false, ""); err == nil {
		t.Fatal("take accepted the stale-root envelope the defect is about")
	}
	if _, err := service.Rebind(context.Background(), currentRoot, retired.Root); err == nil ||
		!strings.Contains(err.Error(), "current owner") {
		t.Fatalf("current-owner rebind route = %v; want the recorded-owner refusal", err)
	}
	if _, err := service.RecoverInactiveOwner(context.Background(), currentRoot, worktreecollab.InactiveOwnerRecovery{
		ExpectedRoot: retired.Root, ExpectedOwner: retiredOwner.WBSessionID, ExpectedEpoch: 1, ExpectedRevision: 7,
		Reason: "stale revision", At: time.Now()}); err == nil || !strings.Contains(err.Error(), "coordination revision") {
		t.Fatalf("mismatched observations = %v", err)
	}
	if after, err := os.ReadFile(staleSnapshotPath(projects, retired)); err != nil || !bytes.Equal(snapshotBefore, after) {
		t.Fatalf("refused recovery rewrote the snapshot: %v", err)
	}

	// A recreated retired root must stop the recovery before any ownership move.
	if err := os.Mkdir(retiredRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	recoveryArgs := []string{currentRoot, "--take-inactive-ownership", "--expected-root", retired.Root,
		"--expected-owner", retiredOwner.WBSessionID, "--expected-owner-epoch", "1", "--expected-revision", "1",
		"--reason", "retired checkout reused by Git after the recorded owner exited"}
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(collaborationFactory(inv)), recoveryArgs...)
	if err == nil || !strings.Contains(err.Error(), "still exists") || out != "" {
		t.Fatalf("recreated retired root: out=%q err=%v", out, err)
	}
	if err := os.Remove(retiredRoot); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(staleSnapshotPath(projects, retired)); err != nil || !bytes.Equal(snapshotBefore, after) {
		t.Fatalf("recreated-root refusal rewrote the snapshot: %v", err)
	}

	out, err = executeCollaborationCommand(t, newWorktreeRebindCmd(collaborationFactory(inv)), recoveryArgs...)
	if err != nil {
		t.Fatalf("native recovery: %v\noutput %s", err, out)
	}
	for _, fragment := range []string{"recovered " + retired.ID, retiredRoot + " -> " + currentRoot,
		"owner " + retiredOwner.WBSessionID + " -> " + live.WBSessionID, "(epoch 2, revision 2)"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("recovery output %q missing %q", out, fragment)
		}
	}
	recovered, found, err := service.Store.Load(current)
	if err != nil || !found {
		t.Fatalf("recovered snapshot = %t, %v", found, err)
	}
	if recovered.Checkout.ID != retired.ID || recovered.Checkout.GitDir != retired.GitDir ||
		recovered.Checkout.CommonDir != retired.CommonDir || recovered.Checkout.Root != currentRoot {
		t.Fatalf("recovery did not keep the exact Git identity at the current root: %+v", recovered.Checkout)
	}
	if recovered.Owner != live.WBSessionID || recovered.OwnerEpoch != 2 || recovered.Revision != 2 {
		t.Fatalf("recovered ownership = %q epoch %d revision %d", recovered.Owner, recovered.OwnerEpoch, recovered.Revision)
	}
	if len(recovered.OwnerChanges) != 2 || len(recovered.CheckoutRebinds) != 1 {
		t.Fatalf("recovered audit = %d owner changes, %d rebinds", len(recovered.OwnerChanges), len(recovered.CheckoutRebinds))
	}
	change := recovered.OwnerChanges[1]
	if change.Previous != retiredOwner.WBSessionID || change.Current != live.WBSessionID || change.Actor != live.WBSessionID ||
		change.Epoch != 2 || change.Reason == "" || change.At.IsZero() {
		t.Fatalf("recovered ownership audit = %+v", change)
	}
	if recovered.CheckoutRebinds[0].PreviousRoot != retiredRoot || recovered.CheckoutRebinds[0].CurrentRoot != currentRoot {
		t.Fatalf("recovered rebind audit = %+v", recovered.CheckoutRebinds[0])
	}

	// The recovered snapshot now behaves like any owned checkout.
	info, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeInfoCmd(inv) }, currentRoot, "--format", "json")
	if err != nil || !strings.Contains(info, live.WBSessionID) {
		t.Fatalf("info after recovery = %q, %v", info, err)
	}
	if _, err := service.Join(context.Background(), currentRoot); err != nil {
		t.Fatalf("join after recovery: %v", err)
	}
	if view, err := service.Inspect(context.Background(), currentRoot); err != nil || view.Owner != live.WBSessionID || len(view.Joined) != 2 {
		t.Fatalf("inspect after recovery = %+v, %v", view, err)
	} else if view.Joined[0].SessionID != retiredOwner.WBSessionID || view.Joined[0].Live {
		t.Fatalf("recovery did not retain the inactive member history: %+v", view.Joined)
	}

	// A repeated request returns a clear conflict and never advances the epoch.
	recoveredBytes, err := os.ReadFile(staleSnapshotPath(projects, retired))
	if err != nil {
		t.Fatal(err)
	}
	out, err = executeCollaborationCommand(t, newWorktreeRebindCmd(collaborationFactory(inv)), recoveryArgs...)
	if err == nil || !strings.Contains(err.Error(), "already completed") || out != "" {
		t.Fatalf("repeated recovery: out=%q err=%v", out, err)
	}
	if after, err := os.ReadFile(staleSnapshotPath(projects, retired)); err != nil || !bytes.Equal(recoveredBytes, after) {
		t.Fatalf("repeated recovery rewrote the snapshot: %v", err)
	}

	// Recovery changed no Core content, index, branch, or registration.
	if after := gitSnapshot(t, currentRoot, "rev-parse", "HEAD"); after != headBefore {
		t.Fatalf("recovery changed HEAD: %q -> %q", headBefore, after)
	}
	if after := gitSnapshot(t, currentRoot, "rev-parse", "--abbrev-ref", "HEAD"); after != branchBefore {
		t.Fatalf("recovery changed branch: %q -> %q", branchBefore, after)
	}
	if after := gitSnapshot(t, currentRoot, "status", "--porcelain=v1"); after != statusBefore {
		t.Fatalf("recovery changed the worktree status: %q -> %q", statusBefore, after)
	}
	if after := gitSnapshot(t, currentRoot, "show-ref", "--head"); after != refsBefore {
		t.Fatalf("recovery changed repository refs: %q -> %q", refsBefore, after)
	}
	if after := gitSnapshot(t, canonical, "worktree", "list", "--porcelain"); after != registrationBefore {
		t.Fatalf("recovery changed Git registration:\nbefore %s\nafter  %s", registrationBefore, after)
	}
}

// TestE2ECollaborationRebindRefusesLiveOwnerAndRecoversAfterExit proves the
// takeover mode still fails closed while the recorded owner is a real live
// process, and admits the same transition only after that exact process exits
// and the registry observes it as inactive.
//
//nolint:paralleltest // The fixture configures process-wide Git and WB environment.
func TestE2ECollaborationRebindRefusesLiveOwnerAndRecoversAfterExit(t *testing.T) {
	root, projects, canonical, inv := rebindReuseFixture(t)
	service, err := newCollaborationService(inv)
	if err != nil {
		t.Fatal(err)
	}
	retired, current, retiredRoot, currentRoot := retainReusedRegistration(t, projects, root, canonical)
	owner := exec.Command("sleep", "120")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})
	if _, err := session.Register(filepath.Join(projects, ".wb", session.DirName), session.Record{
		PID: owner.Process.Pid, Runtime: "codex", NativeHarnessID: "live-owner", WBSessionID: "wbs-live-owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Register(filepath.Join(projects, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "recovery-caller", WBSessionID: "wbs-recovery-caller"}); err != nil {
		t.Fatal(err)
	}
	initializeStaleOwnership(t, service, retired, "wbs-live-owner")
	waitForOwnerStatus(t, service, "wbs-live-owner", "live")
	snapshotBefore, err := os.ReadFile(staleSnapshotPath(projects, retired))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{currentRoot, "--take-inactive-ownership", "--expected-root", retiredRoot,
		"--expected-owner", "wbs-live-owner", "--expected-owner-epoch", "1", "--expected-revision", "1",
		"--reason", "attempted takeover of a live owner"}
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(collaborationFactory(inv)), args...)
	if err == nil || !strings.Contains(err.Error(), "proven inactive") || out != "" {
		t.Fatalf("live owner recovery: out=%q err=%v", out, err)
	}
	if after, err := os.ReadFile(staleSnapshotPath(projects, retired)); err != nil || !bytes.Equal(snapshotBefore, after) {
		t.Fatalf("live owner refusal rewrote the snapshot: %v", err)
	}
	if _, err := service.Rebind(context.Background(), currentRoot, retiredRoot); err == nil {
		t.Fatal("lookalike rebind repaired a live owner's stale root")
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatalf("stop live owner process: %v", err)
	}
	if err := owner.Wait(); err != nil && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("reap live owner process: %v", err)
	}
	waitForOwnerStatus(t, service, "wbs-live-owner", "inactive")
	out, err = executeCollaborationCommand(t, newWorktreeRebindCmd(collaborationFactory(inv)), args...)
	if err != nil || !strings.Contains(out, "owner wbs-live-owner -> wbs-recovery-caller") {
		t.Fatalf("recovery after the owner exited: out=%q err=%v", out, err)
	}
	recovered, found, err := service.Store.Load(current)
	if err != nil || !found || recovered.Owner != "wbs-recovery-caller" || recovered.OwnerEpoch != 2 || recovered.Revision != 2 {
		t.Fatalf("recovery after exit = %+v, %t, %v", recovered, found, err)
	}
}
