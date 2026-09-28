package worktrees

import (
	"path/filepath"
	"testing"
)

func TestUnderPathRequiresExactOrNestedPath(t *testing.T) {
	t.Parallel()
	root := filepath.Join("root", "worktrees")
	for _, test := range []struct {
		name, candidate, path string
		want                  bool
	}{
		{name: "empty candidate", candidate: "", path: root},
		{name: "empty path", candidate: root, path: ""},
		{name: "same after cleaning", candidate: filepath.Join(root, ".", "one", ".."), path: root, want: true},
		{name: "nested", candidate: filepath.Join(root, "one"), path: root, want: true},
		{name: "sibling prefix", candidate: root + "-other", path: root},
		{name: "parent", candidate: "root", path: root},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := underPath(test.candidate, test.path); got != test.want {
				t.Fatalf("underPath(%q, %q) = %t, want %t", test.candidate, test.path, got, test.want)
			}
		})
	}
}
