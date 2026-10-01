package worktreebranches

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BranchQuarantineRequest identifies one exact local ref selected for retirement.
type BranchQuarantineRequest struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	SHA        string `json:"sha"`
	Reason     string `json:"reason"`
}

type BranchQuarantineResult struct {
	BranchQuarantineRequest
	Destination string `json:"destination,omitempty"`
	Outcome     string `json:"outcome"`
	Error       string `json:"error,omitempty"`
}

// PlanBranchQuarantine proves the source is safe to rename and reserves its
// deterministic destination in the plan. Apply must repeat the proof.
func (service InventoryService) PlanBranchQuarantine(ctx context.Context, projectsRoot, path string, request BranchQuarantineRequest, now time.Time) BranchQuarantineResult {
	result := BranchQuarantineResult{BranchQuarantineRequest: request, Outcome: "refused"}
	source, refusal := service.InspectBranchQuarantineCandidate(ctx, projectsRoot, path, request, "", false)
	if refusal != "" {
		result.Error = refusal
		return result
	}
	result.SHA = source
	result.Destination = RetiredBranchDestination(now, request.Ref, source)
	if _, err := service.Ports.Git(ctx, path, "rev-parse", "--verify", "refs/heads/"+result.Destination); err == nil {
		result.Error = "destination already exists"
		return result
	}
	result.Outcome = "planned"
	return result
}

// InspectBranchQuarantineCandidate keeps the plan and apply proofs distinct:
// both inspect current evidence, while apply also checks the destination and
// repeats the live claim check immediately before the caller's CAS rename.
func (service InventoryService) InspectBranchQuarantineCandidate(
	ctx context.Context,
	projectsRoot, path string,
	request BranchQuarantineRequest,
	destination string,
	applying bool,
) (string, string) {
	source, err := service.Ports.Git(ctx, path, "rev-parse", "--verify", "refs/heads/"+request.Ref+"^{commit}")
	if err != nil {
		if applying {
			return "", "source disappeared before apply: " + err.Error()
		}
		return "", "source ref unavailable: " + err.Error()
	}
	source = strings.TrimSpace(source)
	if request.SHA != "" && request.SHA != source {
		if applying {
			return "", fmt.Sprintf("source moved from %s to %s before apply", ShortSHA(request.SHA), ShortSHA(source))
		}
		return "", fmt.Sprintf("source moved from manifest SHA %s to %s", ShortSHA(request.SHA), ShortSHA(source))
	}
	head, _ := service.Ports.Git(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if IsProtectedBranch(request.Ref, "main", strings.TrimSpace(head)) {
		if applying {
			return "", "source became protected or the canonical current branch"
		}
		return "", "source is protected or the canonical current branch"
	}
	checked, diagnostic := service.Ports.CheckedOut(ctx, path)
	if diagnostic != "" {
		return "", diagnostic
	}
	if checked[request.Ref] {
		if applying {
			return "", "source became checked out in a linked worktree"
		}
		return "", "source is checked out in a linked worktree"
	}
	if !applying {
		if refusal := service.branchQuarantineClaimRefusal(ctx, projectsRoot, request, false); refusal != "" {
			return "", refusal
		}
	} else if _, err := service.Ports.Git(ctx, path, "rev-parse", "--verify", "refs/heads/"+destination); err == nil {
		return "", "destination appeared before apply"
	}
	open, err := service.Ports.OpenHeadPull(ctx, path, request.Repository, request.Ref, source)
	if err != nil {
		if applying {
			return "", "cannot re-prove pull-request safety: " + err.Error()
		}
		return "", "cannot prove pull-request safety: " + err.Error()
	}
	if open != nil {
		if applying {
			return "", "source became head of open pull request " + open.URL
		}
		return "", "source is head of open pull request " + open.URL
	}
	if open, err := service.Ports.OpenBasePull(ctx, path, request.Repository, request.Ref); err != nil {
		if applying {
			return "", "cannot re-prove pull-request base safety: " + err.Error()
		}
		return "", "cannot prove pull-request base safety: " + err.Error()
	} else if open != nil {
		if applying {
			return "", "source became base of open pull request " + open.URL
		}
		return "", "source is base of open pull request " + open.URL
	}
	if applying {
		if refusal := service.branchQuarantineClaimRefusal(ctx, projectsRoot, request, true); refusal != "" {
			return "", refusal
		}
	}
	return source, ""
}

func (service InventoryService) branchQuarantineClaimRefusal(ctx context.Context, projectsRoot string, request BranchQuarantineRequest, applying bool) string {
	inUse, diagnostic := service.Ports.InUse(ctx, projectsRoot, "")
	if diagnostic != "" {
		return diagnostic
	}
	if _, claimed := inUse[BranchInUseKey(request.Repository, request.Ref)]; !claimed {
		return ""
	}
	if applying {
		return "source became claimed by a live WB work log"
	}
	return "source is claimed by a live WB work log"
}
