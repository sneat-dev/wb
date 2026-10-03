package ciaudit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestAuditBatchFleetPreservesFilteringAndPolicyFindings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"app", "skip"} {
		path := filepath.Join(root, "acme", name)
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		if name == "app" {
			if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	reports, err := AuditBatch(BatchOptions{ProjectsRoot: root, Filter: "app", Fleet: true})
	if err != nil || len(reports) != 1 || reports[0].Path != filepath.Join(root, "acme", "app") {
		t.Fatalf("reports=%+v err=%v", reports, err)
	}
	if len(reports[0].Findings) != 1 || reports[0].Findings[0].Code != "go-coverage-threshold" {
		t.Fatalf("findings=%+v", reports[0].Findings)
	}
	reports, err = AuditBatch(BatchOptions{ProjectsRoot: root, Fleet: true})
	if err != nil || len(reports) != 2 {
		t.Fatalf("unfiltered reports=%+v err=%v", reports, err)
	}
	empty, err := AuditBatch(BatchOptions{ProjectsRoot: t.TempDir(), Fleet: true})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}
func TestAuditBatchPropagatesScanAndAbsErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("scan refused")
	deps := defaultBatchDependencies()
	deps.scan = func(string) ([]discover.Repo, error) { return nil, boom }
	if reports, err := auditBatchWithDeps(BatchOptions{Fleet: true}, deps); reports != nil || !errors.Is(err, boom) {
		t.Fatalf("reports=%+v err=%v", reports, err)
	}
	want := &os.SyscallError{Syscall: "getwd", Err: syscall.ENOENT}
	deps.abs = func(string) (string, error) { return "", want }
	called := false
	deps.audit = func(string) (Report, error) { called = true; return Report{}, nil }
	reports, err := auditBatchWithDeps(BatchOptions{Path: "relative"}, deps)
	var syscallErr *os.SyscallError
	if reports != nil || called || !errors.As(err, &syscallErr) || syscallErr.Syscall != "getwd" || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reports=%+v called=%t err=%v", reports, called, err)
	}
}
