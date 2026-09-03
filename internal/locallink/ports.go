package locallink

import "context"

// Git is the local Git surface local propagation needs. Every method is
// read-only except ExcludePaths, which writes only to the worktree's own
// exclude file — never to a tracked `.gitignore`.
type Git interface {
	// ContentHash identifies a working tree, including modified and untracked
	// files. The library is uncommitted by construction, so it has no SHA;
	// dirty reports whether the tree differs from HEAD.
	ContentHash(ctx context.Context, dir string) (hash string, dirty bool, err error)
	// TrackedChanges lists paths of *tracked* files that differ from HEAD.
	// Untracked paths are excluded: a link creates untracked artefacts by
	// design, and only a tracked change is a violation.
	TrackedChanges(ctx context.Context, dir string) ([]string, error)
	// ExcludePath adds one pattern to the worktree's own Git exclude file,
	// resolved through `git rev-parse --git-path info/exclude` so the path is
	// the one Git will actually read for this worktree.
	ExcludePath(ctx context.Context, dir, pattern string) error
	// ExcludedPatterns reads that exclude file.
	ExcludedPatterns(ctx context.Context, dir string) ([]string, error)
}

// Node builds and links npm packages through the consumer's and library's own
// package managers.
type Node interface {
	// FrozenInstall proves a clean frozen install of the unlinked consumer
	// tree, so a link never masks a lockfile or manifest mismatch.
	FrozenInstall(ctx context.Context, dir string) error
	// Build runs the library repository's own build target and returns the
	// directory holding the built package.
	Build(ctx context.Context, libraryDir, packageDir string) (dist string, err error)
	// Link points the consumer's node_modules entry for packageName at dist,
	// returning what was there before so --undo restores it exactly. It must
	// not modify any manifest.
	Link(ctx context.Context, consumerDir, packageName, dist string) (previous string, err error)
	// Unlink restores the node_modules entry recorded by Link.
	Unlink(ctx context.Context, consumerDir, packageName string) error
}

// Verifier runs a consumer's own lint and tests.
type Verifier interface {
	// Verify runs the consumer's lint and tests, constrained to a single
	// worker, with env applied on top of the process environment.
	Verify(ctx context.Context, dir string, env []string) (VerificationRun, error)
	// BuildAndVet runs a build and vet with GOWORK=off, which is the
	// pre-landing check proving the consumer still resolves its *published*
	// dependency.
	BuildAndVet(ctx context.Context, dir string) (VerificationRun, error)
}

// VerificationRun is one verification pass over a consumer.
type VerificationRun struct {
	Passed  bool     `json:"passed"`
	Command string   `json:"command,omitempty"`
	Details []string `json:"details,omitempty"`
}
