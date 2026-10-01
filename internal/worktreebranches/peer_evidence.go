package worktreebranches

import (
	"fmt"
	"time"
)

const peerEvidenceMaximumAge = 5 * time.Minute

// PeerEvidence is decoded inventory evidence; the facade owns file reads.
type PeerEvidence struct {
	Path        string        `json:"-"`
	Host        string        `json:"host"`
	GeneratedAt time.Time     `json:"generated_at"`
	Repository  string        `json:"repository"`
	Branch      string        `json:"branch"`
	Base        string        `json:"base"`
	Diagnostics []string      `json:"diagnostics,omitempty"`
	Entries     []BranchEntry `json:"entries"`
}

type PeerEvidenceValidation struct {
	LocalHost, Repository, Branch, Base string
	RequireHosts                        []string
	Evidence                            []PeerEvidence
	Results                             []BranchCleanupResult
	Now                                 time.Time
}

func ValidatePeerEvidence(request PeerEvidenceValidation) error {
	if len(request.Evidence) == 0 && len(request.RequireHosts) == 0 {
		return nil
	}
	required := map[string]bool{}
	for _, host := range request.RequireHosts {
		required[host] = true
	}
	if !required[request.LocalHost] {
		return fmt.Errorf("--require-host must include local host %q", request.LocalHost)
	}
	seen := map[string]bool{}
	for _, evidence := range request.Evidence {
		if evidence.Host == "" || !required[evidence.Host] || seen[evidence.Host] {
			return fmt.Errorf("peer evidence %s has missing, unrequired, or duplicate host", evidence.Path)
		}
		if evidence.GeneratedAt.IsZero() || evidence.GeneratedAt.After(request.Now) || request.Now.Sub(evidence.GeneratedAt) > peerEvidenceMaximumAge {
			return fmt.Errorf("peer evidence for %s is stale or future-dated", evidence.Host)
		}
		if evidence.Repository != request.Repository || evidence.Branch != request.Branch || evidence.Base != request.Base || len(evidence.Diagnostics) != 0 || len(evidence.Entries) != 1 {
			return fmt.Errorf("peer evidence for %s is not a complete exact branch inventory", evidence.Host)
		}
		entry := evidence.Entries[0]
		if entry.Repository != request.Repository || entry.Branch != request.Branch || entry.Base != request.Base || entry.Scope != BranchScopeRemote || !PeerEvidenceSafeDisposition(entry.Disposition) {
			return fmt.Errorf("peer evidence for %s reports unsafe branch state", evidence.Host)
		}
		var planned *BranchCleanupResult
		for index := range request.Results {
			if request.Results[index].Repository == request.Repository && request.Results[index].Branch == request.Branch && request.Results[index].Scope == BranchScopeRemote {
				planned = &request.Results[index]
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
