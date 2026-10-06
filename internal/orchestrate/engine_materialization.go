package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func operationWorktreePath(ctx context.Context, canonical, repository string, options Options, resolvedBase ResolvedBase) (string, worktrees.WorktreePlacement, string, bool, error) {
	if options.Resume {
		registered, err := registeredWorktreeForBranch(ctx, canonical, options.Branch, options)
		if err != nil {
			return "", worktrees.WorktreePlacement{}, "", false, err
		}
		if registered != "" {
			return registered, worktrees.WorktreePlacement{}, "", true, nil
		}
	}
	baseSHA, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "rev-parse", "--verify", "origin/"+resolvedBase.Ref+"^{commit}")
	if err != nil {
		return "", worktrees.WorktreePlacement{}, "", false, err
	}
	baseSHA = strings.TrimSpace(baseSHA)
	placement, err := worktrees.ResolveWorktreePlacement(ctx, options.GitHubDir, canonical, baseSHA)
	if err != nil {
		return "", worktrees.WorktreePlacement{}, "", false, err
	}
	worktree, err := placement.Path(options.Operation, repository)
	if err != nil {
		return "", worktrees.WorktreePlacement{}, "", false, err
	}
	return worktree, placement, baseSHA, false, nil
}

func registeredWorktreeForBranch(ctx context.Context, canonical, branch string, options Options) (string, error) {
	output, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	var path string
	for _, line := range strings.Split(output, "\n") {
		if candidate, ok := strings.CutPrefix(line, "worktree "); ok {
			path = candidate
			continue
		}
		if line == "branch refs/heads/"+branch {
			physicalPath, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr == nil && filepath.Clean(physicalPath) == filepath.Clean(canonical) {
				return "", fmt.Errorf("operation branch %q is checked out in canonical repository %s", branch, canonical)
			}
			return filepath.Clean(path), nil
		}
	}
	return "", nil
}

func prepareWorktree(ctx context.Context, canonical, repository, worktree string, placement worktrees.WorktreePlacement, baseSHA string, registeredResume bool, branch, base string, options Options) (*worktrees.PlacementWorktree, error) {
	if _, err := os.Stat(worktree); err == nil {
		if !options.Resume {
			return nil, fmt.Errorf("operation worktree already exists: %s (use --resume or choose a different operation)", worktree)
		}
		current, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "branch", "--show-current")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(current) != branch {
			return nil, fmt.Errorf("cannot resume worktree branch %q; want %q", strings.TrimSpace(current), branch)
		}
		return nil, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if registeredResume {
		return nil, fmt.Errorf("registered resume worktree disappeared: %s", worktree)
	}
	if _, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if !options.Resume {
			return nil, fmt.Errorf("operation branch already exists: %s (use --resume)", branch)
		}
	}
	return worktrees.CreateWorktreeAtPlacement(ctx, options.GitHubDir, canonical, placement, options.Operation, repository, branch, strings.TrimPrefix(base, "origin/"), baseSHA)
}

