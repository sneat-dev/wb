package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestRoleDescriptionKeepsTemporaryAndCustomRolesActionable(t *testing.T) {
	t.Parallel()
	for role, want := range map[Role]string{RoleTemp: "platform temporary area", Role("custom-cache"): "custom-cache"} {
		if got := role.description(); got != want {
			t.Errorf("description(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestErrorWithoutRootNamesSandboxWorkspace(t *testing.T) {
	t.Parallel()
	diagnostic := (&Error{Denials: []Denial{{Path: "/cache", Role: Role("cache")}}}).Error()
	for _, want := range []string{"cannot write to 1 path", "/cache — cache", "(the sandbox workspace root)"} {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("diagnostic %q does not contain %q", diagnostic, want)
		}
	}
}

func TestCheckSkipsBlankPathsAndUsesNativeProbe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := Check(root, []Requirement{{Path: " \t\n"}, {Path: root, Role: RoleState}}, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("native probe left entries: %v", entries)
	}
	calls := 0
	if err := Check(root, []Requirement{{Path: " \t\n"}}, func(string) error { calls++; return errDenied }); err != nil || calls != 0 {
		t.Fatalf("blank requirement: error=%v, probe calls=%d", err, calls)
	}
}

func TestOSProbeReportsRemovalRace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var removed string
	inj := &filewrite.Injector{Step: filewrite.StepClose, Hook: func() {
		entries, err := filepath.Glob(filepath.Join(root, ".wb-writable-probe-*"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("reserved probes = %v, error=%v", entries, err)
		}
		removed = entries[0]
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
	}}
	err := osProbeInjected(root, inj)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe removal race = %v, want missing probe error", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != removed {
		t.Fatalf("removal failure = %v, want reserved path %q", err, removed)
	}
}

func TestNearestExistingDirectoryRejectsSymlinksAndNonDirectoryAncestors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	leaf := filepath.Join(root, "file")
	if err := os.WriteFile(leaf, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := nearestExistingDirectory(filepath.Join(leaf, "child")); err == nil || got != "" {
		t.Fatalf("non-directory ancestor = %q, %v", got, err)
	}
	for _, target := range []string{root, filepath.Join(root, "missing")} {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got, err := nearestExistingDirectory(link); err == nil || got != "" || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink to %q = %q, %v", target, got, err)
		}
	}
}

func TestNearestExistingDirectoryStopsAtUnavailableVolumeRoot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "child")
	var visited []string
	got, err := nearestExistingDirectoryWithStat(path, func(current string) (os.FileInfo, error) {
		visited = append(visited, current)
		return nil, &os.PathError{Op: "stat", Path: current, Err: os.ErrNotExist}
	})
	if got != "" || err == nil || !strings.Contains(err.Error(), "no existing ancestor directory for "+path) {
		t.Fatalf("unavailable volume = %q, %v", got, err)
	}
	if len(visited) == 0 || filepath.Dir(visited[len(visited)-1]) != visited[len(visited)-1] {
		t.Fatalf("ancestor walk did not stop at volume root: %v", visited)
	}
	for i := 1; i < len(visited); i++ {
		if visited[i] != filepath.Dir(visited[i-1]) {
			t.Fatalf("ancestor walk skipped a parent: %v", visited)
		}
	}
}

func TestErrorDiagnosesCentralPathsAndOmitsAlreadyLocalRemedy(t *testing.T) {
	t.Parallel()
	central := (&Error{Denials: []Denial{{Path: "/projects/.worktrees", Role: RoleStore}, {Path: "/projects/app/.git", Role: RoleCanonicalGit}}}).Error()
	for _, want := range []string{"central checkout store", "canonical clone Git registration", "select repository-local store mode"} {
		if !strings.Contains(central, want) {
			t.Errorf("central diagnostic %q does not contain %q", central, want)
		}
	}
	local := (&Error{Denials: []Denial{{Path: "/projects/app/.worktrees", Role: RoleLocalStore}}}).Error()
	if !strings.Contains(local, "repository-local checkout store") || strings.Contains(local, "select repository-local store mode") {
		t.Fatalf("local diagnostic offers unusable remedy: %s", local)
	}
}
