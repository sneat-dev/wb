package wbhome

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAbsPreservesUnavailableWorkingDirectory(t *testing.T) {
	t.Parallel()
	failure := errors.New("working directory unavailable")
	resolved, err := resolveAbsWith("relative/home", func(string) (string, error) { return "", failure }, filepath.EvalSymlinks)
	if resolved != "" || !errors.Is(err, failure) {
		t.Fatalf("resolution=%q,%v", resolved, err)
	}
}

func TestResolveAbsStopsAtMissingRootAndPreservesParentFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "home")
	for _, failParent := range []bool{false, true} {
		name := "unavailable volume root"
		if failParent {
			name = "parent permission denied"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			visited := []string{}
			resolved, err := resolveAbsWith(path, filepath.Abs, func(current string) (string, error) {
				visited = append(visited, current)
				if failParent && current == filepath.Dir(path) {
					return "", os.ErrPermission
				}
				return "", os.ErrNotExist
			})
			if failParent {
				if resolved != "" || !errors.Is(err, os.ErrPermission) {
					t.Fatalf("parent failure=%q,%v", resolved, err)
				}
			} else {
				if err != nil || resolved != path || len(visited) == 0 || filepath.Dir(visited[len(visited)-1]) != visited[len(visited)-1] {
					t.Fatalf("root resolution=%q,%v;visited=%v", resolved, err, visited)
				}
			}
		})
	}
}
