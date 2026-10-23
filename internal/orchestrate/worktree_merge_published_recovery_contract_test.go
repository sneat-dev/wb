package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestPublishedRecoveryIdentityAndRequiredCheckContracts(t *testing.T) {
	t.Parallel()
	t.Run("long lane preserves bounded readable prefix and full identity", func(t *testing.T) {
		t.Parallel()
		repository, target := "acme/"+strings.Repeat("product", 10), "release_target"
		lane := worktreeMergeLaneID(repository, target)
		digest := sha256.Sum256([]byte(repository + "\x00" + target))
		want := "merge-" + ("acme-" + strings.Repeat("product", 10))[:42] + "-" + hex.EncodeToString(digest[:6])
		if lane != want || len(lane) != 61 || lane == worktreeMergeLaneID(repository+"x", target) {
			t.Fatalf("bounded exact lane = %q, wanted %q", lane, want)
		}
		if worktreeMergeLaneID("acme/app", "main") == worktreeMergeLaneID("acme", "app/main") || strings.Contains(worktreeMergeLaneID("._/", "_"), "/") {
			t.Fatal("lane lost repository/target boundary or safe readable form")
		}
	})
	t.Run("operation suffix remains usable for legacy identities", func(t *testing.T) {
		t.Parallel()
		for _, row := range []struct{ operation, suffix string }{{"merge-lane-abc", "abc"}, {"legacy", "legacy"}, {"legacy-", "legacy-"}, {"", ""}, {"-abc", "abc"}} {
			if got := mergeOperationSuffix(row.operation); got != row.suffix {
				t.Fatalf("suffix %q = %q, wanted %q", row.operation, got, row.suffix)
			}
		}
	})
	t.Run("required check is exact and may be absent", func(t *testing.T) {
		t.Parallel()
		checks := []githubchecks.RequiredRemoteCheck{{Name: "optional"}, {Name: "Go CI"}}
		if hasRequiredRemoteCheck(nil, "Go CI") || hasRequiredRemoteCheck(checks, "go ci") || hasRequiredRemoteCheck(checks, "absent") || !hasRequiredRemoteCheck(checks, "Go CI") {
			t.Fatal("required check name was normalized or absence accepted")
		}
	})
}

func TestPublishedRecoveryDirectCIReadAndIdentityRefusalsPreserveReceipt(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"read refusal", "exact", "closed", "merged", "head branch", "head owner missing", "head owner", "base branch", "base owner missing", "base owner", "head SHA"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "integration", Candidate: WorktreeMergeCandidate{SHA: "retained"}}
			before := receipt
			contract := worktreeMergeDirectCIContract{PullRequest: "17", Base: "main", WorkflowID: 300}
			cause := errors.New("owned exact PR read refused")
			view := `{"number":17,"state":"open","merged":false,"head":{"ref":"integration","sha":"exact-head","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`
			switch stage {
			case "closed":
				view = strings.Replace(view, `"open"`, `"closed"`, 1)
			case "merged":
				view = strings.Replace(view, `"merged":false`, `"merged":true`, 1)
			case "head branch":
				view = strings.Replace(view, `"integration"`, `"other"`, 1)
			case "head owner missing":
				view = strings.Replace(view, `"repo":{"full_name":"acme/app"}`, `"repo":null`, 1)
			case "head owner":
				view = strings.Replace(view, `"full_name":"acme/app"`, `"full_name":"other/app"`, 1)
			case "base branch":
				view = strings.Replace(view, `"main"`, `"release"`, 1)
			case "base owner missing":
				view = strings.Replace(view, `"base":{"ref":"main","repo":{"full_name":"acme/app"}}`, `"base":{"ref":"main","repo":null}`, 1)
			case "base owner":
				view = strings.Replace(view, `"base":{"ref":"main","repo":{"full_name":"acme/app"}}`, `"base":{"ref":"main","repo":{"full_name":"other/app"}}`, 1)
			case "head SHA":
				view = strings.Replace(view, `"exact-head"`, `"advanced-head"`, 1)
			}
			reads := 0
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
				reads++
				if request.Dir != "" || request.Repository != "acme/app" || request.Endpoint != "repos/acme/app/pulls/17" || request.FreshWindow != 0 {
					t.Fatalf("exact PR read = %+v", request)
				}
				if stage == "read refusal" {
					return githubobserver.Response{}, cause
				}
				return githubobserver.Response{Body: []byte(view)}, nil
			}, Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
				t.Fatal("direct CI identity inspection attempted a mutation")
				return githubobserver.CommandResponse{}
			}})
			err := verifyWorktreeMergeDirectCIPullRequest(ctx, receipt, contract, "exact-head")
			if stage == "read refusal" {
				if !errors.Is(err, cause) {
					t.Fatalf("read cause lost: %v", err)
				}
			} else if stage == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != "direct CI pull request 17 no longer points from integration@exact-head to main" {
				t.Fatalf("identity refusal = %v", err)
			}
			if reads != 1 || !reflect.DeepEqual(receipt, before) {
				t.Fatalf("inspection changed receipt or read count: %d %+v", reads, receipt)
			}
		})
	}
}

func TestPublishedRecoveryRefreshRequiresReceiptAndTargetBeforeCommands(t *testing.T) {
	t.Parallel()
	if err := refreshPublishedWorktreeMergeCandidateTarget(t.Context(), nil, "target", 0, 0); err == nil || err.Error() != "refresh published candidate: receipt is required" {
		t.Fatalf("nil receipt %v", err)
	}
	receipt := WorktreeMergeReceipt{TargetSHA: "retained", Candidate: WorktreeMergeCandidate{SHA: "retained", Worktree: t.TempDir()}}
	before := receipt
	if err := refreshPublishedWorktreeMergeCandidateTarget(t.Context(), &receipt, " \t", 0, 0); err == nil || err.Error() != "refresh published candidate: remote target revision is required" || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("missing target changed receipt %+v %v", receipt, err)
	}
}
