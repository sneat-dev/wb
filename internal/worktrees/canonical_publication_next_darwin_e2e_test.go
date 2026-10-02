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
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/testenv"
)

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
	if err := testenv.WriteExecutableFile(regular, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, diagnostic string }{
		{"relative", "git", "non-absolute Git path"},
		{"missing", filepath.Join(path, "missing"), "inspect developer Git"},
		{"regular", regular, "not executable"},
		{"directory", path, "not executable"},
	} {
		//nolint:paralleltest // Serial cases inspect the shared non-executable file before the parent chmods it for the success control.
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

//nolint:paralleltest // xcrun reads the process-wide developer directory.
func TestE2ECanonicalPublicationDarwinDeveloperDirectoryFailure(t *testing.T) {
	t.Setenv("DEVELOPER_DIR", filepath.Join(t.TempDir(), "missing-developer"))
	if got, err := resolveDarwinGitExecutable(); err == nil || got != "" || !strings.Contains(err.Error(), "resolve developer Git with xcrun") {
		t.Fatalf("native xcrun failure = %q, %v", got, err)
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
		//nolint:paralleltest // The ancestor pins HOME/cache configuration with t.Setenv; this case also changes GOCACHE and cannot run in parallel.
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
