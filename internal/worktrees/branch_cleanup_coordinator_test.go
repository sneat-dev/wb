package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestBranchCleanupCoordinatorFaultsPreserveReportOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	base := BranchCleanupOptions{ProjectsRoot: root, Scope: BranchScopeRemote, Apply: true, Now: func() time.Time { return now }}
	for _, tc := range []struct {
		name        string
		failWriteAt int
		wantWrites  int
		wantError   string
	}{
		{name: "initial report fails before apply", failWriteAt: 1, wantWrites: 1, wantError: "injected report failure"},
		{name: "final report failure propagates", failWriteAt: 2, wantWrites: 2, wantError: "injected report failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := base
			options.ReportDir = filepath.Join(t.TempDir(), "reports")
			writes := 0
			ports := branchCleanupCoordinatorPorts{
				resolveHome: wbhome.Resolve,
				writeReport: func(dir string, options BranchCleanupOptions, at time.Time, results []BranchCleanupResult) (string, error) {
					writes++
					if writes == tc.failWriteAt {
						return "", errors.New("injected report failure")
					}
					return writeBranchCleanupReport(dir, options, at, results)
				},
			}
			_, err := branchCleanupWithPorts(context.Background(), options, ports)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || writes != tc.wantWrites {
				t.Fatalf("coordinator fault: writes=%d err=%v", writes, err)
			}
			if tc.failWriteAt == 1 {
				if _, err := os.Stat(filepath.Join(options.ReportDir, "cleanup.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("initial report unexpectedly exists: %v", err)
				}
			}
		})
	}
	t.Run("WB home failure precedes the first report", func(t *testing.T) {
		t.Parallel()
		options := base
		options.ReportDir = ""
		ports := branchCleanupCoordinatorPorts{
			resolveHome: func(string) (wbhome.Resolution, error) {
				return wbhome.Resolution{}, errors.New("injected home failure")
			},
			writeReport: func(string, BranchCleanupOptions, time.Time, []BranchCleanupResult) (string, error) {
				t.Fatal("report written after home resolution failed")
				return "", nil
			},
		}
		_, err := branchCleanupWithPorts(context.Background(), options, ports)
		if err == nil || !strings.Contains(err.Error(), "resolve WB home for branch cleanup report: injected home failure") {
			t.Fatalf("home failure = %v", err)
		}
	})
	t.Run("missing repository selector fails during classification", func(t *testing.T) {
		t.Parallel()
		options := base
		options.Repository = "acme/missing"
		_, err := BranchCleanup(context.Background(), options)
		if err == nil || !strings.Contains(err.Error(), "acme/missing") {
			t.Fatalf("missing repository classification = %v", err)
		}
	})
	t.Run("invalid options refuse before classification or report", func(t *testing.T) {
		t.Parallel()
		options := base
		options.Scope = "invalid"
		_, err := BranchCleanup(context.Background(), options)
		if err == nil || !strings.Contains(err.Error(), "unsupported --scope") {
			t.Fatalf("invalid cleanup scope = %v", err)
		}
	})
}
