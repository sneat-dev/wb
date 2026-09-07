package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// withHostLoad temporarily replaces hostload.System, the Reader every
// admission call site uses by default, and restores it afterward. It also
// pins WB_ADMISSION_LOAD_FLOOR to a fixed positive floor (4.0): TestMain
// disables host-load admission for the whole cmd/wb test binary by default
// (see main_test.go) so no test here depends on the real host load, and a
// positive WB_ADMISSION_LOAD_FLOOR always wins over that default — even
// inside CI itself — restoring the real gating behavior these tests exist
// to exercise.
func withHostLoad(t *testing.T, load float64) {
	t.Helper()
	t.Setenv(hostload.EnvLoadFloor, "4")
	previous := hostload.System
	hostload.System = func() (float64, error) { return load, nil }
	t.Cleanup(func() { hostload.System = previous })
}

// trivialGoModule writes a minimal, self-contained Go module so `go vet
// ./...` — the CPU-heavy command runqueue.Units admits against the CPU
// budget — succeeds instantly regardless of where the test runs from.
func trivialGoModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hostloadadmissiontest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommandRefusesAdmissionWhenHostIsSaturated(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	trivialGoModule(t, dir)
	t.Chdir(dir)
	withHostLoad(t, 999.0) // far above any real admission.load_floor

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--", "go", "vet", "./..."}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("exit code = %d, want a refusal; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"999.00", "admission floor", "--allow-saturated-host"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q: %s", want, stderr.String())
		}
	}
}

func TestRunCommandAdmitsCPUHeavyWorkBelowFloor(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	trivialGoModule(t, dir)
	t.Chdir(dir)
	withHostLoad(t, 0.01) // far below any real admission.load_floor

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--", "go", "vet", "./..."}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 when load is below the floor; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunCommandAllowSaturatedHostOverridesRefusalAndIsRecorded(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trivialGoModule(t, root)
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "hostload-override", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "hostload-override", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	withHostLoad(t, 999.0)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--allow-saturated-host", "--", "go", "vet", "./..."}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 with --allow-saturated-host; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	events, _, err := runlog.ReadCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no runlog events recorded for the overridden command")
	}
	last := events[len(events)-1]
	if !last.LoadOverride {
		t.Errorf("terminal event LoadOverride = false, want true for a --allow-saturated-host admission")
	}
}

