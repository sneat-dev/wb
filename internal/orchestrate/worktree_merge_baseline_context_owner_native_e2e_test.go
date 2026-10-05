//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EBaselineMaterializationKeepsExactTargetTreeAndFetchOrigin(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(f.canonical, "dependency.txt"), "new candidate content\n")
	writeEngineFile(t, filepath.Join(f.canonical, "candidate-only.txt"), "candidate-only\n")
	runEngineGit(t, f.canonical, "add", "dependency.txt", "candidate-only.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "create genuinely different candidate tree")
	candidate := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	if candidate == target {
		t.Fatal("candidate did not advance beyond exact target")
	}
	wantOrigin := strings.TrimSpace(runEngineGit(t, f.canonical, "remote", "get-url", "origin"))
	archive := filepath.Join(t.TempDir(), "target.tar")
	runEngineGit(t, f.canonical, "archive", "--format=tar", "--output="+archive, target)
	snapshot := filepath.Join(t.TempDir(), "tree")
	if err := extractWorktreeMergeArchive(archive, snapshot); err != nil {
		t.Fatal(err)
	}
	assertTarget := func() {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(snapshot, "dependency.txt"))
		if err != nil || string(body) != "old\n" {
			t.Fatalf("exact target file = %q,%v", body, err)
		}
		info, err := os.Stat(filepath.Join(snapshot, "dependency.txt"))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("archived target mode = %v,%v", info, err)
		}
		entries, err := os.ReadDir(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "dependency.txt" && entry.Name() != ".git" {
				t.Fatalf("candidate-only/unarchived file appeared in target: %s", entry.Name())
			}
		}
	}
	assertTarget()
	if err := configureWorktreeMergeBaselineRemote(t.Context(), f.canonical, snapshot, 5*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	assertTarget()
	if got := strings.TrimSpace(runEngineGit(t, snapshot, "remote", "get-url", "origin")); got != wantOrigin {
		t.Fatalf("baseline fetch origin = %q want %q", got, wantOrigin)
	}
	if got := strings.TrimSpace(runEngineGit(t, snapshot, "for-each-ref", "--format=%(refname)")); got != "" {
		t.Fatalf("baseline copied candidate refs: %q", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, snapshot, "ls-files")); got != "" {
		t.Fatalf("baseline indexed candidate files: %q", got)
	}
}

func TestE2EBaselineMaterializationReportsNativeContextRefusals(t *testing.T) {
	t.Parallel()
	// These rows only read this candidate; every mutable snapshot has its own root.
	candidate := newExplicitRootEngineFixture(t)
	for _, mode := range []string{"empty origin", "init refusal", "origin add refusal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := candidate
			if mode == "empty origin" {
				f = newExplicitRootEngineFixture(t)
				runEngineGit(t, f.canonical, "config", "remote.origin.url", " \t ")
			}
			originBefore := runEngineGit(t, f.canonical, "remote", "get-url", "origin")
			snapshot := t.TempDir()
			writeEngineFile(t, filepath.Join(snapshot, "target.txt"), "owned target archive bytes\n")
			want := ""
			switch mode {
			case "empty origin":
				if strings.TrimSpace(originBefore) != "" {
					t.Fatalf("physical empty origin construction returned %q", originBefore)
				}
				want = "candidate origin remote for target baseline is empty"
			case "init refusal":
				if err := os.WriteFile(filepath.Join(snapshot, ".git"), []byte("owned invalid Git context\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "initialize target baseline Git context: "
			case "origin add refusal":
				runEngineGit(t, snapshot, "init", "--quiet")
				runEngineGit(t, snapshot, "remote", "add", "origin", "https://example.test/owned-existing.git")
				want = "configure target baseline origin remote: "
			}
			err := configureWorktreeMergeBaselineRemote(t.Context(), f.canonical, snapshot, 5*time.Second, 0)
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("native %s = %v want prefix %q", mode, err, want)
			}
			if body, e := os.ReadFile(filepath.Join(snapshot, "target.txt")); e != nil || string(body) != "owned target archive bytes\n" {
				t.Fatalf("native refusal changed archived target: %q,%v", body, e)
			}
			if got := runEngineGit(t, f.canonical, "remote", "get-url", "origin"); got != originBefore {
				t.Fatalf("baseline refusal mutated candidate origin: %q != %q", got, originBefore)
			}
			switch mode {
			case "empty origin":
				if _, e := os.Stat(filepath.Join(snapshot, ".git")); !os.IsNotExist(e) {
					t.Fatalf("empty origin initialized snapshot: %v", e)
				}
			case "init refusal":
				if body, e := os.ReadFile(filepath.Join(snapshot, ".git")); e != nil || string(body) != "owned invalid Git context\n" {
					t.Fatalf("init refusal changed blocker: %q,%v", body, e)
				}
			case "origin add refusal":
				if got := strings.TrimSpace(runEngineGit(t, snapshot, "remote", "get-url", "origin")); got != "https://example.test/owned-existing.git" {
					t.Fatalf("origin add refusal replaced existing remote: %q", got)
				}
			}
		})
	}
}
