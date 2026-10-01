//go:build darwin && e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2ECanonicalPublicationCapabilityDescriptors(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, path)
	closed := wtLifeCovOpenDirectory(t, t.TempDir())
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(path, "evidence")
	if err := os.WriteFile(filePath, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })
	for _, tc := range []struct {
		name       string
		roots      []gitFilesystemCapabilityRoot
		diagnostic string
	}{
		{"empty", nil, "at least one writable root"},
		{"absent", []gitFilesystemCapabilityRoot{{path: path}}, "descriptor is unavailable"},
		{"closed", []gitFilesystemCapabilityRoot{{path: path, directory: closed}}, "inspect git capability root"},
		{"regular", []gitFilesystemCapabilityRoot{{path: filePath, directory: regular}}, "not a directory"},
		{"relative", []gitFilesystemCapabilityRoot{{path: "relative", directory: directory}}, "must be absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newGitFilesystemCapability(tc.roots...)
			if err == nil || len(got.writeRoots) != 0 || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("capability = %+v, %v", got, err)
			}
		})
	}
	got, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: path, directory: directory}, gitFilesystemCapabilityRoot{path: path + "/.", directory: directory})
	if err != nil || len(got.writeRoots) != 1 || got.writeRoots[0].directory != directory || got.writeRoots[0].path != path {
		t.Fatalf("deduplicated retained capability = %+v, %v", got, err)
	}
	if contents, err := os.ReadFile(filePath); err != nil || string(contents) != "retained" {
		t.Fatalf("evidence = %q, %v", contents, err)
	}
}

func TestE2ECanonicalPublicationDarwinDescriptorRemoval(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, path)
	if directoryDescriptorWasRemovedAt(nil, path, 0) {
		t.Fatal("nil descriptor reported removed")
	}
	if directoryDescriptorWasRemovedAt(directory, path, 0) {
		t.Fatal("existing owned directory reported removed")
	}
	if directoryDescriptorWasRemovedAt(directory, path+".substitute", 0) {
		t.Fatal("different spelling reported removed")
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if directoryDescriptorWasRemovedAt(directory, path, 0) {
		t.Fatal("closed descriptor reported removed")
	}
	if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor state = %v", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("directory evidence changed: %v, %v", info, err)
	}
}

func TestE2ECanonicalPublicationDarwinExecFailure(t *testing.T) {
	t.Parallel()
	executable := filepath.Join(t.TempDir(), "missing-git")
	if code := runPlatformGitWithFilesystemCapability(gitFilesystemCapability{}, executable, []string{"status"}, os.Environ()); code != 1 {
		t.Fatalf("native failed Exec exit = %d", code)
	}
	if _, err := os.Stat(executable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed exec path changed: %v", err)
	}
}

func TestE2ECanonicalPublicationDarwinDeveloperGitAdmission(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	regular := filepath.Join(path, "git")
	if err := os.WriteFile(regular, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, diagnostic string }{
		{"relative", "git", "non-absolute Git path"},
		{"missing", filepath.Join(path, "missing"), "inspect developer Git"},
		{"regular", regular, "not executable"},
		{"directory", path, "not executable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := admitDarwinGitExecutable([]byte(tc.path))
			if err == nil || got != "" || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("developer query admission = %q, %v", got, err)
			}
		})
	}
	if err := os.Chmod(regular, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := admitDarwinGitExecutable([]byte(" \n" + regular + "\n"))
	if err != nil || got != regular {
		t.Fatalf("executable admission = %q, %v", got, err)
	}
}

