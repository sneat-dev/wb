package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestTerminalLockOperationNameBindsMergeReceipt(t *testing.T) {
	t.Parallel()
	const receipt = "merge-sneat-co-bookius-main-c97cdfb3334d-1d2d9879e050"
	for _, task := range []string{
		"merge-sneat-co-bookius-main-c97cdfb3334d",
		"merge-sneat-co-bookius-main-c97cdfb3334d-a4381f8449a1",
	} {
		if !isTerminalLockOperationName(task, receipt) {
			t.Fatalf("expected %s to bind %s", task, receipt)
		}
	}
	for _, task := range []string{
		"merge-sneat-co-bookius-main-c97cdfb3334e",
		"merge-sneat-co-bookius-main-c97cdfb3334d-feature",
		"merge-sneat-co-bookius-main-c97cdfb3334d-abcdez123456",
		"other-task",
	} {
		if isTerminalLockOperationName(task, receipt) {
			t.Fatalf("unrelated task %s bound to %s", task, receipt)
		}
	}
	if isTerminalLockOperationName("merge-sneat-co-bookius-main-c97cdfb3334d", "invalid-receipt") {
		t.Fatal("malformed receipt identity was accepted")
	}
}

func terminalLockRecoveryFixture(t *testing.T) (TerminalLockRecoveryOptions, string, []byte) {
	t.Helper()
	fixture := newGitFixture(t)
	const task = "merge-acme-app-main-0123456789ab"
	const receiptID = task + "-abcdef123456"
	lockDir := filepath.Join(fixture.home, "worktrees", task)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDir, ".lock")
	if err := os.WriteFile(lockPath, []byte("operation="+task+"\npid="+"999999999"+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := gitTestRun(fixture.canonical, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	receipt := terminalLockLandingReceipt{
		ID: receiptID, Repository: "acme/app", Target: "main", LandingSHA: strings.TrimSpace(head),
		Status: "landed_canonical_sync_blocked", Cleanup: true,
	}
	content, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(fixture.projectsRoot, ".wb", "reports", "worktree-merge")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(reports, receiptID+".json")
	if err := os.WriteFile(receiptPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return TerminalLockRecoveryOptions{ProjectsRoot: fixture.projectsRoot, Task: task, ReceiptPath: receiptPath}, lockPath, content
}

//nolint:paralleltest // The real Git fixture scopes WB configuration through t.Setenv.
func TestTerminalLockRecoveryRequiresFreshContainedLandingAndRetiresOnlyLock(t *testing.T) {
	options, lockPath, receipt := terminalLockRecoveryFixture(t)
	planned, err := RecoverTerminalOperationLock(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Applied || planned.AuditPath != "" || planned.LockPath != lockPath || planned.TargetSHA != planned.LandingSHA {
		t.Fatalf("unexpected dry-run: %#v", planned)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("dry-run changed lock: %v", err)
	}
	options.Apply = true
	applied, err := RecoverTerminalOperationLock(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.AuditPath == "" {
		t.Fatalf("incomplete apply: %#v", applied)
	}
	if _, err := os.Stat(filepath.Dir(lockPath)); !os.IsNotExist(err) {
		t.Fatalf("inert operation shell remains: %v", err)
	}
	auditBytes, err := os.ReadFile(applied.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(receipt)
	if !strings.Contains(string(auditBytes), hex.EncodeToString(wantDigest[:])) {
		t.Fatalf("audit does not bind original receipt: %s", auditBytes)
	}
	if actual, err := os.ReadFile(options.ReceiptPath); err != nil || string(actual) != string(receipt) {
		t.Fatalf("historical receipt changed: %v", err)
	}
}

//nolint:paralleltest // Every case creates a real Git fixture that calls t.Setenv.
func TestTerminalLockRecoveryRefusesUnprovenOrLiveOperations(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *TerminalLockRecoveryOptions, string, []byte)
		want   string
	}{
		{name: "missing receipt", change: func(_ *testing.T, options *TerminalLockRecoveryOptions, _ string, _ []byte) {
			options.ReceiptPath = ""
		}, want: "requires one safe task name"},
		{name: "invalid task", change: func(_ *testing.T, options *TerminalLockRecoveryOptions, _ string, _ []byte) {
			options.Task = "../escape"
		}, want: "requires one safe task name"},
		{name: "receipt outside reports", change: func(_ *testing.T, options *TerminalLockRecoveryOptions, _ string, _ []byte) {
			options.ReceiptPath = filepath.Join(options.ProjectsRoot, "untrusted.json")
		}, want: "exact file under"},
		{name: "receipt missing on disk", change: func(t *testing.T, options *TerminalLockRecoveryOptions, _ string, _ []byte) {
			if err := os.Remove(options.ReceiptPath); err != nil {
				t.Fatal(err)
			}
		}, want: "regular file"},
		{name: "malformed receipt", change: func(t *testing.T, options *TerminalLockRecoveryOptions, _ string, _ []byte) {
			if err := os.WriteFile(options.ReceiptPath, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "parse merge receipt"},
		{name: "wrong receipt status", change: func(t *testing.T, options *TerminalLockRecoveryOptions, _ string, original []byte) {
			var receipt terminalLockLandingReceipt
			if err := json.Unmarshal(original, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.Status = "checks_failed"
			changed, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(options.ReceiptPath, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "does not prove"},
		{name: "live lock", change: func(t *testing.T, options *TerminalLockRecoveryOptions, lock string, _ []byte) {
			if err := os.WriteFile(lock, []byte(fmt.Sprintf("operation=%s\npid=%d\n", options.Task, os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "live"},
		{name: "extra shell content", change: func(t *testing.T, _ *TerminalLockRecoveryOptions, lock string, _ []byte) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(lock), "unexpected"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "not lock-only"},
		{name: "uncontained landing", change: func(t *testing.T, options *TerminalLockRecoveryOptions, _ string, original []byte) {
			var receipt terminalLockLandingReceipt
			if err := json.Unmarshal(original, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.LandingSHA = strings.Repeat("a", 40)
			changed, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(options.ReceiptPath, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "not proven"},
	}
	for _, test := range tests {
		//nolint:paralleltest // The fixture's t.Setenv requires this subtest to stay serial.
		t.Run(test.name, func(t *testing.T) {
			options, lock, receipt := terminalLockRecoveryFixture(t)
			options.Apply = true
			test.change(t, &options, lock, receipt)
			if _, err := RecoverTerminalOperationLock(context.Background(), options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("recovery error = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Fatalf("refusal changed lock: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Every case creates a real Git fixture that calls t.Setenv.
func TestTerminalLockRecoveryPreservesEvidenceOnBoundaryFailures(t *testing.T) {
	sentinel := errors.New("injected boundary failure")
	tests := []struct {
		name   string
		inject func(*terminalLockRecoveryPorts)
	}{
		{"resolve projects root", func(ports *terminalLockRecoveryPorts) {
			ports.abs = func(string) (string, error) { return "", sentinel }
		}},
		{"read receipted bytes", func(ports *terminalLockRecoveryPorts) {
			ports.readReceipt = func(string) ([]byte, error) { return nil, sentinel }
		}},
		{"resolve WB home", func(ports *terminalLockRecoveryPorts) {
			ports.resolve = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, sentinel }
		}},
		{"validate held lock", func(ports *terminalLockRecoveryPorts) {
			ports.validateLock = func(*cleanupTaskHandle) error { return sentinel }
		}},
		{"seek task shell", func(ports *terminalLockRecoveryPorts) { ports.seekTask = func(*os.File) error { return sentinel } }},
		{"list candidates", func(ports *terminalLockRecoveryPorts) {
			ports.list = func(context.Context, ListOptions) (ListOutcome, error) { return ListOutcome{}, sentinel }
		}},
		{"load cleanup backlog", func(ports *terminalLockRecoveryPorts) {
			ports.hasBacklog = func(context.Context, wbhome.Resolution, string, string, string) (bool, error) { return false, sentinel }
		}},
		{"fetch current target", func(ports *terminalLockRecoveryPorts) {
			ports.fetchTarget = func(context.Context, string, string) (string, error) { return "", sentinel }
		}},
		{"revalidate held lock", func(ports *terminalLockRecoveryPorts) {
			original := ports.validateLock
			calls := 0
			ports.validateLock = func(handle *cleanupTaskHandle) error {
				calls++
				if calls == 2 {
					return sentinel
				}
				return original(handle)
			}
		}},
		{"encode audit", func(ports *terminalLockRecoveryPorts) {
			ports.marshalAudit = func(any) ([]byte, error) { return nil, sentinel }
		}},
		{"create audit", func(ports *terminalLockRecoveryPorts) {
			ports.createAudit = func(string, string) (*os.File, error) { return nil, sentinel }
		}},
		{"write audit", func(ports *terminalLockRecoveryPorts) {
			ports.writeAudit = func(*os.File, []byte) (int, error) { return 0, sentinel }
		}},
		{"sync audit", func(ports *terminalLockRecoveryPorts) { ports.syncAudit = func(*os.File) error { return sentinel } }},
		{"close audit", func(ports *terminalLockRecoveryPorts) {
			ports.closeAudit = func(file *os.File) error { _ = file.Close(); return sentinel }
		}},
		{"release lock", func(ports *terminalLockRecoveryPorts) {
			ports.releaseLock = func(*cleanupTaskHandle) error { return sentinel }
		}},
	}
	for _, test := range tests {
		//nolint:paralleltest // The fixture's t.Setenv requires this subtest to stay serial.
		t.Run(test.name, func(t *testing.T) {
			options, lock, receipt := terminalLockRecoveryFixture(t)
			options.Apply = true
			ports := defaultTerminalLockRecoveryPorts()
			test.inject(&ports)
			if _, err := recoverTerminalOperationLock(context.Background(), options, ports); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
				t.Fatalf("boundary error = %v, want sentinel", err)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Fatalf("boundary failure changed operation lock: %v", err)
			}
			if actual, err := os.ReadFile(options.ReceiptPath); err != nil || string(actual) != string(receipt) {
				t.Fatalf("boundary failure changed historical receipt: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Every case creates a real Git fixture that calls t.Setenv.
func TestTerminalLockRecoveryRefusesRemainingCandidatesAndBacklog(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*terminalLockRecoveryPorts)
		want   string
	}{
		{"candidate", func(ports *terminalLockRecoveryPorts) {
			ports.list = func(context.Context, ListOptions) (ListOutcome, error) {
				return ListOutcome{Results: []ListResult{{}}}, nil
			}
		}, "worktree candidates"},
		{"backlog", func(ports *terminalLockRecoveryPorts) {
			ports.hasBacklog = func(context.Context, wbhome.Resolution, string, string, string) (bool, error) { return true, nil }
		}, "cleanup backlog"},
	}
	for _, test := range tests {
		//nolint:paralleltest // The fixture's t.Setenv requires this subtest to stay serial.
		t.Run(test.name, func(t *testing.T) {
			options, lock, _ := terminalLockRecoveryFixture(t)
			options.Apply = true
			ports := defaultTerminalLockRecoveryPorts()
			test.inject(&ports)
			if _, err := recoverTerminalOperationLock(context.Background(), options, ports); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("recovery error = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Fatalf("refusal changed lock: %v", err)
			}
		})
	}
}

//nolint:paralleltest // The real Git fixture scopes WB configuration through t.Setenv.
func TestTerminalLockRecoveryRequiresWritableAuditBeforeRetirement(t *testing.T) {
	options, lock, _ := terminalLockRecoveryFixture(t)
	options.Apply = true
	auditRoot := filepath.Join(options.ProjectsRoot, ".wb", "reports", "worktree-lock-recovery")
	if err := os.WriteFile(auditRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverTerminalOperationLock(context.Background(), options); err == nil {
		t.Fatal("unwritable audit path accepted")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("audit failure changed lock: %v", err)
	}
}

//nolint:paralleltest // The real Git fixture scopes WB configuration through t.Setenv.
func TestTerminalLockRecoveryReportsUnexpectedShellAfterAuditedRelease(t *testing.T) {
	options, _, _ := terminalLockRecoveryFixture(t)
	options.Apply = true
	ports := defaultTerminalLockRecoveryPorts()
	ports.removeShell = func(*cleanupTaskHandle) bool { return false }
	outcome, err := recoverTerminalOperationLock(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "lock retired but task shell was not empty") || outcome.AuditPath == "" {
		t.Fatalf("post-release error = %v, outcome = %#v", err, outcome)
	}
	if _, err := os.Stat(outcome.AuditPath); err != nil {
		t.Fatalf("post-release audit missing: %v", err)
	}
}
