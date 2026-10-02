package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestShellResidueNextInspectionPreservesRetiredLockPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	taskPath := filepath.Join(root, "task")
	if err := os.Mkdir(taskPath, 0700); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(taskPath, ".wb-retired-lock-native")
	if err := os.WriteFile(retired, []byte("terminal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	eligible, reason := taskShellIsEmpty(taskPath, "task", false)
	if !eligible {
		t.Fatalf("regular terminal lock refused: %s", reason)
	}
	if got, err := os.ReadFile(retired); err != nil || string(got) != "terminal bytes" {
		t.Fatalf("inspection mutated terminal bytes: %q %v", got, err)
	}
	namespaces, err := emptyTaskNamespaces([]wbhome.Layout{{WorktreesRoot: root}}, nil, "", t.TempDir(), nil)
	if err != nil || len(namespaces) != 0 {
		t.Fatalf("empty namespace discovery=%+v %v", namespaces, err)
	}
}

func TestShellResidueNextTaskInspectionRetainsNativeDisappearances(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		phase shellInspectionPhase
		want  string
	}{
		{"entry_disappeared", shellAfterTaskEnumeration, "stat owner:"},
		{"owner_disappeared", shellBeforeOwnerInspection, "inspect task/owner:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			taskPath := t.TempDir()
			owner := filepath.Join(taskPath, "owner")
			if err := os.Mkdir(owner, 0700); err != nil {
				t.Fatal(err)
			}
			observed := false
			eligible, reason := taskShellIsEmptyObserved(taskPath, "task", false, func(phase shellInspectionPhase, _ string) {
				if phase == tc.phase {
					observed = true
					if err := os.Remove(owner); err != nil {
						t.Fatal(err)
					}
				}
			})
			if !observed || eligible || !strings.Contains(reason, tc.want) {
				t.Fatalf("disappearance granted shell retirement: %v %q observed=%v", eligible, reason, observed)
			}
			if _, err := os.Stat(owner); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inspection recreated removed owner: %v", err)
			}
			if _, err := os.Stat(taskPath); err != nil {
				t.Fatalf("inspection removed task: %v", err)
			}
		})
	}
}

func TestShellResidueNextOwnerInspectionRetainsNativeRepositoryRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		phase   shellInspectionPhase
		replace bool
	}{
		{"repository_disappeared", shellAfterOwnerEnumeration, false},
		{"repository_became_file", shellBeforeRepositoryEnumeration, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			owner := t.TempDir()
			repository := filepath.Join(owner, "repository")
			if err := os.Mkdir(repository, 0700); err != nil {
				t.Fatal(err)
			}
			observed := false
			var cause error
			empty, err := ownerDirectoryIsProvablyEmptyObserved(owner, func(phase shellInspectionPhase, _ string) {
				if phase != tc.phase {
					return
				}
				observed = true
				if err := os.Remove(repository); err != nil {
					t.Fatal(err)
				}
				if tc.replace {
					if err := os.WriteFile(repository, []byte("replacement bytes"), 0600); err != nil {
						t.Fatal(err)
					}
					_, cause = os.ReadDir(repository)
				} else {
					_, cause = os.Lstat(repository)
				}
				if cause == nil {
					t.Fatal("native mutation did not refuse repository inspection")
				}
			})
			var native *os.PathError
			if !observed || empty || !errors.As(cause, &native) || !errors.Is(err, native.Err) {
				t.Fatalf("repository refusal lost: %v %v native=%v observed=%v", empty, err, cause, observed)
			}
			if tc.replace {
				if got, err := os.ReadFile(repository); err != nil || string(got) != "replacement bytes" {
					t.Fatalf("replacement removed: %q %v", got, err)
				}
			}
		})
	}
}
