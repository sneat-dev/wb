//go:build e2e

package testenv

import (
	"os/exec"
	"strings"
	"testing"
)

// With the real Go and Git commands: the toolchain's cache stays where it was
// and Git still resolves the identity the ambient user's configuration gave it.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestE2EIsolatedUserKeepsTheGoCacheAndGitIdentity(t *testing.T) {
	// What the go command itself reports, before anything moves.
	reported, err := exec.Command("go", "env", "GOCACHE").Output()
	cache := strings.TrimSpace(string(reported))
	if err != nil || cache == "" {
		t.Fatalf("go env GOCACHE = %q, %v", cache, err)
	}
	ambientUser(t)
	// ambientUser replaced the home, so give the derivation the real cache
	// location the way a developer's environment would.
	t.Setenv("GOCACHE", cache)

	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	after, err := exec.Command("go", "env", "GOCACHE").Output()
	if got := strings.TrimSpace(string(after)); err != nil || got != cache {
		t.Fatalf("go env GOCACHE after isolation = %q, %v; want %q", got, err, cache)
	}
	for key, want := range map[string]string{"user.name": "Ambient Developer", "user.email": "ambient@example.test"} {
		output, err := exec.Command("git", "config", "--global", "--includes", "--get", key).Output()
		if got := strings.TrimSpace(string(output)); err != nil || got != want {
			t.Fatalf("git config --global %s = %q, %v; want %q", key, got, err, want)
		}
	}
}
