package sessionlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedDirectoryPreservesObservationOrderAndIdentity(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"cwd", "target", "current", "different", "same"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			other := filepath.Join(root, "other")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("directory observation unavailable")
			var observations []string
			getwd := func() (string, error) {
				observations = append(observations, "cwd")
				if scope == "cwd" {
					return "", sentinel
				}
				if scope == "different" {
					return other, nil
				}
				return root, nil
			}
			stat := func(path string) (os.FileInfo, error) {
				observations = append(observations, path)
				if scope == "target" && len(observations) == 2 || scope == "current" && len(observations) == 3 {
					return nil, sentinel
				}
				return os.Stat(path)
			}
			err := verifyPinnedDirectory(root, getwd, stat)
			switch scope {
			case "cwd", "target":
				if !errors.Is(err, sentinel) {
					t.Fatalf("error = %v", err)
				}
			case "current", "different":
				if err == nil || err.Error() != "private launcher is not rooted in the pinned target worktree" {
					t.Fatalf("error = %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			want := 3
			if scope == "cwd" {
				want = 1
			}
			if scope == "target" {
				want = 2
			}
			if len(observations) != want || observations[0] != "cwd" {
				t.Fatalf("observation order = %v", observations)
			}
			if want > 1 && observations[1] != root {
				t.Fatalf("target not observed first: %v", observations)
			}
			if scope == "different" && !strings.HasSuffix(observations[2], "other") {
				t.Fatalf("current directory = %v", observations)
			}
		})
	}
}
