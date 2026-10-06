package runexec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// These native cases retain real child/admission/telemetry mechanisms while
// supplying host readings per invocation, without process environment mutation.
func TestNativeHostAdmissionRefusalBelowFloorAndOverrideTelemetry(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	for _, name := range []string{"saturated", "below-floor", "override"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module hostloadadmissiontest\n\ngo 1.22\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if name == "override" {
				testenv.Git(t, root, "init", "-b", "main")
				manifest := worktrees.Manifest{Version: 1, EffortID: "hostload-override", EffortKind: worktrees.EffortKindFeature, Repository: "acme/app", Worktree: root, Branch: "hostload-override", Base: "main", BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(), RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated}
				if err := worktrees.WriteManifest(root, manifest); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultExecuteOperations()
			ops.Getwd = func() (string, error) { return root, nil }
			ops.ResolveLoad = func(string) (float64, string) { return 4, "" }
			ops.System = func() (float64, error) {
				if name == "below-floor" {
					return .01, nil
				}
				return 999, nil
			}
			var stdout, stderr bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := (Executor{ops: &ops}).Run(ctx, ExecuteRequest{ProjectsRoot: t.TempDir(), Argv: []string{"go", "vet", "./..."}, Stdout: &stdout, Stderr: &stderr, AllowSaturatedHost: name == "override"})
			if name == "saturated" {
				if err == nil {
					t.Fatalf("want refusal; result=%+v stdout=%s stderr=%s", result, stdout.String(), stderr.String())
				}
				for _, want := range []string{"999.00", "admission floor", "--allow-saturated-host"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil || result.ChildFailed || result.ExitCode != 0 {
				t.Fatalf("result=%+v err=%v stdout=%s stderr=%s", result, err, stdout.String(), stderr.String())
			}
			if name == "override" {
				events, _, err := runlog.ReadCurrent(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(events) == 0 {
					t.Fatal("no runlog events recorded for overridden command")
				}
				if !events[len(events)-1].LoadOverride {
					t.Error("terminal event LoadOverride=false, want true")
				}
			}
		})
	}
}