func TestCheckHostLoadAdmissionRefusesWorktreeMergeCandidateValidation(t *testing.T) {
	withHostLoad(t, 999.0)
	admission, err := checkHostLoadAdmission(worktreeMergeFlags{})
	if err == nil {
		t.Fatal("checkHostLoadAdmission(saturated host) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "--allow-saturated-host") {
		t.Errorf("refusal %q does not mention the override flag", err)
	}
	if admission != nil {
		t.Errorf("checkHostLoadAdmission(saturated host) admission = %+v, want nil on refusal", admission)
	}
}

func TestCheckHostLoadAdmissionAllowSaturatedHostOverrides(t *testing.T) {
	withHostLoad(t, 999.0)
	admission, err := checkHostLoadAdmission(worktreeMergeFlags{allowSaturatedHost: true})
	if err != nil {
		t.Fatalf("checkHostLoadAdmission with --allow-saturated-host = %v, want nil", err)
	}
	if admission == nil {
		t.Fatal("checkHostLoadAdmission with --allow-saturated-host recorded no admission")
	}
	if !admission.Overridden {
		t.Errorf("admission.Overridden = false, want true for --allow-saturated-host")
	}
	if admission.Load != 999.0 {
		t.Errorf("admission.Load = %v, want 999.0", admission.Load)
	}
	if admission.Floor <= 0 {
		t.Errorf("admission.Floor = %v, want > 0", admission.Floor)
	}
	if admission.CheckedAt.IsZero() {
		t.Error("admission.CheckedAt is zero")
	}
}

func TestCheckHostLoadAdmissionAdmitsBelowFloor(t *testing.T) {
	withHostLoad(t, 0.01)
	admission, err := checkHostLoadAdmission(worktreeMergeFlags{})
	if err != nil {
		t.Fatalf("checkHostLoadAdmission(quiet host) = %v, want nil", err)
	}
	if admission == nil {
		t.Fatal("checkHostLoadAdmission(quiet host) recorded no admission")
	}
	if admission.Overridden {
		t.Error("admission.Overridden = true, want false without --allow-saturated-host")
	}
	if admission.Load != 0.01 {
		t.Errorf("admission.Load = %v, want 0.01", admission.Load)
	}
}

// TestHostLoadCheckSkippableCoversTheDocumentedShapes exercises
// hostLoadCheckSkippable's decision directly: a resume/land step is gated on
// host load only when it will actually run local CPU-heavy validation. A
// receipt already complete, or already published with its exact candidate
// SHA already validated, does neither — only remote observation/merge is
// left — so it must skip the check. Everything else, including
// validation_failed and a published receipt whose candidate has since moved
// on from what was validated, must still be gated.
func TestHostLoadCheckSkippableCoversTheDocumentedShapes(t *testing.T) {
	validated := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("a", 40)},
		ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{
			CandidateSHA: strings.Repeat("a", 40),
		},
		Validation: quality.VerificationReport{Status: quality.StatusPassed},
	}
	if !hostLoadCheckSkippable(validated) {
		t.Error("published + validated-for-exact-candidate receipt must skip the host-load check")
	}

	complete := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}
	if !hostLoadCheckSkippable(complete) {
		t.Error("complete receipt must skip the host-load check")
	}

	validationFailed := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeValidationFailed}
	if hostLoadCheckSkippable(validationFailed) {
		t.Error("validation_failed receipt must still be gated by the host-load check")
	}

	preparing := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergePreparing}
	if hostLoadCheckSkippable(preparing) {
		t.Error("preparing receipt must still be gated by the host-load check")
	}

	stalePublished := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("b", 40)},
		ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{
			CandidateSHA: strings.Repeat("a", 40), // stale: does not match the current candidate
		},
		Validation: quality.VerificationReport{Status: quality.StatusPassed},
	}
	if hostLoadCheckSkippable(stalePublished) {
		t.Error("published receipt whose validation identity no longer matches the candidate must still be gated")
	}
}

// TestWorktreeMergeResumeOfCompleteReceiptProceedsUnderSaturatedLoad exercises
// the actual `wb worktree merge resume` command end to end: a complete
// receipt has nothing left to validate or push, so resuming it must proceed
// under a saturated host without --allow-saturated-host, instead of being
// refused by host-load admission.
func TestWorktreeMergeResumeOfCompleteReceiptProceedsUnderSaturatedLoad(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	receipt := fixture.receipt
	receipt.Status = orchestrate.WorktreeMergeComplete
	receipt.Failure = ""
	receipt.Cleanup = false
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(fixture.receiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	withHostLoad(t, 999.0) // far above any real admission.load_floor

	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--projects-root", fixture.projectsRoot, "--non-interactive",
		"worktree", "merge", "resume", fixture.receiptPath,
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("resume of a complete receipt under saturated load was refused: %v\nstderr: %s", err, stderr.String())
	}
}

// TestWorktreeMergeResumeOfValidationFailedReceiptIsRefusedUnderSaturatedLoad
// is the contrasting case: a validation_failed receipt still needs a fresh
// local validation before it can proceed, so resuming it under a saturated
// host without --allow-saturated-host must be refused by host-load
// admission, exactly as prepare/land already are.
func TestWorktreeMergeResumeOfValidationFailedReceiptIsRefusedUnderSaturatedLoad(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1) // fixture's receipt is already validation_failed
	if fixture.receipt.Status != orchestrate.WorktreeMergeValidationFailed {
		t.Fatalf("fixture receipt status = %s, want validation_failed", fixture.receipt.Status)
	}

	withHostLoad(t, 999.0) // far above any real admission.load_floor

	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--projects-root", fixture.projectsRoot, "--non-interactive",
		"worktree", "merge", "resume", fixture.receiptPath,
	})
	err := root.Execute()
	if err == nil {
		t.Fatal("resume of a validation_failed receipt under saturated load = nil, want a host-load refusal")
	}
	if !strings.Contains(err.Error(), "admission floor") || !strings.Contains(err.Error(), "--allow-saturated-host") {
		t.Fatalf("resume refusal %q does not look like a host-load admission refusal", err)
	}
}

