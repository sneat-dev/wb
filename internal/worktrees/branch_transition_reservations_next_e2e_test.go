//go:build e2e

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2EBranchTransitionReservationReleaseAndShellRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"release", "occupied-shell"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			home, run := newPreApplyReservationFixture(t)
			root := filepath.Join(home, "worktrees")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			held, err := acquireCleanupTaskAtOrCreate(root, "destination")
			if err != nil {
				t.Fatal(err)
			}
			if err := held.lock.release(); err != nil {
				t.Fatal(err)
			}
			held.close()
			hit := false
			opts := AbortOptions{Apply: true, beforeReservationRelease: func(task *cleanupTaskHandle) {
				hit = true
				if boundary == "release" {
					if err := task.lock.file.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(task.taskPath, "new-content"), []byte("retain new occupant"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}}
			results, matched, err := abortPreApplyRenameReservations(wbhome.Resolution{Write: wbhome.Layout{Home: home}}, "destination", opts)
			want := "release pre-apply"
			if boundary == "occupied-shell" {
				want = "shell changed"
			}
			if !hit || !matched || len(results) != 1 || results[0].Applied || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native terminal boundary = %+v, %v, %v, hit=%v", results, matched, err, hit)
			}
			if raw, err := os.ReadFile(filepath.Join(run, "original-prompt.txt")); err != nil || string(raw) != "exact recycle prompt\n" {
				t.Fatalf("terminal refusal changed prompt: %q, %v", raw, err)
			}
			if boundary == "occupied-shell" {
				if raw, err := os.ReadFile(filepath.Join(root, "destination", "new-content")); err != nil || string(raw) != "retain new occupant" {
					t.Fatalf("late occupant changed: %q, %v", raw, err)
				}
			}
		})
	}
}

func TestE2EBranchTransitionReservationReadRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"validation", "seek", "read"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			held, err := acquireCleanupTaskAtOrCreate(root, "read-boundary")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(held.close)
			if boundary == "validation" {
				if err := os.Rename(held.taskPath, held.taskPath+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(held.taskPath, 0700); err != nil {
					t.Fatal(err)
				}
				if err := preApplyReservationShellOnly(held); err == nil || !strings.Contains(err.Error(), "task path changed") {
					t.Fatalf("native validation refusal = %v", err)
				}
				return
			}
			var nativeCause error
			hit := false
			err = preApplyReservationShellWithReadObservation(held, func(stage string, file *os.File) {
				if stage != boundary {
					return
				}
				hit = true
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				if stage == "seek" {
					_, nativeCause = file.Seek(0, 0)
				} else {
					_, nativeCause = file.ReadDir(-1)
				}
			})
			if pathErr, ok := nativeCause.(*os.PathError); ok {
				nativeCause = pathErr.Err
			}
			if !hit || nativeCause == nil || !errors.Is(err, nativeCause) {
				t.Fatalf("actual owned %s cause = %v, control=%v hit=%v", boundary, err, nativeCause, hit)
			}
		})
	}
}

func TestE2EBranchTransitionReservationRecoversDeadOwnedLock(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	task := filepath.Join(root, "interrupted")
	if err := os.MkdirAll(task, 0700); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("operation=interrupted\npid=%d\n", killedLifecycleProcessPID(t))
	if err := os.WriteFile(filepath.Join(task, ".lock"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := acquirePreApplyReservationTask(wbhome.Resolution{Write: wbhome.Layout{Home: home}, Read: []wbhome.Layout{{Home: home, WorktreesRoot: root}}}, "interrupted")
	if err != nil || held == nil {
		t.Fatalf("native dead owner recovery = %v, %v", held, err)
	}
	t.Cleanup(held.close)
	if err := held.validateHeldLock(); err != nil {
		t.Fatalf("recovered native ownership invalid: %v", err)
	}
	if err := held.lock.release(); err != nil {
		t.Fatalf("recovered native release: %v", err)
	}
}
