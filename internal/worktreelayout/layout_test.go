package worktreelayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/repopath"
)

func TestRepositoryAddressGrammar(t *testing.T) {
	t.Parallel()
	valid := []struct {
		input, host, owner, name string
	}{
		{"acme/app", "", "acme", "app"},
		{"acme/.github", "", "acme", ".github"},
		{"github.com/acme/app", "github.com", "acme", "app"},
		{"github.com:8443/acme/.github", "github.com:8443", "acme", ".github"},
	}
	for _, tc := range valid {
		address, err := SplitRepositoryAddress(tc.input)
		if err != nil || address != (repopath.Address{Host: tc.host, Org: tc.owner, Repo: tc.name}) {
			t.Errorf("SplitRepositoryAddress(%q) = %+v, %v", tc.input, address, err)
		}
		owner, name, err := SplitRepository(tc.input)
		if err != nil || owner != tc.owner || name != tc.name {
			t.Errorf("SplitRepository(%q) = %q, %q, %v", tc.input, owner, name, err)
		}
	}
	invalid := []struct{ input, errorPart string }{
		{" acme/app", "surrounding whitespace"},
		{"acme/app ", "surrounding whitespace"},
		{"acme", "owner/name or host/owner/name"},
		{"a/b/c/d", "owner/name or host/owner/name"},
		{".acme/app", "owner/name using safe path segments"},
		{"acme/..", "owner/name using safe path segments"},
		{"acme/bad name", "owner/name using safe path segments"},
		{"forge/acme/app", "literal forge hostname"},
		{"github.com/.acme/app", "literal forge hostname"},
	}
	for _, tc := range invalid {
		_, err := SplitRepositoryAddress(tc.input)
		if err == nil || !strings.Contains(err.Error(), tc.errorPart) {
			t.Errorf("SplitRepositoryAddress(%q) error = %v, want %q", tc.input, err, tc.errorPart)
		}
		if _, _, err := SplitRepository(tc.input); err == nil {
			t.Errorf("SplitRepository(%q) accepted invalid address", tc.input)
		}
	}
}

func TestSegmentsAndCloneRelativeGrammar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value       string
		owner, repo bool
	}{
		{"acme", true, true}, {".github", false, true},
		{".", false, false}, {"..", false, false},
		{"", false, false}, {"-bad", false, false},
		{"bad/name", false, false}, {"bad name", false, false},
	} {
		if got := ValidSafeSegment(tc.value); got != tc.owner {
			t.Errorf("ValidSafeSegment(%q) = %t, want %t", tc.value, got, tc.owner)
		}
		if got := ValidRepositorySegment(tc.value); got != tc.repo {
			t.Errorf("ValidRepositorySegment(%q) = %t, want %t", tc.value, got, tc.repo)
		}
	}
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"acme/app", true}, {"github.com/acme/.github", true},
		{"github.com:8443/acme/app", true}, {"forge/acme/app", false},
		{"acme", false}, {"a/b/c/d", false}, {"acme/..", false},
		{".acme/app", false}, {"/acme/app", false},
	} {
		if got := ValidCloneRelative(tc.value); got != tc.valid {
			t.Errorf("ValidCloneRelative(%q) = %t, want %t", tc.value, got, tc.valid)
		}
	}
	for _, tc := range []struct{ value, parent, repo string }{
		{" /github.com/acme/app/ ", "github.com/acme", "app"},
		{"acme/app", "acme", "app"}, {"app", "", "app"},
	} {
		parent, repo := SplitCloneRelative(tc.value)
		if parent != tc.parent || repo != tc.repo {
			t.Errorf("SplitCloneRelative(%q) = %q, %q", tc.value, parent, repo)
		}
	}
}

