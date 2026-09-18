package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/repopath"
	"time"
)

func TestScanLocalIndexedFallsBackToAFreshScanWithoutCacheSettings(t *testing.T) {
	projectsRoot := t.TempDir()
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	for name, options := range map[string]LocalIndexOptions{
		"blank cache path":     {MaxAge: time.Minute},
		"non-positive max age": {CachePath: filepath.Join(t.TempDir(), "fleet-inventory.json")},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ScanLocalIndexed(projectsRoot, options)
			if err != nil {
				t.Fatal(err)
			}
			if result.CacheHit || len(result.Repositories) != 1 || result.SourceFingerprint != "" || len(result.Diagnostics) != 0 {
				t.Fatalf("uncached index = %#v", result)
			}
		})
	}
}

func TestScanLocalIndexedReportsASnapshotFailure(t *testing.T) {
	result, err := ScanLocalIndexed(filepath.Join(t.TempDir(), "missing"), LocalIndexOptions{
		CachePath: filepath.Join(t.TempDir(), "fleet-inventory.json"),
		MaxAge:    time.Minute,
	})
	if err == nil {
		t.Fatal("ScanLocalIndexed() on a missing projects root returned no error")
	}
	if result.CacheHit || len(result.Repositories) != 0 || result.SourceFingerprint != "" {
		t.Fatalf("failed snapshot index = %#v", result)
	}
}

