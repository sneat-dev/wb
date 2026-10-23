package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryPolicyRecordAdmission(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, record  string
		found, refuse bool
	}{
		{"absent", " \n", false, false},
		{"regular", "100644 blob " + sha + "\t.wb/worktrees.yaml\n", true, false},
		{"executable", "100755 blob " + sha + "\t.wb/worktrees.yaml", true, false},
		{"no separator", "100644 blob " + sha, false, true},
		{"wrong path", "100644 blob " + sha + "\t.wb/other.yaml", false, true},
		{"missing metadata", "100644 blob\t.wb/worktrees.yaml", false, true},
		{"tree", "040000 tree " + sha + "\t.wb/worktrees.yaml", false, true},
		{"symlink", "120000 blob " + sha + "\t.wb/worktrees.yaml", false, true},
		{"invalid object", "100644 blob bad\t.wb/worktrees.yaml", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, found, err := repositoryPolicyEntry("base", []byte(tc.record))
			if found != tc.found || (err != nil) != tc.refuse || (found && got != sha) || (!found && got != "") {
				t.Fatalf("policy record admission=(%q,%t,%v)", got, found, err)
			}
			if tc.refuse && err.Error() != fmt.Sprintf("repository worktrees policy at base must be a regular blob, not %q", strings.TrimSpace(tc.record)) {
				t.Fatalf("unexpected diagnostic: %v", err)
			}
		})
	}
}

func TestRepositoryPolicyBlobSizeAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"0", ""}, {fmt.Sprint(maxBranchConfigSize), ""},
		{"no size", `parse repository worktrees policy blob size at base: "no size"`},
		{" -1\n", `parse repository worktrees policy blob size at base: "-1"`},
		{"9999999999999999999999999", `parse repository worktrees policy blob size at base: "9999999999999999999999999"`},
		{fmt.Sprint(maxBranchConfigSize + 1), fmt.Sprintf("repository worktrees policy blob at base exceeds %d-byte limit", maxBranchConfigSize)},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			err := validateRepositoryPolicyBlobSize("base", []byte(tc.input))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("blob size refusal=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestBranchConfigNativeInspectionAndReadFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ stage, diagnostic string }{
		{"resolved", "inspect worktrees config "}, {"inspected", "read worktrees config "},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "worktrees.yaml")
			if err := os.WriteFile(path, []byte("version: 1\nworktrees: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			expectedResolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			removed := false
			config, found, err := loadBranchConfigFileObserved(path, func(stage, resolved string) {
				if stage != tc.stage {
					return
				}
				if resolved != expectedResolved {
					t.Fatalf("resolved path=%q, want %q", resolved, expectedResolved)
				}
				if err := os.Remove(resolved); err != nil {
					t.Fatal(err)
				}
				removed = true
			})
			if !removed || found || config.Version != 0 || !errors.Is(err, os.ErrNotExist) || !strings.HasPrefix(err.Error(), tc.diagnostic+path+": ") {
				t.Fatalf("native disappearance=(%+v,%t,%v), removed=%t", config, found, err, removed)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed config=%v", err)
			}
		})
	}
}

func TestBranchConfigObservedAdmission(t *testing.T) {
	t.Parallel()
	t.Run("valid native read", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "worktrees.yaml")
		contents := []byte("version: 1\nworktrees: {}\n")
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		var stages []string
		config, found, err := loadBranchConfigFileObserved(path, func(stage, _ string) { stages = append(stages, stage) })
		if err != nil || !found || config.Version != 1 || strings.Join(stages, ",") != "resolved,inspected" {
			t.Fatalf("native admission=(%+v,%t,%v), stages=%v", config, found, err, stages)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(contents) {
			t.Fatalf("input changed=%q,%v", actual, err)
		}
	})
	for _, tc := range []struct {
		name, contents, diagnostic string
		directory                  bool
	}{
		{name: "absent"},
		{name: "directory", directory: true, diagnostic: "must resolve to a regular file"},
		{name: "oversized", contents: strings.Repeat("x", maxBranchConfigSize+1), diagnostic: "exceeds"},
		{name: "malformed", contents: "[", diagnostic: "worktrees config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "worktrees.yaml")
			if tc.directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if tc.contents != "" {
				if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, found, err := loadBranchConfigFile(path)
			if tc.diagnostic == "" {
				if err != nil || found {
					t.Fatalf("absent=(%t,%v)", found, err)
				}
				return
			}
			if err == nil || found || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("admission refusal=(%t,%v)", found, err)
			}
		})
	}
}

func TestSharedPlacementRetainsAbsolutePathRefusal(t *testing.T) {
	t.Parallel()
	refused := errors.New("native Windows FullPath refusal")
	called := false
	path := filepath.Join(t.TempDir(), "store")
	got, err := resolveSharedWorktreesRootWithAbsolute(path, func(value string) (string, error) {
		called = true
		if value != path {
			t.Fatalf("absolute input=%q", value)
		}
		return "", refused
	})
	if !called || got != "" || !errors.Is(err, refused) {
		t.Fatalf("absolute refusal=(%q,%v), called=%t", got, err, called)
	}
}

func TestPlacementMissingRootTerminates(t *testing.T) {
	t.Parallel()
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	calls := 0
	got, err := resolvePlacementPathWithResolver(root, func(path string) (string, error) {
		calls++
		if path != root {
			t.Fatalf("unexpected root resolution=%q", path)
		}
		return "", &os.PathError{Op: "lstat", Path: path, Err: os.ErrNotExist}
	})
	if got != root || err != nil || calls != 1 {
		t.Fatalf("missing root termination=(%q,%v), calls=%d", got, err, calls)
	}
}

func TestBranchCanonicalCoordinatesRefuseOutsideProjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	address, resolved, err := canonicalPathAddress(root, outside)
	if err == nil || resolved != "" || address.Host != "" || address.Org != "" || address.Repo != "" {
		t.Fatalf("outside canonical coordinates=(%+v,%q,%v)", address, resolved, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("projects root changed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path changed: %v", err)
	}
}
