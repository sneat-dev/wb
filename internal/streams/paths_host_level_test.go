package streams

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCanonicalPathFollowsTheMachinePlacement pins the preflight's clone
// lookup. A fleet that has adopted the literal host level keeps its clones at
// <root>/{host}/{org}/{repo}; a flat join would name a path nothing is at, and
// every readiness check for that member would degrade to unknown.
func TestCanonicalPathFollowsTheMachinePlacement(t *testing.T) {
	t.Parallel()
	hostRoot := t.TempDir()
	hosted := filepath.Join(hostRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(hosted, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalPath(hostRoot, "acme/app"); got != hosted {
		t.Fatalf("canonicalPath = %q, want the host-level clone %q", got, hosted)
	}

	legacyRoot := t.TempDir()
	legacy := filepath.Join(legacyRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(legacy, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalPath(legacyRoot, "acme/app"); got != legacy {
		t.Fatalf("canonicalPath = %q, want the legacy clone %q", got, legacy)
	}

	emptyRoot := t.TempDir()
	if got := canonicalPath(emptyRoot, "acme/app"); got != filepath.Join(emptyRoot, "acme", "app") {
		t.Fatalf("canonicalPath with no clone = %q, want the legacy prediction", got)
	}

	// A host-qualified member resolves directly, and a bare name keeps the
	// historical flat join.
	if got := canonicalPath("/projects", "git.example.test/acme/app"); got != filepath.Join("/projects", "git.example.test", "acme", "app") {
		t.Fatalf("host-qualified canonicalPath = %q", got)
	}
	if got := canonicalPath("/projects", "bare"); got != filepath.Join("/projects", "bare") {
		t.Fatalf("canonicalPath without an owner = %q", got)
	}

	// The same owner/repository on two forges cannot be resolved from a bare
	// slug at all, so the historical flat join is kept rather than one forge
	// being picked silently.
	ambiguous := t.TempDir()
	for _, host := range []string{"github.com", "gitlab.example.test"} {
		if err := os.MkdirAll(filepath.Join(ambiguous, host, "acme", "app", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := canonicalPath(ambiguous, "acme/app"); got != filepath.Join(ambiguous, "acme", "app") {
		t.Fatalf("ambiguous canonicalPath = %q, want the flat fallback", got)
	}
}
