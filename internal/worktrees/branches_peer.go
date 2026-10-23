package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func compactStrings(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func validatePeerEvidence(options BranchCleanupOptions, results []BranchCleanupResult, now time.Time) error {
	if len(options.PeerEvidence) == 0 && len(options.RequireHosts) == 0 {
		return nil
	}
	evidence := make([]worktreebranches.PeerEvidence, 0, len(options.PeerEvidence))
	for _, path := range options.PeerEvidence {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read peer evidence %s: %w", path, err)
		}
		var decoded worktreebranches.PeerEvidence
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return fmt.Errorf("decode peer evidence %s: %w", path, err)
		}
		decoded.Path = path
		evidence = append(evidence, decoded)
	}
	return worktreebranches.ValidatePeerEvidence(worktreebranches.PeerEvidenceValidation{
		LocalHost: branchEvidenceHost(), Repository: options.Repository, Branch: options.Branch,
		Base: options.Base, RequireHosts: options.RequireHosts, Evidence: evidence,
		Results: results, Now: now,
	})
}

// reviewedRemoteForkGuard refuses reviewed retirement from a fork. GitHub's
// fork-scoped commit-to-pull-request endpoint cannot prove that the same
// branch is not the head of an upstream pull request.
func reviewedRemoteForkGuard(ctx context.Context, repositoryPath, repository string) error {
	response := githubobserver.Execute(ctx, repositoryPath, "api", "--paginate", "repos/"+repository)
	if response.Err != nil {
		return fmt.Errorf("query repository fork status: %w: %s", response.Err, strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
	}
	return admitReviewedRemoteForkMetadata(response.Stdout, repository)
}

// admitReviewedRemoteForkMetadata admits arbitrary observed response bytes; it
// never treats a missing fork declaration as proof of upstream ownership.
func admitReviewedRemoteForkMetadata(raw []byte, repository string) error {
	var metadata struct {
		Fork *bool `json:"fork"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return fmt.Errorf("decode repository fork status: %w", err)
	}
	if metadata.Fork == nil {
		return fmt.Errorf("repository fork status is missing")
	}
	if *metadata.Fork {
		return fmt.Errorf("reviewed remote retirement refuses fork repository %s because upstream pull-request ownership cannot be proven", repository)
	}
	return nil
}
