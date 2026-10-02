package hooks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecureHooksGitHelperResolutionPreservesRefusalContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ stage, prefix string }{
		{"executable", "locate WB hooks helper"},
		{"git", "locate Git for hooks configuration"},
		{"absolute", "make Git path absolute for hooks configuration"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			t.Parallel()
			directory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			refused := errors.New("native resolver unavailable")
			resolver := secureHooksGitResolver{os.Executable, exec.LookPath, filepath.Abs}
			switch tc.stage {
			case "executable":
				resolver.executable = func() (string, error) { return "", refused }
			case "git":
				resolver.lookPath = func(name string) (string, error) {
					if name != "git" {
						t.Fatalf("lookup=%q", name)
					}
					return "", refused
				}
			case "absolute":
				resolver.absolute = func(path string) (string, error) {
					if path == "" {
						t.Fatal("absolute resolver missing looked-up Git path")
					}
					return "", refused
				}
			}
			err = setHooksPathAtResolved(directory, directory, "owned-hooks", resolver)
			if !errors.Is(err, refused) || !strings.Contains(err.Error(), tc.prefix) {
				t.Fatalf("helper error=%v", err)
			}
			if _, err := directory.Stat(); err != nil {
				t.Fatalf("borrowed directory authority closed: %v", err)
			}
		})
	}
}
