package worktrees

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
		if evidence.Repository != options.Repository || evidence.Branch != options.Branch || len(evidence.Diagnostics) != 0 || len(evidence.Entries) != 1 {
			return fmt.Errorf("peer evidence for %s is not a complete exact branch inventory", evidence.Host)
		}
		entry := evidence.Entries[0]
		if entry.Repository != options.Repository || entry.Branch != options.Branch || entry.Scope != BranchScopeRemote || entry.Disposition == BranchInUse || entry.Disposition == BranchProtected || entry.Disposition == BranchUnreadable {
			return fmt.Errorf("peer evidence for %s reports unsafe branch state", evidence.Host)
		}
		var planned *BranchCleanupResult
		for index := range results {
			if results[index].Repository == options.Repository && results[index].Branch == options.Branch && results[index].Scope == BranchScopeRemote {
				planned = &results[index]
				break
			}
		}
		if planned == nil || entry.SHA != planned.SHA {
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