//nolint:paralleltest // xcrun and cache resolution read process-wide environment; each fixture restores it.
func TestE2ECanonicalPublicationDarwinAmbientRoots(t *testing.T) {
	for _, name := range []string{"HOME", "GOCACHE", "GOPATH", "GOMODCACHE"} {
		t.Setenv(name, "")
	}
	if roots := ambientGoCacheRoots(); len(roots) != 0 {
		t.Fatalf("unavailable native home/cache = %v", roots)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if roots := ambientGoCacheRoots(); !reflect.DeepEqual(roots, []string{filepath.Join(cache, "go-build"), filepath.Join(home, "go", "pkg", "mod")}) {
		t.Fatalf("native default caches = %v", roots)
	}
	t.Setenv("HOME", "")
	t.Setenv("GOCACHE", "relative")
	t.Setenv("GOMODCACHE", "relative")
	if roots := ambientGoCacheRoots(); len(roots) != 0 {
		t.Fatalf("relative caches admitted = %v", roots)
	}
	root := t.TempDir()
	t.Setenv("GOCACHE", root)
	t.Setenv("GOMODCACHE", root)
	if roots := ambientGoCacheRoots(); !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("duplicate caches = %v", roots)
	}
	ctx := context.Background()
	if withProjectsRoot(ctx, " \t") != ctx {
		t.Fatal("blank projects root changed context")
	}
	if got := projectsRootFromContext(withProjectsRoot(ctx, " "+root+" ")); got != root {
		t.Fatalf("context root = %q", got)
	}
	t.Setenv("DEVELOPER_DIR", filepath.Join(root, "missing-developer"))
	if got, err := resolveDarwinGitExecutable(); err == nil || got != "" || !strings.Contains(err.Error(), "resolve developer Git with xcrun") {
		t.Fatalf("native xcrun failure = %q, %v", got, err)
	}
}

func TestE2ECanonicalPublicationOwnedMetadataBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := wtLifeCovOpenDirectory(t, root)
	destination := filepath.Join(root, "repo")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(destination, "evidence")
	if err := os.WriteFile(evidence, []byte("retain destination"), 0600); err != nil {
		t.Fatal(err)
	}
	var observed *os.File
	read := func(directory *os.File) (os.FileInfo, error) {
		observed = directory
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		return directory.Stat()
	}
	got, exists, err := prepareWorktreeDestinationWithRead(root, parent, "", "repo", read)
	if got != "" || exists || !errors.Is(err, os.ErrClosed) || !strings.Contains(err.Error(), "inspect secure worktree destination") || observed == nil {
		t.Fatalf("owned metadata refusal = %q, %v, %v", got, exists, err)
	}
	if _, err := observed.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("opened destination still live: %v", err)
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "retain destination" {
		t.Fatalf("destination evidence = %q, %v", contents, err)
	}
	if _, err := parent.Stat(); err != nil {
		t.Fatalf("borrowed operation directory closed: %v", err)
	}
	stagePath := filepath.Join(root, "stage")
	if err := os.Mkdir(stagePath, 0700); err != nil {
		t.Fatal(err)
	}
	stage := wtLifeCovOpenDirectory(t, stagePath)
	readStage := func(directory *os.File) (os.FileInfo, error) {
		if directory != stage {
			t.Fatal("reader observed a different stage")
		}
		info, err := directory.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		return info, nil
	}
	// A real original snapshot followed by close at the observation boundary.
	// This does not claim the host spontaneously reproduced a concurrent close.
	err = quarantineMatchingStageDirectoryAtWithRead(parent, stage, readStage)
	if !errors.Is(err, syscall.EBADF) || !strings.Contains(err.Error(), "inspect held staging directory identity") {
		t.Fatalf("second native identity read = %v", err)
	}
	if _, err := stage.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("stage not closed: %v", err)
	}
	if info, err := os.Stat(stagePath); err != nil || !info.IsDir() {
		t.Fatalf("stage evidence changed: %v, %v", info, err)
	}
}

