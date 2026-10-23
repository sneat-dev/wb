package orchestrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// protocolShapeReceipt is syntax/identity-policy input only. No Git, claim,
// native validation or custody fact is represented by these DTO strings.
func protocolShapeReceipt(t *testing.T) WorktreeMergeReceipt {
	t.Helper()
	r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed, Sources: []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(t.TempDir(), "source"), Branch: "feature/source", SHA: strings.Repeat("b", 40)}}, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC()}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.ReceiptPath = filepath.Join(t.TempDir(), r.ID+".json")
	r.Candidate = WorktreeMergeCandidate{Task: r.ID, Worktree: filepath.Join(t.TempDir(), "candidate"), Branch: "feature/candidate", SHA: strings.Repeat("c", 40)}
	r.Validation = quality.VerificationReport{Repository: r.Repository, Path: r.Candidate.Worktree, Revision: r.Candidate.SHA, Status: quality.StatusFailed}
	return r
}

func TestReplacementProtocolShapePreservesDistinctModernAndLegacyPolicies(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict", "legacy validation", "legacy conflict"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := protocolShapeReceipt(t)
			check := validateValidationFailedSupersessionReceipt
			switch kind {
			case "conflict":
				r.Status = WorktreeMergeConflict
				check = validatePrepareFailureSupersessionReceipt
			case "legacy validation":
				r.Candidate.SHA = ""
				check = validateLegacyValidationFailedReceiptShape
			case "legacy conflict":
				r.Status = WorktreeMergeConflict
				r.Candidate.SHA = ""
				check = validateLegacyConflictReceiptShape
			}
			if err := check(r, r.ReceiptPath); err != nil {
				t.Fatalf("shape-only valid %s identity=%v", kind, err)
			}
			for _, field := range []string{"path", "schema", "phase", "status", "repository", "target", "target SHA", "sources", "candidate task", "candidate worktree", "candidate branch", "candidate SHA", "created", "updated", "lane", "operation", "landing", "published"} {
				t.Run(field, func(t *testing.T) {
					t.Parallel()
					bad := r
					switch field {
					case "path":
						bad.ReceiptPath += ".different"
					case "schema":
						bad.SchemaVersion++
					case "phase":
						bad.Phase = WorktreeMergePhaseLand
					case "status":
						bad.Status = WorktreeMergePrepared
					case "repository":
						bad.Repository = ""
					case "target":
						bad.Target = ""
					case "target SHA":
						bad.TargetSHA = ""
					case "sources":
						bad.Sources = nil
					case "candidate task":
						bad.Candidate.Task = "other"
					case "candidate worktree":
						bad.Candidate.Worktree = ""
					case "candidate branch":
						bad.Candidate.Branch = ""
					case "candidate SHA":
						if strings.HasPrefix(kind, "legacy") {
							bad.Candidate.SHA = strings.Repeat("d", 40)
						} else {
							bad.Candidate.SHA = ""
						}
					case "created":
						bad.CreatedAt = time.Time{}
					case "updated":
						bad.UpdatedAt = time.Time{}
					case "lane":
						bad.Lane = "wrong"
					case "operation":
						bad.ID = "wrong"
						bad.Candidate.Task = bad.ID
					case "landing":
						bad.LandingSHA = strings.Repeat("e", 40)
					case "published":
						bad.PublishedCandidateSHA = strings.Repeat("f", 40)
						bad.PullRequest = "https://example.test/pull/shape"
					}
					// Validation-failure shape alone does not prohibit publication; that
					// policy belongs to the owner/native proof and is deliberately distinct.
					permitsPublication := field == "published" && (kind == "validation" || kind == "legacy validation")
					err := check(bad, r.ReceiptPath)
					if permitsPublication {
						if err != nil {
							t.Fatalf("modern validation shape added publication policy: %v", err)
						}
					} else if err == nil {
						t.Fatalf("%s accepted malformed %s syntax/identity", kind, field)
					}
				})
			}
			for _, field := range []string{"task", "worktree", "branch", "SHA"} {
				t.Run("incomplete source "+field, func(t *testing.T) {
					t.Parallel()
					bad := r
					bad.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
					switch field {
					case "task":
						bad.Sources[0].Task = ""
					case "worktree":
						bad.Sources[0].Worktree = ""
					case "branch":
						bad.Sources[0].Branch = ""
					case "SHA":
						bad.Sources[0].SHA = ""
					}
					bad.ID = worktreeMergeOperationID(bad.Lane, bad.Sources)
					bad.Candidate.Task = bad.ID
					bad.ReceiptPath = filepath.Join(filepath.Dir(r.ReceiptPath), bad.ID+".json")
					if err := check(bad, bad.ReceiptPath); err == nil {
						t.Fatalf("%s accepted incomplete source %s", kind, field)
					}
				})
			}
			if kind == "legacy validation" {
				for _, field := range []string{"repository", "path", "revision"} {
					t.Run("validation "+field, func(t *testing.T) {
						t.Parallel()
						bad := r
						switch field {
						case "repository":
							bad.Validation.Repository = "other/repo"
						case "path":
							bad.Validation.Path = "other/path"
						case "revision":
							bad.Validation.Revision = ""
						}
						if err := check(bad, r.ReceiptPath); err == nil {
							t.Fatalf("legacy validation accepted mismatched %s", field)
						}
					})
				}
			}
		})
	}
}

