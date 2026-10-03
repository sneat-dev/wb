package runexec

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestChangedNativeImmutableBaseNoWorkMissingDefaultAndInvalidTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module changedfixture\n\ngo 1.22\n")
	writeFixture(t, filepath.Join(root, "app.go"), "package changedfixture\nfunc App() {}\n")
	testenv.Git(t, root, "init", "-b", "main")
	testenv.Git(t, root, "add", ".")
	testenv.Git(t, root, "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "base")
	// ChangedPackages and default-branch detection only read this committed graph;
	// each parallel child has fresh operation/result state and no report writer.
	for _, name := range []string{"no-work", "no-default", "invalid-target"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ops := ChangedOperations{Getwd: func() (string, error) { return root, nil }, DefaultBranch: hooks.DetectDefaultBranch, Packages: quality.ChangedPackages}
			request := ChangedRequest{Argv: []string{"/bin/false"}, Target: "HEAD"}
			if name == "no-default" {
				request.Target = ""
			}
			if name == "invalid-target" {
				request.Target = "does-not-exist"
			}
			result, err := ops.Run(context.Background(), request)
			switch name {
			case "no-work":
				if err != nil || !result.NoWork || result.MissingTarget || result.Target == "" || result.MergeBase == "" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			case "no-default":
				if err != nil || !result.MissingTarget {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			case "invalid-target":
				if err == nil || !strings.Contains(err.Error(), "wb run --changed") {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}
func TestChangedNativeModuleRootRunsWithAppendedDot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module changedfixture\n\ngo 1.22\n")
	writeFixture(t, filepath.Join(root, "main.go"), "package main\nfunc main() {}\n")
	testenv.Git(t, root, "init", "-b", "main")
	testenv.Git(t, root, "add", ".")
	testenv.Git(t, root, "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "base")
	testenv.Git(t, root, "checkout", "-q", "-b", "feature")
	writeFixture(t, filepath.Join(root, "main.go"), "package main\nfunc main() { _ = 1 }\n")
	testenv.Git(t, root, "add", ".")
	testenv.Git(t, root, "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "change root")
	ops := ChangedOperations{Getwd: func() (string, error) { return root, nil }, DefaultBranch: hooks.DetectDefaultBranch, Packages: quality.ChangedPackages}
	result, err := ops.Run(context.Background(), ChangedRequest{Target: "main", Argv: []string{"/bin/sh", "-c", `for a in "$@"; do echo "arg:$a"; done`, "sh"}})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	execOps := defaultExecuteOperations()
	execOps.Getwd = func() (string, error) { return root, nil }
	child, err := (Executor{ops: &execOps}).Run(context.Background(), ExecuteRequest{Argv: result.Argv, Stdout: &stdout, Stderr: &stderr})
	if err != nil || child.ExitCode != 0 || child.ChildFailed {
		t.Fatalf("child=%+v err=%v stderr=%s", child, err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "arg:.") {
		t.Errorf("stdout=%q, want module-root pattern appended", stdout.String())
	}
}
