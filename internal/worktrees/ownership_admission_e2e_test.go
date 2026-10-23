//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//nolint:paralleltest // Actual adoption uses newGitFixture's process-wide Git/WB environment.
func TestE2EInventoryRetainsStaleAdoptionEvidenceAndHealthySibling(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	ctx := context.Background()
	var stale []AdoptResult
	for _, suffix := range []string{"z-missing", "a-non-git", "healthy"} {
		path := fixture.externalWorktree(t, "feature/admission-"+suffix)
		results, err := Adopt(ctx, AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
		if err != nil || len(results) != 1 || results[0].Action != AdoptAdopted {
			t.Fatalf("adopt %s = %+v, %v", suffix, results, err)
		}
		stale = append(stale, results[0])
		if suffix == "healthy" {
			continue
		}
		// Preserve the actual adopted checkout and its projection, leaving the
		// original registration spelling missing or occupied by non-Git data.
		if err := os.Rename(path, path+".preserved"); err != nil {
			t.Fatal(err)
		}
		if suffix == "a-non-git" {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "evidence"), []byte("preserve unrelated occupant\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Adoption leaves released operation-lock artifacts for the inventory's
	// normal sweep. Complete that fixture housekeeping before asserting that
	// admission preserves all remaining ownership and registration evidence.
	beforeHome := admissionFilesystemSnapshot(t, fixture.home)
	for _, record := range stale {
		purged := purgeTerminalArtefacts(filepath.Join(fixture.home, "worktrees"), record.Task)
		if len(purged) != 1 || purged[0].Task != record.Task || purged[0].Kind != purgedRetiredLock {
			t.Fatalf("adoption housekeeping removed unexpected artifacts: %+v", purged)
		}
		delete(beforeHome, purged[0].Path)
	}
	if got := admissionFilesystemSnapshot(t, fixture.home); !reflect.DeepEqual(got, beforeHome) {
		t.Fatal("adoption housekeeping changed evidence beyond the released operation locks")
	}
	healthy := stale[2]
	stale = stale[:2]
	beforeExternal := admissionFilesystemSnapshot(t, filepath.Join(filepath.Dir(fixture.projectsRoot), "external"))
	head := gitTestOutput(t, healthy.Path, "rev-parse", "HEAD")
	registry := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Purged) != 0 {
		t.Fatalf("admission unexpectedly purged additional artifacts: %+v", listed.Purged)
	}
	healthyFound := false
	for _, result := range listed.Results {
		if result.WorktreeDir == healthy.Path {
			if !result.External || result.Task != healthy.Task {
				t.Fatalf("healthy adoption lost external ownership: %+v", result)
			}
			healthyFound = true
		}
		for _, record := range stale {
			if result.WorktreeDir == record.Path || result.WorktreeDir == record.Path+".preserved" {
				t.Fatalf("stale adoption was admitted for cleanup: %+v", result)
			}
		}
	}
	if !healthyFound {
		t.Fatalf("healthy sibling disappeared: %+v", listed)
	}
	for index, diagnostic := range listed.Diagnostics {
		if index > 0 {
			previous := listed.Diagnostics[index-1]
			if previous.Task > diagnostic.Task || (previous.Task == diagnostic.Task && previous.Path > diagnostic.Path) {
				t.Fatalf("inventory diagnostics are not deterministically ordered: %+v", listed.Diagnostics)
			}
		}
	}
	for _, record := range stale {
		registration := filepath.Join(fixture.home, "worktrees", record.Task, "acme", "app")
		want := fmt.Sprintf("adopted worktree registration points at %s, which is no longer a Git worktree root", record.Path)
		found := false
		for _, diagnostic := range listed.Diagnostics {
			if diagnostic.Task == record.Task && diagnostic.Path == registration && diagnostic.Message == want && diagnostic.WorktreesRoot == filepath.Join(fixture.home, "worktrees") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing stale registration diagnostic for %+v: %+v", record, listed.Diagnostics)
		}
	}
	if got := admissionFilesystemSnapshot(t, fixture.home); !reflect.DeepEqual(got, beforeHome) {
		for path, before := range beforeHome {
			if got[path] != before {
				t.Errorf("inventory changed existing ownership entry: %s", path)
			}
		}
		for path := range got {
			if _, existed := beforeHome[path]; !existed {
				t.Errorf("inventory created ownership entry: %s", path)
			}
		}
		t.Fatal("inventory changed private ownership records or registration evidence")
	}
	if got := admissionFilesystemSnapshot(t, filepath.Join(filepath.Dir(fixture.projectsRoot), "external")); !reflect.DeepEqual(got, beforeExternal) {
		t.Fatal("inventory changed preserved checkout or replacement evidence")
	}
	if got := gitTestOutput(t, healthy.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("inventory changed healthy HEAD: %s -> %s", head, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != registry {
		t.Fatalf("inventory changed stale Git registry evidence: %s", got)
	}
}

func TestE2ELegacyProjectionPolicyRefusalPreservesUntrustedEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, lifecycle, claim string
		version                int
		want                   string
	}{
		{"version", "active", strings.Repeat("a", 64), 2, "invalid work-log projection identity"},
		{"claim", "active", "short", 1, "invalid work-log projection identity"},
		{"lifecycle", "recycled", strings.Repeat("a", 64), 1, "invalid work-log projection lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := wtLifeCovNewRepo(t)
			home := t.TempDir()
			projection := workLogProjection{Version: tc.version, EffortID: "effort", RunID: "run", ClaimID: tc.claim, Lifecycle: tc.lifecycle}
			encoded, err := json.MarshalIndent(projection, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			if err := os.WriteFile(filepath.Join(repo.path, legacyWorkLogProjectionName), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			beforeRepo, beforeHome := admissionFilesystemSnapshot(t, repo.path), admissionFilesystemSnapshot(t, home)
			got, err := readLegacyWorkLogProjection(repo.path)
			if got != projection || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("legacy projection policy = %+v, %v", got, err)
			}
			for _, read := range []func() (workLogProjection, error){
				func() (workLogProjection, error) { return readWorkLogProjectionForReadOnlyClaim(repo.path) },
				func() (workLogProjection, error) { return readWorkLogProjectionForClaim(home, repo.path) },
			} {
				if got, err := read(); got != (workLogProjection{}) || err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("legacy selection admitted untrusted policy: %+v, %v", got, err)
				}
			}
			if got := admissionFilesystemSnapshot(t, repo.path); !reflect.DeepEqual(got, beforeRepo) {
				t.Fatal("legacy refusal changed untrusted bytes or created a current projection")
			}
			if got := admissionFilesystemSnapshot(t, home); !reflect.DeepEqual(got, beforeHome) {
				t.Fatal("legacy refusal created private ownership records")
			}
		})
	}
}

type admissionFilesystemEntry struct {
	mode os.FileMode
	data string
}

// Snapshot names, modes and contents without following symlinks. The same
// assertion protects stale registrations and rejected legacy projections.
func admissionFilesystemSnapshot(t *testing.T, root string) map[string]admissionFilesystemEntry {
	t.Helper()
	entries := make(map[string]admissionFilesystemEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data string
		if info.Mode()&os.ModeSymlink != 0 {
			data, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var content []byte
			content, err = os.ReadFile(path)
			data = string(content)
		}
		if err != nil {
			return err
		}
		entries[path] = admissionFilesystemEntry{mode: info.Mode(), data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
