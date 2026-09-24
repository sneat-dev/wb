package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

const peerEvidenceMaximumAge = 5 * time.Minute

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
	required := map[string]bool{}
	for _, host := range options.RequireHosts {
		required[host] = true
	}
	if !required[branchEvidenceHost()] {
		return fmt.Errorf("--require-host must include local host %q", branchEvidenceHost())
	}
	seen := map[string]bool{}
	for _, path := range options.PeerEvidence {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read peer evidence %s: %w", path, err)
		}
		var evidence BranchListOutcome
		if err := json.Unmarshal(raw, &evidence); err != nil {
			return fmt.Errorf("decode peer evidence %s: %w", path, err)
		}
		if evidence.Host == "" || !required[evidence.Host] || seen[evidence.Host] {
			return fmt.Errorf("peer evidence %s has missing, unrequired, or duplicate host", path)
		}
		if evidence.GeneratedAt.IsZero() || evidence.GeneratedAt.After(now) || now.Sub(evidence.GeneratedAt) > peerEvidenceMaximumAge {
			return fmt.Errorf("peer evidence for %s is stale or future-dated", evidence.Host)
		}
		if evidence.Repository != options.Repository || evidence.Branch != options.Branch || evidence.Base != options.Base || len(evidence.Diagnostics) != 0 || len(evidence.Entries) != 1 {
			return fmt.Errorf("peer evidence for %s is not a complete exact branch inventory", evidence.Host)
		}
		entry := evidence.Entries[0]
		if entry.Repository != options.Repository || entry.Branch != options.Branch || entry.Base != options.Base || entry.Scope != BranchScopeRemote || !peerEvidenceSafeDisposition(entry.Disposition) {
			return fmt.Errorf("peer evidence for %s reports unsafe branch state", evidence.Host)
		}
		var planned *BranchCleanupResult
		for index := range results {
			if results[index].Repository == options.Repository && results[index].Branch == options.Branch && results[index].Scope == BranchScopeRemote {
				planned = &results[index]
				break
			}
		}
		if planned == nil || entry.SHA != planned.SHA || entry.TargetSHA != planned.TargetSHA {
			return fmt.Errorf("peer evidence for %s does not match planned branch head", evidence.Host)
		}
		seen[evidence.Host] = true
	}
	for host := range required {
		if !seen[host] {
			return fmt.Errorf("required peer evidence for host %s is missing", host)
		}
	}
	return nil
}

func peerEvidenceSafeDisposition(disposition string) bool {
	switch disposition {
	case BranchContained, BranchReceipted, BranchAbsorbed, BranchUnique:
		return true
	default:
		return false
	}
}

// reviewedRemoteForkGuard refuses reviewed retirement from a fork. GitHub's
// fork-scoped commit-to-pull-request endpoint cannot prove that the same
// branch is not the head of an upstream pull request.
func reviewedRemoteForkGuard(ctx context.Context, repositoryPath, repository string) error {
	response := githubobserver.Execute(ctx, repositoryPath, "api", "--paginate", "repos/"+repository)
	if response.Err != nil {
		return fmt.Errorf("query repository fork status: %w: %s", response.Err, strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
	}
	var metadata struct {
		Fork *bool `json:"fork"`
	}
	if err := json.Unmarshal(response.Stdout, &metadata); err != nil {
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