func TestE2ECanonicalPublicationDestinationPlanning(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "valid", "wrong operation", "invalid repository", "unsafe parent", "regular", "symlink", "unreadable", "substitution"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			parent := wtLifeCovOpenDirectory(t, root)
			path := filepath.Join(root, "repo")
			operationRoot := root
			relativeParent := ""
			repository := "repo"
			read := (*os.File).Stat
			want := ""
			exists := false
			switch name {
			case "missing":
			case "wrong operation":
				operationRoot = root + ".wrong"
				want = "operation path changed"
			case "invalid repository":
				repository = "../repo"
				want = "invalid worktree repository segment"
			case "unsafe parent":
				relativeParent = ".."
				want = ""
			case "regular":
				if err := os.WriteFile(path, []byte("retain regular"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "not a directory"
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
				want = "refusing symlinked worktree destination"
			default:
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				exists = true
				if name == "unreadable" {
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Chmod(path, 0700); err != nil {
							t.Error(err)
						}
					})
					want = "inspect secure worktree destination"
					exists = false
				}
				if name == "substitution" {
					read = func(directory *os.File) (os.FileInfo, error) {
						info, err := directory.Stat()
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(path, path+".retained"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						return info, nil
					}
					want = "destination path changed before planning"
					exists = false
				}
			}
			got, actualExists, err := prepareWorktreeDestinationWithRead(operationRoot, parent, relativeParent, repository, read)
			refused := name != "missing" && name != "valid"
			if refused {
				if err == nil || got != "" || actualExists || !strings.Contains(err.Error(), want) {
					t.Fatalf("plan %s = %q, %v, %v; want %q", name, got, actualExists, err, want)
				}
				if name == "unreadable" && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("native permission identity lost: %v", err)
				}
			} else if err != nil || got != path || actualExists != exists {
				t.Fatalf("plan %s = %q, %v, %v", name, got, actualExists, err)
			}
			if _, err := parent.Stat(); err != nil {
				t.Fatalf("borrowed parent closed: %v", err)
			}
			if name == "regular" {
				if contents, err := os.ReadFile(path); err != nil || string(contents) != "retain regular" {
					t.Fatalf("regular evidence = %q, %v", contents, err)
				}
			}
			if name == "symlink" {
				if target, err := os.Readlink(path); err != nil || target == "" {
					t.Fatalf("symlink evidence = %q, %v", target, err)
				}
			}
			if name == "substitution" {
				if info, err := os.Stat(path + ".retained"); err != nil || !info.IsDir() {
					t.Fatalf("retained destination = %v, %v", info, err)
				}
			}
		})
	}
}

//nolint:paralleltest // The native repository helper pins process-wide HOME and WB/XDG roots.
func TestE2ECanonicalPublicationGuardNativeNamespaceRefusals(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	mismatch := createMismatchedWorktree(t, fixture, "guard-mismatch", "acme", "other", "other")
	before := gitTestOutput(t, mismatch, "rev-parse", "HEAD")
	// Defensive inconsistent query observations at the existing ordinary-Git boundary.
	// The replacement was captured from another actual native canonical clone;
	// all queries still execute native Git, and later identity reads remain unchanged.
	observedCommon := gitTestOutput(t, fixture.canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	fault := &canonicalPublicationFirstCommonObservation{Runner: runner.New(), directory: mismatch, common: observedCommon}
	got, err := Guard(withGitRunner(context.Background(), fault), mismatch, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if !fault.hit || err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "belongs to a different canonical clone") {
		t.Fatalf("inconsistent observed common directory = %+v, %v", got, err)
	}
	if after := gitTestOutput(t, mismatch, "rev-parse", "HEAD"); after != before {
		t.Fatalf("mismatch refusal changed HEAD: %s", after)
	}
	protected := filepath.Join(fixture.canonical, ".worktrees", "protected")
	gitTest(t, fixture.canonical, "worktree", "add", "--force", protected, "main")
	got, err = Guard(context.Background(), protected, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "protected base branch") {
		t.Fatalf("native protected branch = %+v, %v", got, err)
	}
	home := t.TempDir()
	if err := os.Symlink(".wb", filepath.Join(home, ".wb")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	_, cause := wbhome.Resolve(fixture.projectsRoot)
	if cause == nil {
		t.Fatal("native legacy-home loop did not refuse")
	}
	got, err = Guard(context.Background(), protected, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err == nil || !reflect.DeepEqual(got, GuardResult{}) || err.Error() != cause.Error() {
		t.Fatalf("native legacy-home propagation = %+v, %v; cause %v", got, err, cause)
	}
}

func TestE2ECanonicalPublicationQuarantineNativePermissionRefusal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := wtLifeCovOpenDirectory(t, root)
	name := ".wb-stage-permission"
	stagePath := filepath.Join(root, name)
	if err := os.Mkdir(stagePath, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(stagePath, "evidence")
	if err := os.WriteFile(evidence, []byte("retained stage"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := secureDirectoryIdentityAt(int(parent.Fd()), name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0700); err != nil {
			t.Error(err)
		}
	})
	err = quarantineStageDirectoryAt(parent, name, expected)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("native quarantine permission refusal = %v", err)
	}
	actual, err := secureDirectoryIdentityAt(int(parent.Fd()), name)
	if err != nil || actual != expected {
		t.Fatalf("stage identity changed on refusal = %+v, %v", actual, err)
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "retained stage" {
		t.Fatalf("stage evidence = %q, %v", contents, err)
	}
	if _, err := parent.Stat(); err != nil {
		t.Fatalf("borrowed quarantine parent closed: %v", err)
	}
}

func TestE2ECanonicalPublicationNativeCwdRefusals(t *testing.T) {
	const marker = "WB_CANONICAL_PUBLICATION_CWD_CHILD"
	if os.Getenv(marker) == "1" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		cwd := t.TempDir()
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(cwd, 0700); err != nil {
				t.Error(err)
			}
			if err := os.Chdir(original); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chmod(cwd, 0); err != nil {
			t.Fatal(err)
		}
		if _, cause := os.Getwd(); !errors.Is(cause, os.ErrPermission) {
			t.Fatalf("native cwd refusal prerequisite = %v", cause)
		}
		if got, err := absoluteProjectsRoot("relative"); got != "" || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native absolute-root refusal = %q, %v", got, err)
		}
		got, err := Guard(context.Background(), "unused-checkout", GuardOptions{ProjectsRoot: "relative"})
		if !reflect.DeepEqual(got, GuardResult{}) || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("guard native root-resolution refusal = %+v, %v", got, err)
		}
		if code := verifySecureStageContainment(cwd); code != 1 {
			t.Fatalf("native containment cwd refusal = %d", code)
		}
		return
	}
	t.Parallel()
	deadline := time.Now().Add(time.Minute)
	if parentDeadline, ok := t.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ECanonicalPublicationNativeCwdRefusals$")
	command.Env = append(os.Environ(), marker+"=1")
	if dir := wtLifeCovCoverDir(); testing.CoverMode() != "" && dir != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+dir)
		command.Env = append(command.Env, "GOCOVERDIR="+dir)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native cwd child = %v\n%s", err, output)
	}
}

