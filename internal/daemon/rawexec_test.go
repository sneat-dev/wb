package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const enabledRawExecutionPolicy = `{"version":1,"allow_raw_daemon_execution":true}`

func TestLoadRawExecutionPolicyFailsClosed(t *testing.T) {
	projectsRoot := t.TempDir()
	externalRoot := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		allowed, err := LoadRawExecutionPolicy(filepath.Join(externalRoot, "missing.json"), projectsRoot)
		if err != nil || allowed {
			t.Fatalf("missing policy = %t, %v", allowed, err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(externalRoot, "malformed.json")
		writeRawExecutionPolicy(t, path, []byte(`{"version":`), 0o600)
		allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("malformed policy = %t, %v", allowed, err)
		}
	})

	t.Run("inside projects root", func(t *testing.T) {
		path := filepath.Join(projectsRoot, "policy.json")
		writeRawExecutionPolicy(t, path, []byte(enabledRawExecutionPolicy), 0o600)
		allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "outside projects root") {
			t.Fatalf("in-project policy = %t, %v", allowed, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is not generally available to unprivileged Windows users")
		}
		target := filepath.Join(externalRoot, "target.json")
		path := filepath.Join(externalRoot, "policy-link.json")
		writeRawExecutionPolicy(t, target, []byte(enabledRawExecutionPolicy), 0o600)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlink policy = %t, %v", allowed, err)
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose POSIX file modes")
		}
		path := filepath.Join(externalRoot, "wrong-mode.json")
		writeRawExecutionPolicy(t, path, []byte(enabledRawExecutionPolicy), 0o644)
		allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "want 600") {
			t.Fatalf("wrong-mode policy = %t, %v", allowed, err)
		}
	})
}

func TestLoadRawExecutionPolicyAcceptsProtectedExternalOptIn(t *testing.T) {
	projectsRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "policy.json")
	writeRawExecutionPolicy(t, path, []byte(enabledRawExecutionPolicy), 0o600)
	allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
	if err != nil || !allowed {
		t.Fatalf("protected external policy = %t, %v", allowed, err)
	}
}

func writeRawExecutionPolicy(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}
