package orchestrate

import (
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandingCleanupRefusesUnverifiableLinkEvidence(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"malformed workspace", "corrupt stream"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			projects, checkout := t.TempDir(), t.TempDir()
			var evidence string
			if fault == "malformed workspace" {
				evidence = filepath.Join(checkout, "go.work")
			} else {
				store, err := streams.Open(projects)
				if err != nil {
					t.Fatal(err)
				}
				evidence = filepath.Join(store.Root, "broken", "stream.json")
				if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
					t.Fatal(err)
				}
			}
			original := []byte("{malformed\n")
			if err := os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
			refusal := refuseLinkedWorktree(projects, worktrees.ListResult{Task: "candidate", Repository: "acme/app", WorktreeDir: checkout})
			if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, evidence) || !strings.Contains(refusal.command, "inspect") {
				t.Fatalf("unverifiable %s allowed cleanup: %+v", fault, refusal)
			}
			raw, err := os.ReadFile(evidence)
			if err != nil || string(raw) != string(original) {
				t.Fatalf("evidence changed: %q %v", raw, err)
			}
			if info, err := os.Stat(checkout); err != nil || !info.IsDir() {
				t.Fatal("checkout deleted", err)
			}
		})
	}
}

func TestLandingCleanupHomeResolutionFallbackStillChecksWorkspace(t *testing.T) {
	t.Parallel()
	projects := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(projects, projects); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "go.work"), []byte("{malformed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout})
	if refusal == nil || refusal.code != "cleanup-unverifiable" {
		t.Fatalf("home fallback missed malformed workspace: %+v", refusal)
	}
}
