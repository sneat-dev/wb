package worktreeclaims

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func finalizeReportTestPorts(t *testing.T) FinalizeReportPorts {
	t.Helper()
	directory := t.TempDir()
	return FinalizeReportPorts{
		SplitRepository: func(repository string) (string, string, error) {
			if repository == "bad" {
				return "", "", errors.New("bad repository")
			}
			return "acme", "app", nil
		},
		ValidSegment: func(task string) bool { return task != "" && !strings.Contains(task, "/") },
		OpenRun: func(string, string, string, bool) (*os.File, string, error) {
			file, err := os.Open(directory)
			return file, directory, err
		},
		OpenChild:     func(*os.File, string, bool) (*os.File, error) { return os.Open(directory) },
		WriteBytes:    func(*os.File, string, []byte, os.FileMode) error { return nil },
		OpenDirectory: func(string, bool) (*os.File, error) { return os.Open(directory) },
		ReadBytes:     func(*os.File, string) ([]byte, error) { return []byte("private completion"), nil },
	}
}

func TestFinalizeReportPrivateStoreAndFailureStages(t *testing.T) {
	t.Parallel()
	ports := finalizeReportTestPorts(t)
	if name, err := ports.FinalizeReportFileName(" task ", "acme/app"); err != nil || name != "task--acme--app.md" {
		t.Fatalf("report name = %q, %v", name, err)
	}
	if _, err := ports.FinalizeReportFileName("task", "bad"); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if _, err := ports.FinalizeReportFileName("bad/task", "acme/app"); err == nil {
		t.Fatal("unsafe task accepted")
	}
	if _, err := ports.WriteFinalizeReport("home", "task", "run", "task", "acme/app", make([]byte, MaxFinalizeReportBytes+1)); err == nil {
		t.Fatal("oversized private report accepted")
	}
	if _, err := ports.WriteFinalizeReport("home", "task", "run", "task", "bad", nil); err == nil {
		t.Fatal("bad report name accepted")
	}
	for _, stage := range []string{"open run", "open reports", "write bytes"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			broken := finalizeReportTestPorts(t)
			failure := errors.New(stage)
			switch stage {
			case "open run":
				broken.OpenRun = func(string, string, string, bool) (*os.File, string, error) { return nil, "", failure }
			case "open reports":
				broken.OpenChild = func(*os.File, string, bool) (*os.File, error) { return nil, failure }
			case "write bytes":
				broken.WriteBytes = func(*os.File, string, []byte, os.FileMode) error { return failure }
			}
			if _, err := broken.WriteFinalizeReport("home", "task", "run", "task", "acme/app", []byte("body")); !errors.Is(err, failure) {
				t.Fatalf("%s = %v", stage, err)
			}
		})
	}
	var written string
	ports.WriteBytes = func(_ *os.File, name string, body []byte, mode os.FileMode) error {
		if string(body) != "body" || mode != 0o600 {
			t.Fatalf("private report write = %q, %v", body, mode)
		}
		written = name
		return nil
	}
	path, err := ports.WriteFinalizeReport("home", "task", "run", "task", "acme/app", []byte("body"))
	if err != nil || written != "task--acme--app.md" || filepath.Base(path) != written {
		t.Fatalf("write private report = %q, %q, %v", path, written, err)
	}
	if body, err := ports.ReadFinalizeReportBody(path); err != nil || body != "private completion" {
		t.Fatalf("read private report = %q, %v", body, err)
	}
	failure := errors.New("private report unavailable")
	ports.OpenDirectory = func(string, bool) (*os.File, error) { return nil, failure }
	if _, err := ports.ReadFinalizeReportBody(path); !errors.Is(err, failure) {
		t.Fatalf("open failure = %v", err)
	}
	ports = finalizeReportTestPorts(t)
	ports.ReadBytes = func(*os.File, string) ([]byte, error) { return nil, failure }
	if _, err := ports.ReadFinalizeReportBody(path); !errors.Is(err, failure) {
		t.Fatalf("read failure = %v", err)
	}
}