func TestPlacementPathGrammar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		local                            bool
		relative, task, repository, want string
		errorPart                        string
	}{
		{repository: "bad", task: "task", errorPart: "owner/name"},
		{repository: "acme/app", task: "../bad", errorPart: "invalid worktree task"},
		{local: true, repository: "acme/app", task: "task", want: filepath.Join(root, "task")},
		{repository: "github.com/acme/app", task: "task", want: filepath.Join(root, "task", "github.com", "acme", "app")},
		{repository: "acme/app", task: "task", relative: " /github.com/acme/.github/ ", want: filepath.Join(root, "task", "github.com", "acme", ".github")},
		{repository: "acme/app", task: "task", relative: "forge/acme/app", errorPart: "not a safe relative path"},
	} {
		got, err := Path(root, tc.local, tc.relative, tc.task, tc.repository)
		if tc.errorPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errorPart) {
				t.Errorf("Path(%q, %q) error = %v, want %q", tc.task, tc.repository, err, tc.errorPart)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("Path(%q, %q) = %q, %v, want %q", tc.task, tc.repository, got, err, tc.want)
		}
	}
}

func TestCanonicalCloneResolutionAndCoordinates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := repopath.Address{Org: "acme", Repo: "app"}
	hosted := repopath.Address{Host: "github.com", Org: "acme", Repo: "app"}
	if got, err := ResolveCanonicalClone(root, legacy); err != nil || got != legacy {
		t.Fatalf("missing clone = %+v, %v", got, err)
	}
	if got, err := ResolveCanonicalClone(root, hosted); err != nil || got != hosted {
		t.Fatalf("qualified clone = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		repository string
		want       repopath.Address
	}{
		{"acme/app", legacy}, {"github.com/acme/app", hosted},
	} {
		owner, name, path, err := CanonicalRepositoryPath(root, tc.repository)
		if err != nil || owner != "acme" || name != "app" || path != tc.want.Path(root) {
			t.Errorf("CanonicalRepositoryPath(%q) = %q, %q, %q, %v", tc.repository, owner, name, path, err)
		}
	}
	if _, _, _, err := CanonicalRepositoryPath(root, "bad"); err == nil {
		t.Error("invalid repository accepted")
	}
	if err := os.MkdirAll(filepath.Join(hosted.Path(root), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveCanonicalClone(root, legacy); err != nil || got != hosted {
		t.Fatalf("existing hosted clone = %+v, %v", got, err)
	}
	if _, _, path, err := CanonicalRepositoryPath(root, "acme/app"); err != nil || path != hosted.Path(root) {
		t.Fatalf("canonical hosted path = %q, %v", path, err)
	}
	other := repopath.Address{Host: "gitlab.com", Org: "acme", Repo: "app"}
	if err := os.MkdirAll(filepath.Join(other.Path(root), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCanonicalClone(root, legacy); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Errorf("ambiguous clone error = %v", err)
	}
	if _, _, _, err := CanonicalRepositoryPath(root, "acme/app"); err == nil {
		t.Error("ambiguous canonical path accepted")
	}
	for _, tc := range []struct {
		path, host, owner, name string
		valid                   bool
	}{
		{path: legacy.Path(root), owner: "acme", name: "app", valid: true},
		{path: hosted.Path(root), host: "github.com", owner: "acme", name: "app", valid: true},
		{path: filepath.Join(root, "forge", "acme", "app")},
		{path: filepath.Join(root, "acme")},
		{path: filepath.Join(root, "acme", "..")},
		{path: filepath.Join(root, ".acme", "app")},
	} {
		host, owner, name, err := CanonicalCoordinates(root, tc.path)
		if tc.valid {
			if err != nil || host != tc.host || owner != tc.owner || name != tc.name {
				t.Errorf("CanonicalCoordinates(%q) = %q, %q, %q, %v", tc.path, host, owner, name, err)
			}
		} else if err == nil {
			t.Errorf("CanonicalCoordinates(%q) accepted invalid path", tc.path)
		}
	}
	if _, _, _, err := CanonicalCoordinates(root, "relative/path"); err == nil {
		t.Error("mixed absolute and relative path accepted")
	}
}
