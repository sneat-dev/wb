package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLegacyInventoryCannotDowngradeToOrdinaryApprovalOrLanding(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"valid", "malformed", "unterminated"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			binding, _ := legacyTestBinding(r)
			view := promotionView()
			view.Head.SHA = r.CandidateSHA
			view.Head.Ref = r.Integration
			view.Body = legacyInventoryPrefix + legacyJSON([]LegacyBinding{binding}) + " -->"
			if kind == "malformed" {
				view.Body = legacyInventoryPrefix + "wrong -->"
			}
			if kind == "unterminated" {
				view.Body = legacyInventoryPrefix
			}
			world := newPromoteWorld()
			options := PullRequestLandOptions{Repository: r.Repository, promotePorts: world.ports()}
			commits := []SourceCommit{{SHA: r.CandidateSHA, Parents: []string{r.TargetSHA, r.Sources[0]}}}
			receipt, refusal, err := promotionLandingGate(context.Background(), &options, view, "7", commits)
			if err != nil || refusal == nil || refusal.code != LandRefusalBatchBlockInvalid || receipt != nil || !options.NoAutoMerge || !options.NoUpdateBranch {
				t.Fatal("inventory downgrade", receipt, refusal, err, options.NoAutoMerge, options.NoUpdateBranch)
			}
			options.ApprovedBy = "opus@codex@ordinary-review"
			receipt, refusal, err = promotionLandingGate(context.Background(), &options, view, "7", commits)
			if err != nil || receipt != nil || refusal == nil || refusal.code != LandRefusalBatchBlockInvalid || !options.NoAutoMerge || !options.NoUpdateBranch {
				t.Fatalf("ordinary approval allowed inventory downgrade: %+v %+v %v", receipt, refusal, err)
			}

		})
	}
}

func TestLegacyArchiveSurvivesRemovalOfBothEditableBodyMarkers(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"archive", "changed archive", "outage", "ordinary merge", "ordinary linear", "all typed evidence erased", "invalid endpoint"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			binding, _ := legacyTestBinding(r)
			view := promotionView()
			view.Head.SHA = r.CandidateSHA
			view.Head.Ref = r.Integration
			view.Body = "ordinary text"
			world := newPromoteWorld()
			ports := world.ports()
			reads := 0
			ports.legacyComments = func(_ context.Context, repo, sha string) ([]LegacyBinding, error) {
				reads++
				if repo != r.Repository || sha != r.CandidateSHA {
					t.Fatal("foreign endpoint", repo, sha)
				}
				switch kind {
				case "archive", "changed archive":
					return []LegacyBinding{binding}, nil
				case "outage":
					return nil, errors.New("incomplete provider inventory")
				}
				return nil, nil
			}
			options := PullRequestLandOptions{Repository: r.Repository, promotePorts: ports}
			commits := []SourceCommit{{SHA: r.CandidateSHA, Parents: []string{r.TargetSHA, r.Sources[0]}}}
			if kind == "ordinary linear" {
				commits[0].Parents = commits[0].Parents[:1]
			}
			if kind == "invalid endpoint" {
				commits[0].SHA = "file:///private/path"
			}
			receipt, refusal, err := promotionLandingGate(context.Background(), &options, view, "7", commits)
			if kind == "ordinary merge" || kind == "ordinary linear" || kind == "all typed evidence erased" {
				if receipt != nil || refusal != nil || err != nil || options.NoAutoMerge || options.NoUpdateBranch {
					t.Fatal("ordinary path changed", receipt, refusal, err)
				}
				if kind == "ordinary linear" && reads != 0 {
					t.Fatal("unnecessary provider discovery")
				}
			} else if err == nil && refusal == nil {
				t.Fatal("durable evidence or unknown state bypassed gate", kind)
			}
			if kind == "archive" || kind == "changed archive" || kind == "outage" {
				if !options.NoAutoMerge || !options.NoUpdateBranch {
					t.Fatal("unsafe arming flags", kind)
				}
			}
			if kind == "invalid endpoint" && reads != 0 {
				t.Fatal("transport before endpoint validation")
			}
		})
	}
	if defaultPromoteLandingPorts().legacyComments == nil {
		t.Fatal("production durable archive discovery is not wired")
	}
}

func TestLegacyProjectionRejectsPrivateOrUnboundedIdentityComponents(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"model", "harness", "session", "original"} {
		for _, bad := range []string{"/Users/private/receipt", "C:\\private\\receipt", "not-a-session\nprivate", "control\x00identity", "control\x1bidentity", "space identity", strings.Repeat("a", 129), "../private", "user@example", "unknown"} {
			t.Run(field+"/"+legacyHash([]byte(bad)), func(t *testing.T) {
				t.Parallel()
				r := legacyTestRecord()
				switch field {
				case "model":
					r.Adopter.Model = bad
				case "harness":
					r.Adopter.Harness = bad
				case "session":
					r.Adopter.Session = bad
				case "original":
					r.OriginalMerger = bad
				}
				if err := validateLegacyRecord(r); err == nil {
					t.Fatal("unsafe identity accepted", field)
				}
			})
		}
	}
	for _, identity := range []ReviewerIdentity{{Model: "gpt-6.1-sol", Harness: "codex", Session: "019c6e27-e55b-73d1-87d8-4e01f1f75043"}, {Model: "opencode-go/deepseek-v4.1-flash", Harness: "opencode", Session: "valid-session"}, {Model: "claude-sonnet-4-5", Harness: "claude-code", Session: "wb-20261026T222308.123Z"}, {Model: "provider:model", Harness: "native", Session: strings.Repeat("a", 128)}} {
		r := legacyTestRecord()
		r.Adopter = identity
		r.OriginalMerger = ""
		if err := validateLegacyRecord(r); err != nil {
			t.Fatal("supported identity rejected", err)
		}
	}
}

func TestLegacyModelNamespacesRejectAbsoluteEmptyOrDotSegments(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"/provider/model", "C:/Users/private", "provider//model", "provider/../model", "./model", "provider/./model", "", strings.Repeat("a", 129)} {
		if safeLegacyModel(bad) {
			t.Fatal("unsafe model accepted")
		}
	}
	for _, valid := range []string{"gpt-6.1-sol", "opencode-go/deepseek-v4.1-flash", "provider/model:revision"} {
		if !safeLegacyModel(valid) {
			t.Fatal("supported model rejected", valid)
		}
	}
}
