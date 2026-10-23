package deps

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestObservationRetainsDisappearingReadFailures(t *testing.T) {
	t.Parallel()
	t.Run("go requirement", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		path := filepath.Join(root, "go.mod")
		writeTestFile(t, path, "module example.com/app\n\ngo 1.24\n\nrequire example.com/sdk v1.2.3\n")
		modules, err := goManifestsWithWalk(root, "example.com/sdk", func(root string, visit fs.WalkDirFunc) error {
			return filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
				if name == path {
					if err := os.Remove(path); err != nil {
						return err
					}
				}
				return visit(name, entry, walkErr)
			})
		})
		if !errors.Is(err, os.ErrNotExist) || len(modules) != 0 {
			t.Fatalf("modules=%+v err=%v", modules, err)
		}
	})
	for _, name := range []string{"package.json", "pnpm-workspace.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, name)
			writeTestFile(t, path, "{}\n")
			decisions, err := (npmAdapter{}).inspectWorkingTreeWithRead(context.Background(), root, Target{Dependency: "sdk", Version: "2.0.0"}, Options{}, func(name string) ([]byte, error) {
				if err := os.Remove(name); err != nil {
					return nil, err
				}
				return os.ReadFile(name)
			})
			if !errors.Is(err, os.ErrNotExist) || len(decisions) != 0 {
				t.Fatalf("decisions=%+v err=%v", decisions, err)
			}
		})
	}
}

func TestManifestInspectRefusesGoDowngrade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/sdk v2.0.0+incompatible\n")
	target := Target{Dependency: "example.com/sdk", Version: "v1.2.3"}
	decisions, err := (goAdapter{}).inspectWorkingTree(context.Background(), root, target, Options{})
	if err == nil || len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" || !strings.Contains(err.Error(), "lower than observed version") {
		t.Fatalf("decisions=%+v err=%v", decisions, err)
	}
	decisions, err = (goAdapter{}).inspectWorkingTree(context.Background(), root, target, Options{AllowDowngrade: true})
	if err != nil || len(decisions) != 1 || decisions[0].Action != "planned" {
		t.Fatalf("allowed decisions=%+v err=%v", decisions, err)
	}
}

func TestNpmLockObservationFailureSurvivesManifestDiscovery(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"drift", "installed peers"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "package.json"), "{}\n")
			readLocks := func(path string) (map[string]npmLockScope, error) {
				if err := os.RemoveAll(path); err != nil {
					return nil, err
				}
				return readNpmLockScopes(path)
			}
			var err error
			if name == "drift" {
				_, err = inspectNpmDriftRepositoryWithLockScopes(context.Background(), Repository{Slug: "acme/app", Path: root}, DriftOptions{}, time.Time{}, nil, readLocks)
			} else {
				_, _, err = installedNpmVersionsWithLockScopes(root, readLocks)
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lock observation lost native failure: %v", err)
			}
		})
	}
}

type removedWorkflowEntry struct {
	fs.DirEntry
	path string
}

func (entry removedWorkflowEntry) Info() (fs.FileInfo, error) {
	if err := os.Remove(entry.path); err != nil {
		return nil, err
	}
	return os.Stat(entry.path)
}

func TestWorkflowObservationRetainsNativeFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read during inspect", "read during apply", "metadata during apply", "walk during inspect", "walk during apply"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, ".github", "workflows", "ci.yml")
			original := "jobs:\n  test:\n    steps:\n      - uses: acme/action@v1\n"
			writeTestFile(t, path, original)
			target := Target{Dependency: "acme/action", Version: "v2", Resolved: strings.Repeat("a", 40)}
			walk := func(root string, visit fs.WalkDirFunc) error {
				return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
					if current == path {
						switch {
						case strings.HasPrefix(name, "read"):
							if err := os.Remove(path); err != nil {
								return err
							}
						case strings.HasPrefix(name, "metadata"):
							entry = removedWorkflowEntry{DirEntry: entry, path: path}
						case strings.HasPrefix(name, "walk"):
							walkErr = &os.PathError{Op: "readdir", Path: current, Err: os.ErrPermission}
						}
					}
					return visit(current, entry, walkErr)
				})
			}
			var err error
			if strings.HasSuffix(name, "inspect") {
				_, err = (githubActionsAdapter{}).inspectWorkingTreeWithWalk(context.Background(), root, target, Options{}, walk)
			} else {
				_, err = (githubActionsAdapter{}).applyWithWalk(context.Background(), root, target, Options{}, walk)
			}
			want := os.ErrNotExist
			if strings.HasPrefix(name, "walk") {
				want = os.ErrPermission
			}
			if !errors.Is(err, want) {
				t.Fatalf("observation lost error: %v", err)
			}
		})
	}
}

func TestWorkflowInspectionOrdersFilesAndRetainsStatErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"z.yml", "a.yml"} {
		writeTestFile(t, filepath.Join(root, ".github", "workflows", name), "steps:\n - uses: acme/action@v1\n")
	}
	decisions, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), root, Target{Dependency: "acme/action", Version: "v2", Resolved: strings.Repeat("a", 40)}, Options{})
	if err != nil || len(decisions) != 2 || decisions[0].File >= decisions[1].File {
		t.Fatalf("decisions=%+v err=%v", decisions, err)
	}
	badRoot := filepath.Join(t.TempDir(), "occupied")
	writeTestFile(t, badRoot, "keep")
	_, err = (githubActionsAdapter{}).inspectWorkingTree(context.Background(), badRoot, Target{}, Options{})
	if err == nil {
		t.Fatal("non-directory ancestor accepted")
	}
}

func TestManifestInspectionDistinguishesMissingSourcesAndIgnoredFixtures(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := (goAdapter{}).inspectWorkingTree(context.Background(), missing, Target{}, Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Go missing root error=%v", err)
	}
	if _, err := (npmAdapter{}).inspectWorkingTree(context.Background(), missing, Target{}, Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("npm missing root error=%v", err)
	}
	if decisions, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), t.TempDir(), Target{}, Options{}); err != nil || len(decisions) != 0 {
		t.Fatalf("absent optional workflows=%v err=%v", decisions, err)
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "testdata", "go.mod"), "invalid fixture which must not be parsed\n")
	writeTestFile(t, filepath.Join(root, "dist", "go.mod"), "invalid generated output\n")
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/sdk v1.2.3\n")
	modules, err := goManifests(root, "example.com/sdk")
	if err != nil || len(modules) != 1 || modules[0].relative != "go.mod" {
		t.Fatalf("fixture policy changed: modules=%+v err=%v", modules, err)
	}
}

func TestWorkflowInspectionRefusesDowngradeUnlessExplicitlyAllowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".github", "workflows", "ci.yml")
	original := "steps:\n  - uses: acme/action@v2.0.0\n"
	writeTestFile(t, path, original)
	target := Target{Dependency: "acme/action", Version: "v1.2.3", Resolved: strings.Repeat("b", 40)}
	decisions, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), root, target, Options{})
	if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
		t.Fatalf("downgrade accepted: decisions=%+v err=%v", decisions, err)
	}
	decisions, err = (githubActionsAdapter{}).inspectWorkingTree(context.Background(), root, target, Options{AllowDowngrade: true})
	if err != nil || len(decisions) != 1 || decisions[0].BeforeVersion != "v2.0.0" || decisions[0].TargetVersion != "v1.2.3" {
		t.Fatalf("allowed decisions=%+v err=%v", decisions, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != original {
		t.Fatalf("inspection changed original: bytes=%q err=%v", raw, err)
	}
}
