package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCanonicalClonePathFollowsTheCloneURL pins where a canonical clone belongs:
// the literal forge hostname from the clone URL when it names one, and the
// legacy two-level placement otherwise. The empty-URL case is the one place WB
// names a forge itself, and it must be the same fallback EnsureCanonical clones
// from — otherwise the predicted and the created path could disagree.
func TestCanonicalClonePathFollowsTheCloneURL(t *testing.T) {
	root := "/projects"
	for _, test := range []struct {
		slug     string
		cloneURL string
		want     string
	}{
		{slug: "acme/app", cloneURL: "git@github.com:acme/app.git", want: "github.com/acme/app"},
		{slug: "acme/app", cloneURL: "https://git.example.test/acme/app.git", want: "git.example.test/acme/app"},
		{slug: "acme/app", cloneURL: "https://github.com:8443/acme/app.git", want: "github.com:8443/acme/app"},
		{slug: "acme/app", cloneURL: "/srv/remotes/acme/app.git", want: "acme/app"},
		{slug: "acme/app", cloneURL: "file:///srv/remotes/acme/app.git", want: "acme/app"},
		{slug: "acme/app", cloneURL: "", want: "github.com/acme/app"},
	} {
		owner, name, err := splitRepository(test.slug)
		if err != nil {
			t.Fatal(err)
		}
		resolved := cloneURLFor(Repository{Slug: test.slug, CloneURL: test.cloneURL})
		got := canonicalClonePath(root, owner, name, resolved)
		if want := filepath.Join(root, filepath.FromSlash(test.want)); got != want {
			t.Fatalf("canonicalClonePath(%q) = %q, want %q", test.cloneURL, got, want)
		}
	}
}

// TestEnsureCanonicalClonesIntoTheHostLevelDerivedFromTheCloneURL proves the
// destination is real, not just arithmetic: a repository whose clone URL names
// github.com is cloned to <root>/github.com/{org}/{repo} and no flat duplicate
// is created. A git url.insteadOf rewrite stands in for the network.
func TestEnsureCanonicalClonesIntoTheHostLevelDerivedFromTheCloneURL(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	writeEngineFile(t, filepath.Join(seed, "file.txt"), "contents\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeEngineFile(t, filepath.Join(home, ".gitconfig"),
		"[url \""+remote+"\"]\n\tinsteadOf = git@github.com:acme/cloned.git\n")

	projectsRoot := filepath.Join(root, "projects")
	cloneURL := "git@github.com:acme/cloned.git"
	canonical := canonicalClonePath(projectsRoot, "acme", "cloned", cloneURL)
	if want := filepath.Join(projectsRoot, "github.com", "acme", "cloned"); canonical != want {
		t.Fatalf("canonicalClonePath = %q, want %q", canonical, want)
	}

	if _, err := EnsureCanonical(context.Background(),
		Repository{Slug: "acme/cloned", CloneURL: cloneURL}, canonical,
		Options{GitHubDir: projectsRoot, Ref: "main", Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if contents := mustReadEngineFile(t, filepath.Join(canonical, "file.txt")); contents != "contents\n" {
		t.Fatalf("cloned file = %q", contents)
	}
	if _, err := os.Stat(filepath.Join(projectsRoot, "acme", "cloned")); !os.IsNotExist(err) {
		t.Fatalf("a flat duplicate was created at %s", filepath.Join(projectsRoot, "acme", "cloned"))
	}
}
