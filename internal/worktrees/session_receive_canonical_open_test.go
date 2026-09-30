package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitremote"
)

func TestSessionReceiveCanonicalOpenRefusesUnsafeDirectories(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "projects root symlink", want: "open projects root for target receive"},
		{name: "invalid parent", want: "invalid secure worktree parent segment"},
		{name: "canonical symlink", want: "inspect canonical clone for acme/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			projects := filepath.Join(root, "projects")
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			parent := "acme"
			if test.name == "projects root symlink" {
				if err := os.Symlink(outside, projects); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(projects, parent), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch test.name {
			case "invalid parent":
				parent = ".."
			case "canonical symlink":
				if err := os.Symlink(outside, filepath.Join(projects, parent, "app")); err != nil {
					t.Fatal(err)
				}
			}
			canonical, err := openOrCloneSessionReceiveCanonical(
				context.Background(), projects, parent, "app", filepath.Join(projects, parent, "app"), "",
				gitremote.Identity{Repository: "acme/app"},
			)
			if canonical != nil {
				canonical.close()
				t.Fatal("unsafe canonical directory was opened")
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refusal changed the outside directory: entries=%v err=%v", entries, err)
			}
		})
	}
}