func TestScanLocalIndexedReportsAProjectsRootThatIsNotADirectory(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	if err := os.WriteFile(projectsRoot, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ScanLocalIndexed(projectsRoot, LocalIndexOptions{
		CachePath: filepath.Join(t.TempDir(), "fleet-inventory.json"),
		MaxAge:    time.Minute,
	})
	if err == nil {
		t.Fatal("ScanLocalIndexed() on a non-directory projects root returned no error")
	}
	if result.CacheHit || len(result.Repositories) != 0 {
		t.Fatalf("failed snapshot index = %#v", result)
	}
}

func TestScanLocalIndexedSurvivesAnUnwritableCachePath(t *testing.T) {
	projectsRoot := t.TempDir()
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ScanLocalIndexed(projectsRoot, LocalIndexOptions{
		CachePath: filepath.Join(blocker, "fleet-inventory.json"),
		MaxAge:    time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHit || len(result.Repositories) != 1 {
		t.Fatalf("unwritable-cache index = %#v", result)
	}
	if !lgCovHasDiagnostic(result.Diagnostics, "write local fleet index") {
		t.Fatalf("diagnostics = %#v, want a write failure", result.Diagnostics)
	}
}

func TestScanLocalIndexedRescansATamperedCache(t *testing.T) {
	for name, tamper := range map[string]func([]Repo) []Repo{
		"clone url": func(repositories []Repo) []Repo {
			repositories[0].CloneURL = "git@github.com:acme/widgets.git"
			return repositories
		},
		"archived flag": func(repositories []Repo) []Repo {
			repositories[0].Archived = true
			return repositories
		},
		"local flag": func(repositories []Repo) []Repo {
			repositories[0].Local = true
			return repositories
		},
		"path outside the root": func(repositories []Repo) []Repo {
			repositories[0].Path = filepath.Join(string(filepath.Separator), "elsewhere", "acme", "widgets")
			return repositories
		},
		"duplicate entry": func(repositories []Repo) []Repo {
			return append(repositories, repositories[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			projectsRoot := t.TempDir()
			mustIndexedRepository(t, projectsRoot, "acme", "widgets")
			cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
			options := LocalIndexOptions{
				CachePath: cachePath, MaxAge: time.Minute,
				Now: func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
			}
			if _, err := ScanLocalIndexed(projectsRoot, options); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			var cached persistedLocalIndex
			if err := json.Unmarshal(contents, &cached); err != nil {
				t.Fatal(err)
			}
			cached.Repositories = tamper(cached.Repositories)
			if err := writeLocalIndex(cachePath, cached); err != nil {
				t.Fatal(err)
			}

			result, err := ScanLocalIndexed(projectsRoot, options)
			if err != nil {
				t.Fatal(err)
			}
			if result.CacheHit {
				t.Fatalf("tampered cache reused: %#v", result)
			}
			if len(result.Repositories) != 1 || result.Repositories[0].Slug() != "acme/widgets" || result.Repositories[0].CloneURL != "" {
				t.Fatalf("fresh rescan = %#v", result.Repositories)
			}
		})
	}
}

func TestScanLocalOrganizationsReturnsOnlyCanonicalClones(t *testing.T) {
	root := t.TempDir()
	mustIndexedRepository(t, root, "acme", "widgets")
	if err := os.MkdirAll(filepath.Join(root, "acme", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acme", "notes.txt"), []byte("not a repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "acme", "widgets-feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: ../widgets/.git/worktrees/widgets-feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	owners, _ := repopath.Owners(root)

	repositories, err := scanLocalOrganizations(owners)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Slug() != "acme/widgets" {
		t.Fatalf("scanLocalOrganizations() = %#v, want only the canonical clone", repositories)
	}
}

func TestScanLocalOrganizationsToleratesAVanishedOrganization(t *testing.T) {
	root := t.TempDir()
	mustIndexedRepository(t, root, "acme", "widgets")
	owners, _ := repopath.Owners(root)
	if err := os.RemoveAll(filepath.Join(root, "acme")); err != nil {
		t.Fatal(err)
	}

	repositories, err := scanLocalOrganizations(owners)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 0 {
		t.Fatalf("scanLocalOrganizations() = %#v, want none for a vanished organization", repositories)
	}
}

func TestSnapshotLocalSourceIgnoresNonOrganizationEntries(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustIndexedRepository(t, root, "acme", "widgets")
	if err := os.MkdirAll(filepath.Join(root, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(loose, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.root != root {
		t.Fatalf("snapshot root = %q, want %q", first.root, root)
	}
	if len(first.owners) != 1 || first.owners[0].Name != "acme" {
		t.Fatalf("snapshot owners = %#v, want only acme", first.owners)
	}
	if err := os.WriteFile(loose, []byte("a much longer payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.fingerprint != second.fingerprint {
		t.Fatalf("a loose file changed the fingerprint: %s then %s", first.fingerprint, second.fingerprint)
	}

	if err := os.MkdirAll(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	third, err := snapshotLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if third.fingerprint == second.fingerprint {
		t.Fatalf("a new organization did not change the fingerprint: %s", third.fingerprint)
	}
	if len(third.owners) != 2 {
		t.Fatalf("snapshot owners = %#v, want acme and beta", third.owners)
	}
}

func TestWriteLocalIndexReportsAnUncreatableTemporaryFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are unavailable")
	}
	directory := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	probe, probeErr := os.CreateTemp(directory, "lgcov-probe-*")
	if probeErr == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem permissions are not enforced for this user")
	}

	err := writeLocalIndex(filepath.Join(directory, "fleet-inventory.json"), persistedLocalIndex{
		SchemaVersion: LocalIndexSchemaVersion,
		ProjectsRoot:  t.TempDir(),
		Repositories:  []Repo{{Org: "acme", Name: "widgets"}},
	})
	if err == nil {
		t.Fatal("writeLocalIndex() into a directory it cannot create files in returned no error")
	}
	if _, statErr := os.Stat(filepath.Join(directory, "fleet-inventory.json")); statErr == nil {
		t.Fatal("writeLocalIndex() left an index behind after a failed write")
	}
}

func TestWriteLocalIndexRefusesAnUnrepresentableObservationTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "fleet-inventory.json")
	err := writeLocalIndex(path, persistedLocalIndex{
		SchemaVersion: LocalIndexSchemaVersion,
		ProjectsRoot:  filepath.Join(string(filepath.Separator), "projects"),
		ObservedAt:    time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		Repositories:  []Repo{{Org: "acme", Name: "widgets"}},
	})
	if err == nil {
		t.Fatal("writeLocalIndex() with an unencodable observation time returned no error")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("writeLocalIndex() left an index behind after a failed encode")
	}
}

func TestSnapshotLocalSourceReportsAFailedWorkingDirectoryLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removing the process working directory is a POSIX behaviour")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	doomed := t.TempDir()
	if err := os.Chdir(doomed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(original); restoreErr != nil {
			t.Errorf("restore working directory %s: %v", original, restoreErr)
		}
	})
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotLocalSource("relative-projects-root"); err == nil {
		t.Skip("this operating system still resolves a removed working directory")
	}
}

func lgCovHasDiagnostic(diagnostics []string, fragment string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, fragment) {
			return true
		}
	}
	return false
}
