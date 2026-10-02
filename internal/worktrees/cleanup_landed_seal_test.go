package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// On 2026-10-02 only 62 of 3,679 sealed terminal records on the founder's
// machine said `landed`: every land verb ends in cleanup, and cleanup sealed
// each worktree it removed as `removed`, whatever proof of the merge it held.
// These tests pin the disposition to that proof.

var (
	landedSealHead   = strings.Repeat("a", 40)
	landedSealBase   = strings.Repeat("b", 40)
	landedSealSquash = strings.Repeat("c", 40)
	landedSealTarget = strings.Repeat("d", 40)
)

func TestCleanupProofOfLandingNamesTheTargetAndTheCommitThatCarriesTheWork(t *testing.T) {
	t.Parallel()
	integrated := ListResult{Branch: "topic", Base: "main", HeadSHA: landedSealHead, RemoteTargetSHA: landedSealTarget, IntegratedAtOrigin: true}
	merged := &PullRequest{Number: 17, MergeSHA: landedSealSquash}
	cases := map[string]struct {
		edit func(*ListResult)
		want *worktreeclaims.LandedEvidence
	}{
		"head contained in the target": {
			edit: func(*ListResult) {},
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealHead, Proof: worktreeclaims.LandedProofContained},
		},
		"contained with a merged pull request": {
			edit: func(entry *ListResult) { entry.MergedPullRequest = merged },
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealHead, Proof: worktreeclaims.LandedProofMergedPullRequest, PullRequest: 17},
		},
		"squash merged": {
			edit: func(entry *ListResult) {
				entry.MergedPullRequest, entry.AbsorbedAtOrigin, entry.AbsorbedBySHA = merged, true, landedSealSquash
			},
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealSquash, Proof: worktreeclaims.LandedProofAbsorbed, PullRequest: 17},
		},
		"absorbed by a batch without a pull request": {
			edit: func(entry *ListResult) { entry.AbsorbedAtOrigin, entry.AbsorbedBySHA = true, landedSealSquash },
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealSquash, Proof: worktreeclaims.LandedProofAbsorbed},
		},
		"acknowledged absorption names the proved target": {
			edit: func(entry *ListResult) { entry.AbsorbedAtOrigin = true },
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealTarget, Proof: worktreeclaims.LandedProofAbsorbed},
		},
		"rebase merged": {
			edit: func(entry *ListResult) { entry.MergedPullRequest, entry.RebaseMergedAtOrigin = merged, true },
			want: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealSquash, Proof: worktreeclaims.LandedProofRebaseMerged, PullRequest: 17},
		},
		"rebase merged without its pull request": {
			edit: func(entry *ListResult) { entry.RebaseMergedAtOrigin = true },
		},
		"not integrated": {
			edit: func(entry *ListResult) { entry.IntegratedAtOrigin = false },
		},
		"removed past residual commits": {
			edit: func(entry *ListResult) { entry.IntegratedAtOrigin, entry.MergedPullRequest = false, merged },
		},
		"detached review checkout": {
			edit: func(entry *ListResult) { entry.Detached, entry.Branch = true, "" },
		},
		"candidate of a deleted integration target": {
			edit: func(entry *ListResult) { entry.RetiredPrepareCandidateAcknowledgementPath = "/ack.json" },
		},
		"no target name": {
			edit: func(entry *ListResult) { entry.Base = "" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := integrated
			tc.edit(&entry)
			if got := cleanupLandedEvidence(entry); !worktreeclaims.SameLandedEvidence(got, tc.want) {
				t.Fatalf("proof = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCleanupSealsLandedOnlyWorkTheClaimMadeAndTheTargetReceived(t *testing.T) {
	t.Parallel()
	proof := &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealHead, Proof: worktreeclaims.LandedProofContained}
	claim := workLogClaim{Branch: "topic", BaseSHA: landedSealBase}
	ahead := func(string, string) bool { return true }
	cases := map[string]struct {
		claim       workLogClaim
		head        string
		proof       *worktreeclaims.LandedEvidence
		addsCommits func(string, string) bool
		want        string
	}{
		"merged work":                     {claim, landedSealHead, proof, ahead, "landed"},
		"no proof of a landing":           {claim, landedSealHead, nil, ahead, "removed"},
		"worktree that never committed":   {claim, landedSealBase, proof, ahead, "removed"},
		"branch reset behind its base":    {claim, landedSealHead, proof, func(string, string) bool { return false }, "removed"},
		"integration candidate":           {workLogClaim{Branch: "wb/integration/main/abc", BaseSHA: landedSealBase}, landedSealHead, proof, ahead, "removed"},
		"claim without a branch":          {workLogClaim{BaseSHA: landedSealBase}, landedSealHead, proof, ahead, "removed"},
		"branch merely named integration": {workLogClaim{Branch: "integration/main", BaseSHA: landedSealBase}, landedSealHead, proof, ahead, "landed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			disposition, evidence := cleanupSealChoice(tc.claim, tc.head, tc.proof, tc.addsCommits)
			if disposition != tc.want {
				t.Fatalf("disposition = %q, want %q", disposition, tc.want)
			}
			if (evidence.Landed != nil) != (tc.want == "landed") || (tc.want == "landed" && evidence.Landed != tc.proof) {
				t.Fatalf("%s terminal carries %#v", disposition, evidence.Landed)
			}
		})
	}
}

func TestCleanupFirstSealAsksGitWhetherTheHeadIsBehindTheClaimsBase(t *testing.T) {
	t.Parallel()
	proof := &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: landedSealHead, Proof: worktreeclaims.LandedProofContained}
	cases := map[string]struct {
		behind bool
		err    error
		want   string
	}{
		"head ahead of the base":       {false, nil, "landed"},
		"head behind the base":         {true, nil, "removed"},
		"ancestry that cannot be read": {false, errors.New("git failed"), "removed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("seal failed")
			var sealed string
			ports := removalSealPorts{
				seal: func(home, worktree, finalCommit string, choose func(workLogClaim) (string, worktreeclaims.TerminalEvidence)) error {
					if home != "home" || worktree != "/worktree" || finalCommit != landedSealHead {
						t.Fatalf("seal of %q %q %q", home, worktree, finalCommit)
					}
					sealed, _ = choose(workLogClaim{Branch: "topic", BaseSHA: landedSealBase})
					return failure
				},
				isAncestor: func(_ context.Context, repository, ancestor, descendant string) (bool, error) {
					if repository != "/worktree" || ancestor != landedSealHead || descendant != landedSealBase {
						t.Fatalf("ancestry asked of %q: %q in %q", repository, ancestor, descendant)
					}
					return tc.behind, tc.err
				},
			}
			if err := ports.sealWorkLogForRemoval("home", "/worktree", landedSealHead, proof); !errors.Is(err, failure) {
				t.Fatalf("seal error = %v", err)
			}
			if sealed != tc.want {
				t.Fatalf("sealed %q, want %q", sealed, tc.want)
			}
		})
	}
}

func TestCleanupFirstSealOfACheckoutWithoutAWorkLogIsNothing(t *testing.T) {
	t.Parallel()
	if err := sealWorkLogForRemoval("home", t.TempDir(), landedSealHead, nil); err != nil {
		t.Fatalf("legacy checkout: %v", err)
	}
}
