// Package pathguard declares the paths a WB command needs to write to and
// turns a denial into an actionable diagnostic.
//
// A sandboxed harness grants write access to a workspace root and nothing
// else. WB keeps private state at <root>/.wb and, in the default central store
// mode, checkouts at <root>/.worktrees — both outside a workspace that was
// widened only to a canonical clone. Without this package the operator sees a
// bare "operation not permitted" from whichever syscall happened to fail
// first, with no path, no role and no remedy.
//
// The contract this package implements is
// projects-root-layout#req:declared-writable-paths and
// projects-root-layout#req:actionable-permission-error.
package pathguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Role names why WB needs to write to a declared path. The role is what makes
// the diagnostic actionable: an operator who is told the path is the checkout
// store can move it, while an operator told only the path can only guess.
type Role string

const (
	// RoleState is the private coordination state directory <root>/.wb.
	RoleState Role = "state"
	// RoleStore is the central checkout store <root>/.worktrees, where
	// central-mode task checkouts physically land.
	RoleStore Role = "store"
	// RoleLocalStore is <canonical>/.worktrees, where a repository-local
	// checkout physically lands. It is a distinct role from RoleStore because
	// the two select different layouts: telling an operator already in
	// repository-local mode to select repository-local mode is not a remedy.
	RoleLocalStore Role = "local-store"
	// RoleCanonicalGit is <canonical>/.git. Git writes gitdir, commondir,
	// HEAD, index, logs, refs, ORIG_HEAD and COMMIT_EDITMSG there on worktree
	// create, repair and remove — so it is part of the declared writable set
	// even though it sits inside a directory WB otherwise only reads.
	RoleCanonicalGit Role = "canonical-git"
	// RoleTemp is the platform temporary area.
	RoleTemp Role = "temp"
)

// description explains a role in one clause a human can act on.
func (role Role) description() string {
	switch role {
	case RoleState:
		return "private state (claims, locks, Work Logs, reports)"
	case RoleStore:
		return "central checkout store"
	case RoleLocalStore:
		return "repository-local checkout store (<canonical>/.worktrees)"
	case RoleCanonicalGit:
		return "canonical clone Git registration (worktree add/repair/remove)"
	case RoleTemp:
		return "platform temporary area"
	default:
		return string(role)
	}
}

// Requirement is one path WB must be able to write to, together with the role
// that path plays in the command about to run.
type Requirement struct {
	Path string
	Role Role
}

// Probe reports whether WB can create an entry at or under path. A nil result
// means the path is writable. Production callers use OSProbe; a test that must
// reproduce a sandbox denial without being sandboxed injects its own.
type Probe func(path string) error

// Denial is one declared path the probe refused.
type Denial struct {
	Path string
	Role Role
	// Err is the underlying probe failure. It is carried for the operator's
	// logs but is deliberately not the whole diagnostic.
	Err error
}

// Error is the actionable diagnostic: it names every unwritable path, the role
// each plays, and the remedies. It never consists solely of an errno string,
// because the errno is what the operator already had.
type Error struct {
	// Root is the projects root the command resolves every path from.
	Root string
	// Denials is non-empty and ordered as the requirements were declared.
	Denials []Denial
}

func (err *Error) Error() string {
	var builder strings.Builder
	if len(err.Denials) == 1 {
		builder.WriteString("cannot write to 1 path this command needs:\n")
	} else {
		fmt.Fprintf(&builder, "cannot write to %d paths this command needs:\n", len(err.Denials))
	}
	for _, denial := range err.Denials {
		fmt.Fprintf(&builder, "  %s — %s", denial.Path, denial.Role.description())
		if denial.Err != nil {
			fmt.Fprintf(&builder, " (%v)", denial.Err)
		}
		builder.WriteString("\n")
	}
	builder.WriteString("to fix it, do any one of these:\n")
	root := err.Root
	if root == "" {
		root = "(the sandbox workspace root)"
	}
	fmt.Fprintf(&builder, "  - widen the sandbox workspace to %s so WB can write the paths above\n", root)
	fmt.Fprintf(&builder, "  - add %s as an allowed writable root\n", err.paths())
	if !err.repositoryLocal() {
		builder.WriteString("  - select repository-local store mode (`worktrees.store: repository-local` in the machine-local worktrees config), which keeps each checkout and its Git registration inside its canonical clone\n")
	}
	builder.WriteString("the failing operation was a write; WB names the path rather than reporting the bare errno")
	return builder.String()
}

