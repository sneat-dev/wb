//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2EPlacementResidueTraversesHostedAndLegacyLayouts(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	root := filepath.Join(projectsRoot, ".worktrees")
	task := "placement-residue"
	hosted := filepath.Join(root, task, "github.com:8443", "acme", "lost")
	legacy := filepath.Join(root, task, "acme", "registered")
	unregisteredLegacy := filepath.Join(root, task, "acme", "unregistered")
	dotted := filepath.Join(root, task, "github.com:8443", "acme", ".notes")
	for _, path := range []string{hosted, legacy, unregisteredLegacy, dotted, filepath.Join(root, ".hidden", "acme", "ignored"),
		filepath.Join(root, task, "github.com:8443", ".hidden", "ignored")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /missing/registration\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, task, "ordinary-file"), []byte("not a repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, task, "github.com:8443", "ordinary-file"), []byte("not an organization"), 0o600); err != nil {
		t.Fatal(err)
	}
	residue := residueSweep(projectsRoot, map[string]string{root: LayoutCurrent}, map[string]bool{legacy: true})
	if len(residue) != 2 {
		t.Fatalf("hosted and unregistered legacy residue: %+v", residue)
	}
	byPath := make(map[string]OrphanResidue, len(residue))
	for _, item := range residue {
		byPath[item.Path] = item
	}
	for path, repository := range map[string]string{hosted: "github.com:8443/acme/lost", unregisteredLegacy: "acme/unregistered"} {
		item, found := byPath[path]
		if !found || item.Repository != repository || item.Task != task || item.Layout != LayoutCurrent ||
			len(item.Evidence) == 0 || item.Remedy == "" {
			t.Fatalf("residue %q not bound to its exact placement: %+v", path, residue)
		}
	}
	if _, err := os.Stat(hosted); err != nil {
		t.Fatalf("read-only residue sweep removed checkout: %v", err)
	}

	badLocal := filepath.Join(projectsRoot, "not-a-canonical-clone", ".worktrees")
	if err := os.MkdirAll(filepath.Join(badLocal, task), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := residueSweep(projectsRoot, map[string]string{badLocal: LayoutLocal}, nil); len(got) != 0 {
		t.Fatalf("unbound local layout yielded residue: %+v", got)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EPlacementListingPreservesMalformedAndAdoptedFilterBoundaries(t *testing.T) {
	fixture := newGitFixture(t)
	projectsRoot := fixture.projectsRoot
	root := filepath.Join(projectsRoot, ".worktrees")
	task := "placement-list"
	invalidRepository := filepath.Join(root, task, "acme", "bad name")
	invalidOrganization := filepath.Join(root, task, "github.com:8443", "bad name", "app")
	hiddenOrganization := filepath.Join(root, task, "github.com:8443", ".hidden", "app")
	registration := filepath.Join(root, task, "acme", "registration-only")
	for _, path := range []string{invalidRepository, invalidOrganization, hiddenOrganization, registration} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	external := fixture.externalWorktree(t, "feature/placement-filter-proof")
	pointer, err := json.Marshal(adoptedWorktreePointer{Version: 1, Worktree: external})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registration, adoptedWorktreePointerName), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	newListing := func(filter string) *layoutListing {
		return &layoutListing{ctx: context.Background(), projectsRoot: projectsRoot, home: filepath.Join(projectsRoot, ".wb"),
			layout: wbhome.Layout{WorktreesRoot: root}, filter: filter}
	}
	unfiltered := newListing("")
	unfiltered.walkTasks(entries)
	foundInvalid := false
	foundExternal := false
	for _, diagnostic := range unfiltered.diagnostics {
		if diagnostic.Path == invalidRepository && strings.Contains(diagnostic.Message, "invalid repository directory name") {
			foundInvalid = true
		}
		if diagnostic.Path == invalidOrganization || strings.HasPrefix(diagnostic.Path, invalidOrganization+string(filepath.Separator)) ||
			diagnostic.Path == hiddenOrganization || strings.HasPrefix(diagnostic.Path, hiddenOrganization+string(filepath.Separator)) {
			t.Fatalf("invalid or hidden organization descended into: %+v", diagnostic)
		}
	}
	for _, candidate := range unfiltered.pending {
		if candidate.path == external && candidate.external {
			foundExternal = true
		}
		if candidate.path == invalidOrganization || strings.HasPrefix(candidate.path, invalidOrganization+string(filepath.Separator)) ||
			candidate.path == hiddenOrganization || strings.HasPrefix(candidate.path, hiddenOrganization+string(filepath.Separator)) {
			t.Fatalf("invalid or hidden organization queued for inspection: %+v", candidate)
		}
	}
	if !foundInvalid {
		t.Fatalf("invalid repository lacked diagnostic: %+v", unfiltered.diagnostics)
	}
	if !foundExternal {
		t.Fatalf("valid adopted Git worktree was not queued without filter: %+v", unfiltered.pending)
	}
	// The registration's full path contains the task, while its slug and the
	// external checkout do not. The second filter must still refuse it.
	filtered := newListing(task)
	filtered.walkTasks(entries)
	for _, candidate := range filtered.pending {
		if candidate.path == external {
			t.Fatalf("registration pointer's external path bypassed second filter: %+v", filtered.pending)
		}
	}
	for _, diagnostic := range filtered.diagnostics {
		if diagnostic.Path == registration {
			t.Fatalf("filtered adoption pointer was inspected: %+v", diagnostic)
		}
	}
}

func TestE2EPlacementWalkReadFailuresStayScoped(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	root := filepath.Join(projectsRoot, ".worktrees")
	task := "placement-unreadable"
	unreadableOwner := filepath.Join(root, task, "unreadable-owner")
	unreadableOrganization := filepath.Join(root, task, "github.com:8443", "unreadable-org")
	unreadableHost := filepath.Join(root, task, "github.com:9443")
	unreadableTask := filepath.Join(root, "unreadable-task")
	for _, path := range []string{unreadableOwner, unreadableOrganization, unreadableHost, unreadableTask} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	}
	if _, err := os.ReadDir(unreadableOwner); err == nil {
		t.Skip("filesystem permits reads without directory permissions")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	listing := &layoutListing{ctx: context.Background(), projectsRoot: projectsRoot,
		home: filepath.Join(projectsRoot, ".wb"), layout: wbhome.Layout{WorktreesRoot: root}}
	listing.walkTasks(entries)
	for _, path := range []string{unreadableOwner, unreadableOrganization} {
		found := false
		for _, diagnostic := range listing.diagnostics {
			if diagnostic.Path == path && strings.Contains(diagnostic.Message, "read ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("unreadable path %q lacked scoped list diagnostic: %+v", path, listing.diagnostics)
		}
	}
	if got := residueSweep(projectsRoot, map[string]string{root: LayoutCurrent}, nil); len(got) != 0 {
		t.Fatalf("residue sweep should silently skip unreadable branches: %+v", got)
	}
}

func TestE2EPlacementWalkSilentlySkipsVanishedTask(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	root := filepath.Join(projectsRoot, ".worktrees")
	taskRoot := filepath.Join(root, "removed-task")
	if err := os.MkdirAll(taskRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(taskRoot); err != nil {
		t.Fatal(err)
	}
	listing := &layoutListing{ctx: context.Background(), projectsRoot: projectsRoot,
		home: filepath.Join(projectsRoot, ".wb"), layout: wbhome.Layout{WorktreesRoot: root}}
	listing.walkTasks(entries)
	if len(listing.pending) != 0 || len(listing.diagnostics) != 0 || len(listing.artifacts) != 0 || len(listing.purged) != 0 {
		t.Fatalf("vanished task produced listing output: pending=%+v diagnostics=%+v artifacts=%+v purged=%+v",
			listing.pending, listing.diagnostics, listing.artifacts, listing.purged)
	}
}

//nolint:paralleltest // seedCloneAt sets process-wide Git environment.
func TestE2EPlacementAmbiguousCanonicalFallbackStaysNonblocking(t *testing.T) {
	projectsRoot := t.TempDir()
	root := filepath.Join(projectsRoot, ".worktrees")
	candidate := filepath.Join(root, "legacy-task", "acme", "app")
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		t.Fatal(err)
	}
	github := newHostLevelClone(t, projectsRoot, "github.com", "acme", "app")
	gitlab := newHostLevelClone(t, projectsRoot, "gitlab.com", "acme", "app")
	if _, err := CanonicalRepositoryPath(projectsRoot, "acme/app"); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("ambiguous canonical repository was accepted: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	listing := &layoutListing{ctx: context.Background(), projectsRoot: projectsRoot,
		home: filepath.Join(projectsRoot, ".wb"), layout: wbhome.Layout{WorktreesRoot: root}}
	listing.walkTasks(entries)
	if len(listing.pending) != 0 || len(listing.diagnostics) != 1 {
		t.Fatalf("ambiguous non-Git candidate inspection = pending=%+v diagnostics=%+v", listing.pending, listing.diagnostics)
	}
	diagnostic := listing.diagnostics[0]
	if diagnostic.Path != candidate || !diagnostic.NonBlocking || !strings.Contains(diagnostic.Message, "foreign non-Git debris") {
		t.Fatalf("ambiguous canonical fallback diagnostic = %+v", diagnostic)
	}
	for _, path := range []string{candidate, filepath.Join(github, ".git"), filepath.Join(gitlab, ".git")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("read-only listing removed %q: %v", path, err)
		}
	}
}