//nolint:paralleltest // Capability cache admission reads process-wide HOME and Go-cache environment values.
func TestE2ECanonicalPublicationHookRootNativeAdmission(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GOPATH", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOCACHE", "")
	for _, name := range []string{"runtime file", "unavailable cache"} {
		t.Run(name, func(t *testing.T) {
			repo := wtLifeCovNewRepo(t)
			projects := t.TempDir()
			layout, err := hooks.ResolveExecutionLayout(repo.path, projects)
			if err != nil {
				t.Fatal(err)
			}
			if !pathWithin(projects, layout.Root) {
				t.Fatalf("runtime fixture escaped private projects root: %s", layout.Root)
			}
			if name == "runtime file" {
				if err := os.MkdirAll(filepath.Dir(layout.Root), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(layout.Root, []byte("retained runtime file"), 0600); err != nil {
					t.Fatal(err)
				}
				roots, handles, err := appendSecureHookExecutionCapabilityRoots(repo.path, projects, nil)
				if err == nil || roots != nil || handles != nil {
					t.Fatalf("non-directory runtime admission = %+v, %+v, %v", roots, handles, err)
				}
				if contents, err := os.ReadFile(layout.Root); err != nil || string(contents) != "retained runtime file" {
					t.Fatalf("runtime evidence = %q, %v", contents, err)
				}
				return
			}
			cache := filepath.Join(t.TempDir(), "cache-file")
			if err := os.WriteFile(cache, []byte("retained cache file"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOCACHE", cache)
			roots, handles, err := appendSecureHookExecutionCapabilityRoots(repo.path, projects, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, handle := range handles {
					_ = handle.directory.Close()
				}
			})
			found := false
			for _, root := range roots {
				if root.path == cache {
					t.Fatalf("non-directory cache authorized: %+v", root)
				}
				if root.path == layout.Root {
					found = true
				}
			}
			if !found {
				t.Fatal("runtime root lost after optional cache refusal")
			}
			if contents, err := os.ReadFile(cache); err != nil || string(contents) != "retained cache file" {
				t.Fatalf("cache evidence = %q, %v", contents, err)
			}
		})
	}
}

// canonicalPublicationFirstCommonObservation substitutes only one observed
// response after the genuine native command has completed successfully.
type canonicalPublicationFirstCommonObservation struct {
	runner.Runner
	directory, common string
	hit               bool
}

func (f *canonicalPublicationFirstCommonObservation) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := f.Runner.RunOpts(ctx, dir, options, name, args...)
	if err == nil && !f.hit && dir == f.directory && name == "git" && reflect.DeepEqual(args, []string{"-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir"}) {
		f.hit = true
		result.CombinedOutput = f.common + "\n"
	}
	return result, err
}
