package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// withHostLoad temporarily replaces hostload.System, the Reader every
// admission call site uses by default, and restores it afterward.
func withHostLoad(t *testing.T, load float64) {
	t.Helper()
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
	err := checkHostLoadAdmission(worktreeMergeFlags{})
	if err == nil {
		t.Fatal("checkHostLoadAdmission(saturated host) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "--allow-saturated-host") {
		t.Errorf("refusal %q does not mention the override flag", err)
	}
}

func TestCheckHostLoadAdmissionAllowSaturatedHostOverrides(t *testing.T) {
	withHostLoad(t, 999.0)
	if err := checkHostLoadAdmission(worktreeMergeFlags{allowSaturatedHost: true}); err != nil {
		t.Fatalf("checkHostLoadAdmission with --allow-saturated-host = %v, want nil", err)
	}
}

func TestCheckHostLoadAdmissionAdmitsBelowFloor(t *testing.T) {
	withHostLoad(t, 0.01)
	if err := checkHostLoadAdmission(worktreeMergeFlags{}); err != nil {
		t.Fatalf("checkHostLoadAdmission(quiet host) = %v, want nil", err)
	}
}