// recordWorktreeManifest gives every worktree this engine creates the WB
// manifest and originating-instruction record wb's own commit-admission
// hook requires (see internal/worktrees.CheckAdmission). Before this, a
// `wb deps bump`/`wb deps set` wave worktree had neither: wb created the
// worktree itself, applied a real change, and then its own pre-commit hook
// rejected the commit with "this worktree has no WB manifest, so nothing
// records what it is or who asked for it" — even though wb, not an
// unattended agent working around it, created the worktree. It is
// idempotent, so a --resume'd worktree that already carries a manifest and
// prompt from an earlier run is left untouched (a manifest is immutable by
// design; see worktrees.WriteManifest).
func recordWorktreeManifest(ctx context.Context, home, canonical, worktree string, repository Repository, resolvedBase ResolvedBase, creationBaseSHA string, branchCreated bool, options Options) error {
	_, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "rev-parse", "origin/"+resolvedBase.Ref)
	if err != nil {
		return err
	}
	owner, name, err := splitRepository(repository.Slug)
	if err != nil {
		return err
	}
	effortID := worktreeEffortID(options.Operation, owner, name)
	claimResult := worktrees.CreateResult{
		Repository: repository.Slug, WorktreeDir: worktree, Branch: options.Branch,
		Base: resolvedBase.Ref, BaseSHA: creationBaseSHA,
	}
	claimOptions := worktrees.WorkLogOptions{
		EffortID: effortID, RunID: options.Operation, Initiator: options.Initiator,
		AgentRuntime: options.AgentRuntime, Model: options.Model,
		CLI: options.CLI, Provider: options.Provider,
	}
	if !branchCreated {
		baseSHA, present, err := worktrees.OperationWorkLogBase(home, effortID, claimResult, claimOptions)
		if err != nil {
			return fmt.Errorf("record worktree Work Log claim: %w", err)
		}
		if !present {
			currentBase, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "rev-parse", "--verify", "origin/"+resolvedBase.Ref+"^{commit}")
			if err != nil {
				return err
			}
			recoveryCtx := ctx
			cancel := func() {}
			if options.Timeout > 0 {
				recoveryCtx, cancel = context.WithTimeout(ctx, options.Timeout)
			}
			baseSHA, err = worktrees.RecoverLegacyWorktreeBase(recoveryCtx, canonical, repository.Slug, options.Branch, strings.TrimSpace(currentBase))
			cancel()
			if err != nil {
				return fmt.Errorf("record worktree Work Log claim: %w", err)
			}
		}
		claimResult.BaseSHA = baseSHA
	}
	claimID := worktrees.WorkLogClaimID(effortID, claimResult)
	createdAt := time.Now().UTC()
	manifest := worktrees.Manifest{
		Version: 1, EffortID: effortID, ParentEffort: worktrees.ParentEffort(effortID),
		EffortKind: worktrees.EffortKindFor(effortID), Repository: repository.Slug, Worktree: worktree,
		Branch: options.Branch, Base: resolvedBase.Ref, BaseSHA: claimResult.BaseSHA,
		CreatedAt: createdAt, Initiator: options.Initiator, AgentRuntime: options.AgentRuntime,
		Model: options.Model, CLI: options.CLI, Provider: options.Provider,
		DependencyCampaign: options.DependencyCampaign,
		RunID:              options.Operation, ClaimID: claimID, Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.EnsureOperationManifest(worktree, manifest); err != nil {
		return fmt.Errorf("record worktree manifest: %w", err)
	}
	header := worktrees.PromptHeader{
		At: createdAt, Source: worktrees.PromptSourceAgent, Runtime: options.AgentRuntime,
		Model: options.Model, CLI: options.CLI, Provider: options.Provider, Slug: "operation",
	}
	if err := worktrees.EnsurePrompt(worktree, header, []byte(options.Prompt)); err != nil {
		return fmt.Errorf("record worktree originating instruction: %w", err)
	}
	if _, err := worktrees.EnsureWorkLogClaim(home, effortID, claimResult, claimOptions); err != nil {
		return fmt.Errorf("record worktree Work Log claim: %w", err)
	}
	return nil
}

// worktreeEffortID derives a valid worktrees.ValidEffortPath from an
// operation identity and a repository's owner/name, e.g.
// "deps-bump-go-c787f43a90d5-wave-01.sneat-co-ext-competios". The operation
// is the parent (feature-like) effort; each repository's worktree is a task
// effort beneath it.
func worktreeEffortID(operation, owner, name string) string {
	segment := worktreeEffortSegment(owner + "-" + name)
	if segment == "" {
		segment = "repository"
	}
	return operation + "." + segment
}

// worktreeEffortSegment sanitizes a value into a single
// worktrees.ValidEffortPath segment: alphanumeric, '.', '_', and '-' only,
// starting with an alphanumeric character.
func worktreeEffortSegment(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch {
		case isASCIIAlphanumeric(character),
			character == '.', character == '_', character == '-':
			output.WriteRune(character)
		default:
			output.WriteRune('-')
		}
	}
	segment := strings.Trim(output.String(), ".-_")
	if segment == "" {
		return ""
	}
	return segment
}

func isASCIIAlphanumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}