func TestReceiptCollisionShapeRequiresEveryExplicitIdentity(t *testing.T) {
	t.Parallel()
	r := protocolShapeReceipt(t)
	r.Status = WorktreeMergePreparing
	r.SourceRefreshes = []WorktreeMergeSourceRefresh{{Sources: append([]WorktreeMergeSource(nil), r.Sources...)}}
	o := WorktreeMergeReceiptCollisionAcknowledgementOptions{ExpectedReceiptSHA256: "receipt-hash", ExpectedImmutableClaimSHA256: "claim-hash", ExpectedTargetSHA: r.TargetSHA, ExpectedCandidateSHA: r.Candidate.SHA, ExpectedCurrentSourceSHA: r.Sources[0].SHA, ExpectedHistoricalRefreshSourceSHA: r.SourceRefreshes[0].Sources[0].SHA}
	if err := requireReceiptCollisionExpectations(o); err != nil {
		t.Fatal(err)
	}
	if err := validateReceiptCollisionShape(r, r.ReceiptPath, o); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"receipt hash", "claim hash", "target", "candidate", "current source", "historical source"} {
		t.Run("required "+field, func(t *testing.T) {
			t.Parallel()
			bad := o
			switch field {
			case "receipt hash":
				bad.ExpectedReceiptSHA256 = ""
			case "claim hash":
				bad.ExpectedImmutableClaimSHA256 = ""
			case "target":
				bad.ExpectedTargetSHA = ""
			case "candidate":
				bad.ExpectedCandidateSHA = ""
			case "current source":
				bad.ExpectedCurrentSourceSHA = ""
			case "historical source":
				bad.ExpectedHistoricalRefreshSourceSHA = ""
			}
			if err := requireReceiptCollisionExpectations(bad); err == nil {
				t.Fatalf("missing %s was accepted", field)
			}
		})
	}
	for _, field := range []string{"status", "phase", "path", "ID", "lane", "landing", "pull request", "published SHA", "candidate", "source count", "source SHA", "history count", "history source count", "history SHA"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			bad := r
			bad.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			bad.SourceRefreshes = append([]WorktreeMergeSourceRefresh(nil), r.SourceRefreshes...)
			bad.SourceRefreshes[0].Sources = append([]WorktreeMergeSource(nil), r.SourceRefreshes[0].Sources...)
			switch field {
			case "status":
				bad.Status = WorktreeMergeConflict
			case "phase":
				bad.Phase = WorktreeMergePhaseLand
			case "path":
				bad.ReceiptPath = "wrong"
			case "ID":
				bad.ID = ""
			case "lane":
				bad.Lane = "wrong"
			case "landing":
				bad.LandingSHA = "landed"
			case "pull request":
				bad.PullRequest = "7"
			case "published SHA":
				bad.PublishedCandidateSHA = "published"
			case "candidate":
				bad.Candidate.SHA = "other"
			case "source count":
				bad.Sources = nil
			case "source SHA":
				bad.Sources[0].SHA = "other"
			case "history count":
				bad.SourceRefreshes = nil
			case "history source count":
				bad.SourceRefreshes[0].Sources = nil
			case "history SHA":
				bad.SourceRefreshes[0].Sources[0].SHA = "other"
			}
			if err := validateReceiptCollisionShape(bad, r.ReceiptPath, o); err == nil {
				t.Fatalf("collision accepted mismatched %s", field)
			}
		})
	}
}
