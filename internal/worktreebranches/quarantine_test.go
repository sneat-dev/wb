package worktreebranches

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQuarantineServiceChecksPlanAndApplyClaimsSeparately(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	request := BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "retired"}
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	claims := 0
	service := InventoryService{Ports: InventoryPorts{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "rev-parse --verify refs/heads/feature/old^{commit}":
				return sha, nil
			case "rev-parse --abbrev-ref HEAD":
				return "main", nil
			default:
				return "", errors.New("not found")
			}
		},
		CheckedOut: func(context.Context, string) (map[string]bool, string) { return map[string]bool{}, "" },
		InUse: func(context.Context, string, string) (map[string]string, string) {
			claims++
			if claims == 2 {
				return map[string]string{BranchInUseKey(request.Repository, request.Ref): "task"}, ""
			}
			return map[string]string{}, ""
		},
		OpenHeadPull: func(context.Context, string, string, string, string) (*PullRequest, error) { return nil, nil },
		OpenBasePull: func(context.Context, string, string, string) (*PullRequest, error) { return nil, nil },
	}}
	planned := service.PlanBranchQuarantine(context.Background(), "/projects", "/repo", request, now)
	if planned.Outcome != "planned" || planned.Destination != "retired/20260930-feature-old-0123456789ab" {
		t.Fatalf("plan = %+v", planned)
	}
	if _, refusal := service.InspectBranchQuarantineCandidate(context.Background(), "/projects", "/repo", planned.BranchQuarantineRequest, planned.Destination, true); refusal != "source became claimed by a live WB work log" {
		t.Fatalf("apply claim refusal = %q", refusal)
	}
	if claims != 2 {
		t.Fatalf("claim checks = %d, want plan and apply", claims)
	}
}

func TestValidateDecodedPeerEvidenceChecksHostAndExactHead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	entry := BranchEntry{Repository: "acme/app", Branch: "feature/old", Base: "main", Scope: BranchScopeRemote,
		Disposition: BranchUnique, SHA: "head", TargetSHA: "target"}
	base := PeerEvidenceValidation{LocalHost: "local", Repository: entry.Repository, Branch: entry.Branch, Base: entry.Base,
		RequireHosts: []string{"local"}, Evidence: []PeerEvidence{{Path: "evidence.json", Host: "local", GeneratedAt: now,
			Repository: entry.Repository, Branch: entry.Branch, Base: entry.Base, Entries: []BranchEntry{entry}}},
		Results: []BranchCleanupResult{{BranchEntry: entry}}, Now: now}
	if err := ValidatePeerEvidence(base); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePeerEvidence(PeerEvidenceValidation{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		change     func(*PeerEvidenceValidation)
	}{
		{"missing local host", "must include local host", func(v *PeerEvidenceValidation) { v.RequireHosts = []string{"peer"} }},
		{"duplicate host", "duplicate host", func(v *PeerEvidenceValidation) { v.Evidence = append(v.Evidence, v.Evidence[0]) }},
		{"different head", "does not match planned branch head", func(v *PeerEvidenceValidation) { v.Evidence[0].Entries[0].SHA = "other" }},
		{"missing peer", "required peer evidence", func(v *PeerEvidenceValidation) { v.RequireHosts = []string{"local", "peer"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			v.RequireHosts = append([]string(nil), base.RequireHosts...)
			v.Evidence = append([]PeerEvidence(nil), base.Evidence...)
			v.Evidence[0].Entries = append([]BranchEntry(nil), base.Evidence[0].Entries...)
			tc.change(&v)
			if err := ValidatePeerEvidence(v); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRetiredBranchDestinationUsesFallbackForUnsafeSource(t *testing.T) {
	t.Parallel()
	got := RetiredBranchDestination(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), "///", "abcdef")
	if got != "retired/20260930-branch-abcdef" {
		t.Fatal(got)
	}
}
