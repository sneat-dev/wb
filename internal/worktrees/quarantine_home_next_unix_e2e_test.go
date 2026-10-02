//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EQuarantineDefaultReportRetainsNativeHomeResolutionError(t *testing.T) {
	if os.Getenv("WB_QUARANTINE_HOME_CHILD") == "1" {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.Symlink(".wb", filepath.Join(home, ".wb")); err != nil {
			t.Fatal(err)
		}
		options, clone, ops := quarantineNextFixture(t)
		options.ReportDir = ""
		outcome, err := branchQuarantineWithOps(context.Background(), options, ops, nil)
		if err == nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "refused" || outcome.ReportPath != "" {
			t.Fatalf("home resolution failure=%+v %v", outcome, err)
		}
		if !gitRefExists(clone, "refs/heads/"+options.Branch) {
			t.Fatal("home resolution failure changed source")
		}
		return
	}
	t.Parallel()
	command := exec.Command(os.Args[0], "-test.run=^TestE2EQuarantineDefaultReportRetainsNativeHomeResolutionError$")
	command.Env = append(os.Environ(), "WB_QUARANTINE_HOME_CHILD=1")
	if f := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && f != nil && f.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+f.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+f.Value.String())
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("home child=%v\n%s", err, output)
	}
}

func TestE2EQuarantineReservationRetainsNativeParentFailures(t *testing.T) {
	t.Parallel()
	t.Run("file ancestor refuses parent creation", func(t *testing.T) {
		t.Parallel()
		parent := filepath.Join(t.TempDir(), "occupied")
		if err := os.WriteFile(parent, []byte("existing occupant"), 0600); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(parent, "reports", "run")
		err := reserveQuarantineReportDir(report)
		if err == nil || !strings.Contains(err.Error(), "create quarantine report parent ") {
			t.Fatalf("parent creation error=%v", err)
		}
		if raw, err := os.ReadFile(parent); err != nil || string(raw) != "existing occupant" {
			t.Fatalf("occupant=%q %v", raw, err)
		}
	})
	t.Run("ancestor sync refuses unreadable parent", func(t *testing.T) {
		t.Parallel()
		unreadableParent := filepath.Join(t.TempDir(), "unreadable")
		if err := os.Mkdir(unreadableParent, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unreadableParent, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(unreadableParent, 0700); err != nil {
				t.Error(err)
			}
		})
		report := filepath.Join(unreadableParent, "run")
		err := reserveQuarantineReportDir(report)
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native ancestor sync error=%v, want permission refusal", err)
		}
		if err := os.Chmod(unreadableParent, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(report); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("report reserved before durable parent sync: %v", err)
		}
	})
}