// repositoryLocal reports whether the offending mode already keeps checkouts
// inside their canonical clone. Offering that mode as a remedy then is a no-op
// an operator cannot act on, so it is left out.
func (err *Error) repositoryLocal() bool {
	for _, denial := range err.Denials {
		if denial.Role == RoleLocalStore {
			return true
		}
	}
	return false
}

// paths renders the denied paths as one comma-separated list for the
// "allowed writable root" remedy.
func (err *Error) paths() string {
	paths := make([]string, 0, len(err.Denials))
	for _, denial := range err.Denials {
		paths = append(paths, denial.Path)
	}
	return strings.Join(paths, ", ")
}

// Check probes every declared requirement and returns nil when all of them are
// writable. It returns a *Error naming each unwritable path, its role and the
// remedies, so a caller can fail before its first mutation instead of
// reporting whichever syscall lost the race to fail.
//
// A nil probe falls back to OSProbe. Root is only used to word the remedies.
func Check(root string, requirements []Requirement, probe Probe) error {
	if probe == nil {
		probe = OSProbe
	}
	var denials []Denial
	for _, requirement := range requirements {
		if strings.TrimSpace(requirement.Path) == "" {
			continue
		}
		if err := probe(requirement.Path); err != nil {
			denials = append(denials, Denial{Path: requirement.Path, Role: requirement.Role, Err: err})
		}
	}
	if len(denials) == 0 {
		return nil
	}
	return &Error{Root: root, Denials: denials}
}

// Requirements builds the declared writable set for a command: the private
// state directory, the platform temporary area, and — only when the selected
// store mode has one — the central checkout store. Repository-local mode keeps
// checkouts inside their canonical clone, so it declares no store root; the
// canonical clone is declared per repository by CanonicalRequirement.
func Requirements(stateDir, centralStore string) []Requirement {
	requirements := []Requirement{
		{Path: stateDir, Role: RoleState},
		{Path: os.TempDir(), Role: RoleTemp},
	}
	if strings.TrimSpace(centralStore) != "" {
		requirements = append(requirements, Requirement{Path: centralStore, Role: RoleStore})
	}
	return requirements
}

// CanonicalRequirement declares the canonical clone Git registration for one
// repository. WB needs the clone's .git directory writable to register,
// repair or remove a linked checkout.
func CanonicalRequirement(canonicalPath string) Requirement {
	return Requirement{Path: filepath.Join(canonicalPath, ".git"), Role: RoleCanonicalGit}
}

// OSProbe is the production probe. It walks up to the nearest existing
// directory — a declared path such as <root>/.wb usually does not exist yet —
// and creates and removes one uniquely named entry there, which is exactly the
// permission a later create needs. The entry is transient and dot-named, but it
// is a real write: probing a directory inside a canonical clone briefly adds and
// removes a `.wb-writable-probe-*` entry in it, because creating the checkout
// root is the permission being tested and no read-only check answers it.
func OSProbe(path string) error {
	directory, err := nearestExistingDirectory(path)
	if err != nil {
		return err
	}
	probe, err := os.CreateTemp(directory, ".wb-writable-probe-")
	if err != nil {
		return err
	}
	name := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	if removeErr := os.Remove(name); removeErr != nil {
		return removeErr
	}
	return nil
}

// nearestExistingDirectory returns path itself when it is a directory, else
// its nearest existing ancestor directory.
func nearestExistingDirectory(path string) (string, error) {
	// A symlink at the declared path is refused rather than probed through.
	// WB never writes state or a store through a symlinked leaf (see
	// wbhome.EnsureHome), and a dangling one would otherwise be probed through
	// its target's parent and reported writable when the create that follows
	// cannot possibly succeed.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("is a symlink; WB refuses to write through it")
	}
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				// The declared path exists as a non-directory: WB cannot write
				// children into it whatever the permissions say.
				return "", fmt.Errorf("exists but is not a directory")
			}
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor directory for %s", path)
		}
		current = parent
	}
}
