package worktreeclaims

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

type LocalGit struct {
	Branch string
	Head   string
}

type RecoveryPorts struct {
	RepositoryRootFor func(context.Context, string) (string, error)
	ReadManifest      func(string) (Manifest, error)
	ObserveGit        func(context.Context, string) LocalGit
	Git               func(context.Context, string, ...string) (string, error)
	ClaimID           func(string, CreationResult) string
}

func (p RecoveryPorts) ResolveWorktreeRoot(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	return p.RepositoryRootFor(ctx, path)
}

func (p RecoveryPorts) ResolveLogBase(worktree, requested string) string {
	if base := strings.TrimSpace(requested); base != "" {
		return base
	}
	if manifest, err := p.ReadManifest(worktree); err == nil {
		if base := strings.TrimSpace(manifest.Base); base != "" {
			return base
		}
	}
	return "main"
}

func (p RecoveryPorts) RecoverableBlankManifestClaimID(ctx context.Context, root string, manifest Manifest) (string, error) {
	if !manifest.DependencyCampaign {
		return "", fmt.Errorf("claim establishment is supported only for dependency-campaign manifests")
	}
	if strings.TrimSpace(manifest.ClaimID) != "" {
		return "", fmt.Errorf("manifest already records ClaimID %q; refusing to establish a replacement claim", manifest.ClaimID)
	}
	if strings.TrimSpace(manifest.EffortID) == "" || strings.TrimSpace(manifest.Repository) == "" ||
		strings.TrimSpace(manifest.Worktree) == "" || strings.TrimSpace(manifest.Branch) == "" ||
		strings.TrimSpace(manifest.Base) == "" || !worktreeproof.IsGitObjectID(strings.TrimSpace(manifest.BaseSHA)) {
		return "", fmt.Errorf("immutable campaign manifest lacks complete checkout identity; refusing claim recovery")
	}
	if filepath.Clean(manifest.Worktree) != filepath.Clean(root) {
		return "", fmt.Errorf("immutable campaign manifest names worktree %q, not %q", manifest.Worktree, root)
	}
	evidence := p.ObserveGit(ctx, root)
	if evidence.Branch != manifest.Branch {
		return "", fmt.Errorf("live branch %q does not match immutable campaign manifest branch %q", evidence.Branch, manifest.Branch)
	}
	if _, err := p.Git(ctx, root, "merge-base", "--is-ancestor", manifest.BaseSHA, evidence.Head); err != nil {
		return "", fmt.Errorf("live HEAD is not descended from immutable campaign base %s: %w", manifest.BaseSHA, err)
	}
	return p.ClaimID(manifest.EffortID, CreationResult{
		Repository: manifest.Repository, WorktreeDir: root, Branch: manifest.Branch,
		Base: manifest.Base, BaseSHA: manifest.BaseSHA,
	}), nil
}

// RecoverBlankManifestClaim leaves publication in the caller's domain while
// keeping all immutable identity checks and option derivation here.
func RecoverBlankManifestClaim[T any](ctx context.Context, home, root string, manifest Manifest, ports RecoveryPorts, ensure func(string, string, CreationResult, Options) (T, error)) (T, error) {
	claimID, err := ports.RecoverableBlankManifestClaimID(ctx, root, manifest)
	if err != nil {
		var zero T
		return zero, err
	}
	task := ParentEffort(manifest.EffortID)
	if task == "" {
		task = manifest.EffortID
	}
	runID := strings.TrimSpace(manifest.RunID)
	if runID == "" {
		runID = "recovery-" + claimID[:16]
	}
	model := strings.TrimSpace(manifest.Model)
	if model == "" {
		model = "unknown"
	}
	return ensure(home, task, CreationResult{
		Repository: manifest.Repository, WorktreeDir: root, Branch: manifest.Branch,
		Base: manifest.Base, BaseSHA: manifest.BaseSHA,
	}, Options{
		EffortID: manifest.EffortID, RunID: runID, Initiator: manifest.Initiator,
		AgentID: manifest.AgentID, AgentRuntime: manifest.AgentRuntime, Model: model,
		CLI: manifest.CLI, Provider: manifest.Provider,
	})
}
