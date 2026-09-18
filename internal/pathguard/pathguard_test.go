package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errDenied models the sandbox refusal the preflight exists to explain.
var errDenied = errors.New("operation not permitted")

// TestCheckNamesEveryDeniedPathItsRoleAndTheRemedies is the proof for
// projects-root-layout#ac:denied-write-names-the-path-and-remedy: the operator
// must learn the path, what it is for, and what to do — never just the errno
// they already had.
func TestCheckNamesEveryDeniedPathItsRoleAndTheRemedies(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "projects")
	state := filepath.Join(root, ".wb")
	store := filepath.Join(root, ".worktrees")
	canonical := filepath.Join(root, "github.com", "acme", "app")

	probe := func(path string) error {
		switch path {
		case state, filepath.Join(canonical, ".git"):
			return errDenied
		default:
			return nil
		}
	}
	err := Check(root, []Requirement{
		{Path: state, Role: RoleState},
		{Path: store, Role: RoleStore},
		CanonicalRequirement(canonical),
	}, probe)
	var denial *Error
	if !errors.As(err, &denial) {
		t.Fatalf("check = %v, want a *pathguard.Error", err)
	}
	if len(denial.Denials) != 2 {
		t.Fatalf("denials = %#v, want only the two denied paths", denial.Denials)
	}
	message := denial.Error()
	for _, want := range []string{
		state,
		"private state",
		filepath.Join(canonical, ".git"),
		"Git registration",
		root,                    // remedy: widen the workspace to the root
		"allowed writable root", // remedy: name the path as a writable root
		"repository-local",      // remedy: keep checkouts inside the clone
		"write",                 // the failing operation is named as a write
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic %q does not mention %q", message, want)
		}
	}
	if strings.TrimSpace(message) == errDenied.Error() {
		t.Fatalf("diagnostic is the bare errno: %q", message)
	}
	if strings.Contains(message, store) {
		t.Fatalf("diagnostic names a writable path as denied: %q", message)
	}
}

// TestCheckPassesWhenEveryDeclaredPathIsWritable keeps the preflight from
// becoming a blanket refusal.
func TestCheckPassesWhenEveryDeclaredPathIsWritable(t *testing.T) {
	if err := Check("/projects", []Requirement{
		{Path: "/projects/.wb", Role: RoleState},
		{Path: "/projects/.worktrees", Role: RoleStore},
	}, func(string) error { return nil }); err != nil {
		t.Fatalf("check = %v, want nil", err)
	}
}

// TestRequirementsDeclareTheStoreOnlyWhenTheModeHasOne proves the declared set
// follows the selected store mode: repository-local mode keeps checkouts
// inside their canonical clone, so it declares no store root — and must
// therefore not demand write access to one.
func TestRequirementsDeclareTheStoreOnlyWhenTheModeHasOne(t *testing.T) {
	central := Requirements("/projects/.wb", "/projects/.worktrees")
	if !hasRole(central, RoleState) || !hasRole(central, RoleTemp) || !hasRole(central, RoleStore) {
		t.Fatalf("central requirements = %#v", central)
	}
	local := Requirements("/projects/.wb", "")
	if hasRole(local, RoleStore) {
		t.Fatalf("repository-local requirements declared a store root: %#v", local)
	}
	if !hasRole(local, RoleState) || !hasRole(local, RoleTemp) {
		t.Fatalf("repository-local requirements = %#v", local)
	}
}

// TestOSProbeReportsAMissingPathThroughItsNearestAncestor covers the real
// probe: a declared path that does not exist yet must be judged by the
// permission a later create actually needs, not by its own absence.
func TestOSProbeReportsAMissingPathThroughItsNearestAncestor(t *testing.T) {
	root := t.TempDir()
	if err := OSProbe(filepath.Join(root, ".wb", "worktrees", "task")); err != nil {
		t.Fatalf("probe of a creatable path = %v, want nil", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left entries behind: %#v", entries)
	}
	file := filepath.Join(root, "not-a-directory")
	if writeErr := os.WriteFile(file, []byte("x"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := OSProbe(file); err == nil {
		t.Fatal("probe accepted a declared path that exists as a regular file")
	}
}

func hasRole(requirements []Requirement, role Role) bool {
	for _, requirement := range requirements {
		if requirement.Role == role {
			return true
		}
	}
	return false
}
