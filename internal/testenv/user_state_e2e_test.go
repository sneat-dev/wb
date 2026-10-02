//go:build e2e

package testenv

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// With the real Go and Git commands: the toolchain's cache stays where it was
// and Git still resolves the identity the ambient user's configuration gave it.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestE2EIsolatedUserKeepsTheGoCacheAndGitIdentity(t *testing.T) {
	ambientUser(t)
	goEnvironmentValue = productionGoEnvironmentValue
	cache, err := goEnvironmentValue("GOCACHE")
	if err != nil || cache == "" {
		t.Fatalf("go env GOCACHE = %q, %v", cache, err)
	}
	t.Setenv("GOCACHE", "")

	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	if got := os.Getenv("GOCACHE"); got != cache {
		t.Fatalf("GOCACHE = %q after isolation, want %q", got, cache)
	}
	for key, want := range map[string]string{"user.name": "Ambient Developer", "user.email": "ambient@example.test"} {
		output, err := exec.Command("git", "config", "--global", "--includes", "--get", key).Output()
		if got := strings.TrimSpace(string(output)); err != nil || got != want {
			t.Fatalf("git config --global %s = %q, %v; want %q", key, got, err, want)
		}
	}
}