// TestWorktreeMergePrepareRecordsHostLoadOverrideOnReceipt exercises `wb
// worktree merge prepare --allow-saturated-host` end to end under an
// injected saturated reader and asserts the override is provable from the
// receipt itself: host_load_admission.overridden is true and names the exact
// load and floor the override admitted past.
func TestWorktreeMergePrepareRecordsHostLoadOverrideOnReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, ".wb"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	writeCLIWorktreeFile(t, filepath.Join(seed, "initial.txt"), "initial\n")
	runCLIWorktreeGit(t, seed, "init", "-b", "main")
	runCLIWorktreeGit(t, seed, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, seed, "config", "user.email", "wb@example.test")
	runCLIWorktreeGit(t, seed, "add", "-A")
	runCLIWorktreeGit(t, seed, "commit", "-m", "initial")
	runCLIWorktreeGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, root, "clone", remote, canonical)
	runCLIWorktreeGit(t, canonical, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, canonical, "config", "user.email", "wb@example.test")
	fixture := cliWorktreeMergeFixture{projectsRoot: projectsRoot, canonical: canonical}
	source := createCLIWorktreeSource(t, fixture, "hostload-override-source", "feature/hostload-override", "feature.txt", "feature\n")

	withHostLoad(t, 999.0) // far above any real admission.load_floor

	var stdout, stderr bytes.Buffer
	rootCmd := newRootCmd()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"--projects-root", projectsRoot, "--non-interactive",
		"worktree", "merge", "prepare", source.WorktreeDir,
		"--target", "main", "--model", "test-model", "--agent-runtime", "test",
		"--allow-saturated-host", "--format", "json",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("prepare --allow-saturated-host under saturated load failed: %v\nstderr: %s", err, stderr.String())
	}

	var receipt orchestrate.WorktreeMergeReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("decode prepare output %q: %v", stdout.String(), err)
	}
	if receipt.HostLoadAdmission == nil {
		t.Fatal("receipt has no host_load_admission recorded")
	}
	if !receipt.HostLoadAdmission.Overridden {
		t.Error("receipt host_load_admission.overridden = false, want true")
	}
	if receipt.HostLoadAdmission.Load != 999.0 {
		t.Errorf("receipt host_load_admission.load = %v, want 999.0", receipt.HostLoadAdmission.Load)
	}
	if receipt.HostLoadAdmission.Floor <= 0 {
		t.Errorf("receipt host_load_admission.floor = %v, want > 0", receipt.HostLoadAdmission.Floor)
	}
	if receipt.HostLoadAdmission.CheckedAt.IsZero() {
		t.Error("receipt host_load_admission.checked_at is zero")
	}

	// The persisted receipt on disk must carry the same field — not just the
	// in-memory copy the command happened to print.
	onDisk, err := orchestrate.PeekWorktreeMergeReceipt(projectsRoot, receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.HostLoadAdmission == nil || !onDisk.HostLoadAdmission.Overridden {
		t.Errorf("persisted receipt host_load_admission = %+v, want overridden true", onDisk.HostLoadAdmission)
	}

	// The text-format output must also surface the override, per the ai/capabilities.json note.
	var textOut, textErr bytes.Buffer
	textCmd := newRootCmd()
	textCmd.SetOut(&textOut)
	textCmd.SetErr(&textErr)
	textCmd.SetArgs([]string{
		"--projects-root", projectsRoot, "--non-interactive",
		"worktree", "merge", "resume", receipt.ReceiptPath,
		"--allow-saturated-host",
	})
	// resume of a still-preparing/prepared receipt is a legitimate way to
	// observe the text-format receipt printer; failure here for unrelated
	// remote-landing reasons is fine, only the printed text matters.
	_ = textCmd.Execute()
	if !strings.Contains(textOut.String(), "host_load_admission:") {
		t.Errorf("text-format receipt output %q does not mention host_load_admission", textOut.String())
	}
}
