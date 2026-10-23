//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2ERetiredStageInspectionRetainsOwnedBoundaryRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"open", "identity", "inventory"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			root := retiredStageNativeRoot(t)
			task := "task"
			name := ".wb-retired-stage-0123456789abcdef0123456789abcdef"
			path := filepath.Join(root, task, name)
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "evidence"), []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			saved := path + "-retained"
			hooks := retiredStageRecoveryHooks{}
			var held *os.File
			preserve := func() {
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
			}
			prefix := ""
			switch boundary {
			case "open":
				hooks.afterStageStat = preserve
				prefix = "cannot open retired stage"
			case "identity":
				hooks.afterStageOpen = func(file *os.File) {
					held = file
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				prefix = "cannot inspect retired stage identity"
			case "inventory":
				hooks.afterStageOpen = func(file *os.File) { held = file }
				hooks.beforeInventory = func() {
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
					preserve()
				}
				prefix = "cannot inventory retired stage"
			}
			result := inspectRetiredStageWithHooks(context.Background(), root, task, path, name, hooks)
			if result.Eligible || result.Applied || !strings.HasPrefix(result.Reason, prefix) {
				t.Fatalf("inspection=%+v", result)
			}
			if held != nil {
				if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("owned descriptor live=%v", err)
				}
			}
			if boundary != "identity" {
				path = saved
			}
			if raw, err := os.ReadFile(filepath.Join(path, "evidence")); err != nil || string(raw) != "retained" {
				t.Fatalf("retained evidence=%q %v", raw, err)
			}
		})
	}
}

func TestE2ERetiredStageResolvedLayoutsDeduplicateAndRetainReceiptFailure(t *testing.T) {
	t.Parallel()
	for _, failReceipt := range []bool{false, true} {
		name := "plan overlapping roots"
		if failReceipt {
			name = "archive before receipt refusal"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			home := retiredStageNativeRoot(t)
			root := filepath.Join(home, "worktrees")
			task := "task"
			stageName := ".wb-retired-stage-0123456789abcdef0123456789abcdef"
			stage := filepath.Join(root, task, stageName)
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			layout := wbhome.Layout{Home: home, WorktreesRoot: root}
			resolution := wbhome.Resolution{Write: layout, Read: []wbhome.Layout{layout, layout}}
			var inj *filewrite.Injector
			cause := errors.New("receipt write refused")
			if failReceipt {
				inj = &filewrite.Injector{Step: filewrite.StepWrite, Err: cause}
			}
			outcome, err := recoverRetiredStagesResolved(context.Background(), RetiredStageRecoveryOptions{Task: task, Apply: failReceipt}, resolution, inj)
			if len(outcome.Results) != 1 {
				t.Fatalf("overlapping layouts duplicated stage: %+v %v", outcome, err)
			}
			result := outcome.Results[0]
			if !failReceipt {
				if err != nil || result.Applied || !result.Eligible {
					t.Fatalf("plan=%+v %v", outcome, err)
				}
				return
			}
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), "write private stage recovery receipt") || !result.Applied || result.ArchivePath == "" {
				t.Fatalf("partial archival outcome=%+v %v", outcome, err)
			}
			if raw, err := os.ReadFile(filepath.Join(result.ArchivePath, "evidence")); err != nil || string(raw) != "retained" {
				t.Fatalf("durable archive=%q %v", raw, err)
			}
			if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archived stage remains=%v", err)
			}
			if _, err := os.Stat(outcome.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed receipt published=%v", err)
			}
		})
	}
}

func retiredStageNativeRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestE2ERetiredStageResolvedLayoutsOrderRealRootsAndStagePaths(t *testing.T) {
	t.Parallel()
	home := retiredStageNativeRoot(t)
	roots := []string{filepath.Join(home, "z-root"), filepath.Join(home, "a-root")}
	names := []string{".wb-retired-stage-22222222222222222222222222222222", ".wb-retired-stage-11111111111111111111111111111111"}
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: home}}
	for _, root := range roots {
		resolution.Read = append(resolution.Read, wbhome.Layout{Home: home, WorktreesRoot: root})
		for _, name := range names {
			stage := filepath.Join(root, "task", name)
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte(root+name), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	outcome, err := recoverRetiredStagesResolved(context.Background(), RetiredStageRecoveryOptions{Task: "task"}, resolution, nil)
	if err != nil || len(outcome.Results) != 4 {
		t.Fatalf("multi-root plan=%+v %v", outcome, err)
	}
	expected := []struct{ root, name string }{{roots[1], names[1]}, {roots[1], names[0]}, {roots[0], names[1]}, {roots[0], names[0]}}
	for index, want := range expected {
		result := outcome.Results[index]
		if result.WorktreesRoot != want.root || result.Path != filepath.Join(want.root, "task", want.name) || result.Stage != want.name || !result.Eligible || result.Applied {
			t.Fatalf("ordered result[%d]=%+v wantroot=%q stage=%q", index, result, want.root, want.name)
		}
		if raw, err := os.ReadFile(filepath.Join(result.Path, "evidence")); err != nil || string(raw) != want.root+want.name {
			t.Fatalf("retained stage[%d]=%q %v", index, raw, err)
		}
	}
}
