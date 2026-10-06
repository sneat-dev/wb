package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Git runs a fixture command with child-only automatic-maintenance protection.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = GitAutoMaintenanceOffEnv(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// CloneWithOrigin preserves seed history and an explicit clone identity while
// protecting the bare origin against detached server-side maintenance writers.
func CloneWithOrigin(t testing.TB, seedRoot, name, clonePath string) string {
	t.Helper()
	seed := filepath.Join(seedRoot, name+"-seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatalf("%v", err)
	}
	Git(t, seed, "init", "-b", "main")
	Git(t, seed, "config", "user.email", "wb@example.test")
	Git(t, seed, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("%v", err)
	}
	Git(t, seed, "add", ".")
	Git(t, seed, "commit", "-m", "init")
	remote := filepath.Join(seedRoot, name+".git")
	Git(t, seedRoot, "clone", "--bare", seed, remote)
	ConfigureGitAutoMaintenanceOff(t, remote)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatalf("%v", err)
	}
	Git(t, filepath.Dir(clonePath), "clone", remote, clonePath)
	Git(t, clonePath, "config", "user.email", "wb@example.test")
	Git(t, clonePath, "config", "user.name", "WB Test")
	return remote
}
