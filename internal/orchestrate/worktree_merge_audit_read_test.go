package orchestrate

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These records exercise static audit schemas and real filesystem bytes.
// Native Git, claims and ancestry remain the separate original witnesses.
func remainingAuditReadCase(t *testing.T, kind string) acknowledgementReadCase {
	t.Helper()
	r := acknowledgementReadReceipt(t)
	if kind == "retired" {
		r.PullRequest = "7"
		r.PublishedCandidateSHA = r.Candidate.SHA
	}
	if kind == "stranded" {
		r.Phase = WorktreeMergePhaseLand
		r.Status = WorktreeMergeChecksFailed
		r.PullRequest = "7"
		r.PublishedCandidateSHA = r.Candidate.SHA
	}
	if kind == "unpublished" {
		r.Status = WorktreeMergeValidationFailed
	}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "retired":
		path := retiredPublicationAcknowledgementPath(r.ReceiptPath)
		doc := WorktreeMergeRetiredPublicationAcknowledgement{SchemaVersion: worktreeMergeRetiredPublicationAcknowledgementSchemaVersion, Status: "retired_publication_acknowledged", AcknowledgementPath: path, ReceiptPath: r.ReceiptPath, ReceiptID: r.ID, ReceiptSHA256: digest, ReceiptPhase: r.Phase, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: "current-target", CandidateTask: r.Candidate.Task, CandidateWorktree: r.Candidate.Worktree, CandidateBranch: r.Candidate.Branch, CandidateSHA: r.Candidate.SHA, PublishedCandidateSHA: r.PublishedCandidateSHA, PullRequest: r.PullRequest, PullRequestState: "CLOSED", PullRequestHeadSHA: r.Candidate.SHA, Sources: r.Sources, Actor: "fixture", Reason: "static evidence", RecordedAt: r.CreatedAt}
		return bindAcknowledgementReadCase(t, kind, "retired-publication acknowledgement", path, r, doc, false, retiredPublicationAcknowledgementID, func() (WorktreeMergeRetiredPublicationAcknowledgement, error) {
			return readRetiredPublicationAcknowledgement(path, r)
		})
	case "stranded":
		path := strandedLandingAcknowledgementPath(r.ReceiptPath)
		doc := WorktreeMergeStrandedLandingAcknowledgement{SchemaVersion: worktreeMergeStrandedLandingAcknowledgementSchemaVersion, Status: "stranded_landing_acknowledged", AcknowledgementPath: path, ReceiptPath: r.ReceiptPath, ReceiptID: r.ID, ReceiptSHA256: digest, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: "current-target", CandidateSHA: r.Candidate.SHA, PullRequest: r.PullRequest, ProvedLandingSHA: "landing-sha", CandidateLanding: "ancestor", Sources: r.Sources, Actor: "fixture", Reason: "static evidence", RecordedAt: r.CreatedAt}
		return bindAcknowledgementReadCase(t, kind, "stranded-landing acknowledgement", path, r, doc, false, strandedLandingAcknowledgementID, func() (WorktreeMergeStrandedLandingAcknowledgement, error) {
			return readStrandedLandingAcknowledgement(path, r)
		})
	case "unpublished":
		path := unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)
		doc := WorktreeMergeUnpublishedValidationFailureAcknowledgement{SchemaVersion: worktreeMergeUnpublishedValidationFailureAcknowledgementSchemaVersion, Status: "unpublished_validation_failure_acknowledged", AcknowledgementPath: path, ReceiptPath: r.ReceiptPath, ReceiptID: r.ID, ReceiptSHA256: digest, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: "current-target", Candidate: r.Candidate, Sources: r.Sources, PreservedSources: r.Sources, Actor: "fixture", Reason: "static evidence", RecordedAt: r.CreatedAt}
		return bindAcknowledgementReadCase(t, kind, "unpublished-validation-failure acknowledgement", path, r, doc, false, unpublishedValidationFailureAcknowledgementID, func() (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
			return readUnpublishedValidationFailureAcknowledgement(path, r)
		})
	case "adoption":
		path := publishedCandidateAdoptionPath(r.ReceiptPath)
		doc := WorktreeMergePublishedCandidateAdoption{SchemaVersion: 1, Status: "published_candidate_adopted", AcknowledgementPath: path, ReceiptPath: r.ReceiptPath, ReceiptID: r.ID, ReceiptSHA256: digest, Lane: r.Lane, Repository: r.Repository, Target: r.Target, Candidate: r.Candidate, PullRequest: "7", Actor: "fixture", Reason: "static evidence", RecordedAt: r.CreatedAt}
		return bindAcknowledgementReadCase(t, kind, "", path, r, doc, true, publishedCandidateAdoptionID, func() (WorktreeMergePublishedCandidateAdoption, error) {
			return readPublishedCandidateAdoption(path, r)
		})
	default:
		t.Fatalf("unknown audit kind %q", kind)
		return acknowledgementReadCase{}
	}
}

func TestRemainingAuditReadersPreserveFilesystemDecodeAndResultCustody(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"retired", "stranded", "unpublished", "adoption"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			tc := remainingAuditReadCase(t, kind)
			value, err := tc.read()
			if !errors.Is(err, os.ErrNotExist) || !reflect.ValueOf(value).IsZero() {
				t.Fatalf("absent sidecar result=%#v error=%v", value, err)
			}
			if err := os.WriteFile(tc.path, []byte(`{"id":"partial-id","schema_version":"wrong-type"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err = tc.read()
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Fatalf("actual decode type error=%v", err)
			}
			if kind == "adoption" {
				if err != typeErr || reflect.ValueOf(value).FieldByName("ID").String() != "partial-id" {
					t.Fatalf("raw partial contract value=%#v error=%v", value, err)
				}
			} else if !reflect.ValueOf(value).IsZero() || !strings.HasPrefix(err.Error(), "decode "+tc.description+" "+tc.path+": ") {
				t.Fatalf("wrapped zero contract value=%#v error=%v", value, err)
			}
			tc.write()
			value, err = tc.read()
			if err != nil || reflect.ValueOf(value).FieldByName("ID").String() != tc.id() {
				t.Fatalf("valid bytes value=%#v error=%v", value, err)
			}
			tc.change("SchemaVersion", 999)
			tc.write()
			value, err = tc.read()
			if err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
				t.Fatalf("self-consistent invalid schema value=%#v error=%v", value, err)
			}
			if tc.partial != !reflect.ValueOf(value).IsZero() {
				t.Fatalf("identity refusal custody=%#v", value)
			}
			// Restore a valid schema before removing its actual receipt bytes.
			tc.change("SchemaVersion", 1)
			tc.write()
			if err := os.Remove(tc.receipt.ReceiptPath); err != nil {
				t.Fatal(err)
			}
			value, err = tc.read()
			if !errors.Is(err, os.ErrNotExist) || tc.partial != !reflect.ValueOf(value).IsZero() {
				t.Fatalf("receipt digest result=%#v error=%v", value, err)
			}
		})
	}
}

func TestAuditReadbackEnforcesRecordedEvidenceExceptions(t *testing.T) {
	t.Parallel()
	t.Run("stranded ancestor and tree evidence", func(t *testing.T) {
		t.Parallel()
		tc := remainingAuditReadCase(t, "stranded")
		tc.write()
		for _, row := range []struct {
			landing, tree, head string
			valid               bool
		}{{"ancestor", "", "", true}, {"tree-identical", "tree-sha", "descendant-sha", true}, {"tree-identical", "", "", false}, {"ancestor", "tree-sha", "", false}, {"unknown", "", "", false}, {"ancestor", "", tc.receipt.Candidate.SHA, false}} {
			tc.change("CandidateLanding", row.landing)
			tc.change("CandidateLandingTreeSHA", row.tree)
			tc.change("PullRequestHeadSHA", row.head)
			tc.write()
			_, err := tc.read()
			if (err == nil) != row.valid {
				t.Fatalf("landing=%q tree=%q head=%q error=%v", row.landing, row.tree, row.head, err)
			}
		}
	})
	t.Run("unpublished cleanup and preserved sources", func(t *testing.T) {
		t.Parallel()
		tc := remainingAuditReadCase(t, "unpublished")
		tc.write()
		var ack WorktreeMergeUnpublishedValidationFailureAcknowledgement
		contents, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(contents, &ack); err != nil {
			t.Fatal(err)
		}
		preparing := tc.receipt
		preparing.Status = WorktreeMergePreparing
		if _, err := readUnpublishedValidationFailureAcknowledgement(tc.path, preparing); err == nil || !strings.Contains(err.Error(), "lacks discarded candidate cleanup evidence") {
			t.Fatalf("cleanup evidence refusal=%v", err)
		}
		ack.CandidateCleanupBacklog = "private-cleanup-proof"
		ack.ID = unpublishedValidationFailureAcknowledgementID(ack)
		writeAcknowledgementReadJSON(t, tc.path, ack)
		if _, err := readUnpublishedValidationFailureAcknowledgement(tc.path, preparing); err != nil {
			t.Fatal(err)
		}
		ack.PreservedSources = nil
		ack.ID = unpublishedValidationFailureAcknowledgementID(ack)
		writeAcknowledgementReadJSON(t, tc.path, ack)
		if _, err := readUnpublishedValidationFailureAcknowledgement(tc.path, preparing); err == nil || !strings.Contains(err.Error(), "lacks preserved descendant source evidence") {
			t.Fatalf("preserved-source refusal=%v", err)
		}
	})
	t.Run("adoption recorded descendant identity", func(t *testing.T) {
		t.Parallel()
		tc := remainingAuditReadCase(t, "adoption")
		tc.write()
		descendant := tc.receipt
		descendant.PullRequest = "7"
		descendant.Candidate.SHA = "descendant-sha"
		descendant.PublishedCandidateSHA = tc.receipt.Candidate.SHA
		// Real changed receipt bytes make the digest differ from the sidecar.
		if err := persistWorktreeMergeReceipt(descendant); err != nil {
			t.Fatal(err)
		}
		for _, published := range []string{tc.receipt.Candidate.SHA, descendant.Candidate.SHA} {
			descendant.PublishedCandidateSHA = published
			if _, err := readPublishedCandidateAdoption(tc.path, descendant); err != nil {
				t.Fatalf("recorded transition published=%q error=%v", published, err)
			}
		}
		for _, mutate := range []func(*WorktreeMergeReceipt){func(r *WorktreeMergeReceipt) { r.PullRequest = "different" }, func(r *WorktreeMergeReceipt) { r.Candidate.Task = "different" }, func(r *WorktreeMergeReceipt) { r.Candidate.Worktree += "-different" }, func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "different" }, func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "unrelated" }} {
			changed := descendant
			mutate(&changed)
			value, err := readPublishedCandidateAdoption(tc.path, changed)
			if err == nil || value.ID == "" {
				t.Fatalf("transition mismatch must refuse with partial record: value=%#v error=%v", value, err)
			}
		}
		sameHead := descendant
		sameHead.Candidate = tc.receipt.Candidate
		if _, err := readPublishedCandidateAdoption(tc.path, sameHead); err == nil {
			t.Fatal("same-head record accepted stale receipt digest")
		}
	})
}

func TestAuditReceiptEligibilityPreservesRefusalOrder(t *testing.T) {
	t.Parallel()
	type refusal struct {
		name, fragment string
		change         func(*WorktreeMergeReceipt)
	}
	retired := []refusal{
		{"identity", "inconsistent immutable receipt identity", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "wrong"; r.Phase = "wrong" }},
		{"phase", "want prepare or land", func(r *WorktreeMergeReceipt) { r.Phase = "wrong"; r.Status = "wrong" }},
		{"status", "want conflict, validation_failed, or checks_failed", func(r *WorktreeMergeReceipt) { r.Status = "wrong"; r.LandingSHA = "already-landed" }},
		{"landing", "already recorded a landing SHA", func(r *WorktreeMergeReceipt) { r.LandingSHA = "already-landed"; r.PullRequest = "" }},
		{"pull request", "no published pull request", func(r *WorktreeMergeReceipt) { r.PullRequest = ""; r.TargetSHA = "" }},
		{"target", "lacks complete immutable repository or target identity", func(r *WorktreeMergeReceipt) { r.TargetSHA = ""; r.Candidate.Task = "" }},
		{"candidate", "lacks complete immutable candidate identity", func(r *WorktreeMergeReceipt) {
			r.Candidate.Task = ""
			r.Candidate.SHA = ""
			r.PublishedCandidateSHA = ""
		}},
		{"candidate SHA", "no published or preserved candidate SHA", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = ""; r.PublishedCandidateSHA = ""; r.Sources = nil }},
		{"sources", "no receipted sources", func(r *WorktreeMergeReceipt) { r.Sources = nil }},
		{"source identity", "incomplete immutable source identity", func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = "" }},
	}
	stranded := []refusal{
		{"identity", "inconsistent immutable receipt identity", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "wrong"; r.Phase = "wrong" }},
		{"phase status", "want recoverable land receipt", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhasePrepare; r.LandingSHA = "already-landed" }},
		{"landing", "already recorded a landing SHA", func(r *WorktreeMergeReceipt) { r.LandingSHA = "already-landed"; r.PullRequest = "" }},
		{"pull request", "no published pull request", func(r *WorktreeMergeReceipt) { r.PullRequest = ""; r.TargetSHA = "" }},
		{"target candidate", "lacks complete immutable repository, target, or candidate identity", func(r *WorktreeMergeReceipt) { r.TargetSHA = ""; r.PublishedCandidateSHA = "wrong" }},
		{"publication", "does not match its exact preserved candidate", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "wrong" }},
	}
	for _, group := range []struct {
		kind     string
		validate func(WorktreeMergeReceipt, string) error
		rows     []refusal
	}{{"retired", validateRetiredPublicationReceipt, retired}, {"stranded", validateStrandedLandingReceipt, stranded}} {
		t.Run(group.kind, func(t *testing.T) {
			t.Parallel()
			base := remainingAuditReadCase(t, group.kind).receipt
			if err := group.validate(base, base.ReceiptPath); err != nil {
				t.Fatalf("valid receipt refused: %v", err)
			}
			for _, row := range group.rows {
				r := base
				r.Sources = append([]WorktreeMergeSource(nil), base.Sources...)
				row.change(&r)
				err := group.validate(r, base.ReceiptPath)
				if err == nil || !strings.Contains(err.Error(), row.fragment) {
					t.Fatalf("%s error=%v want=%q", row.name, err, row.fragment)
				}
			}
		})
	}
	t.Run("stranded recoverable statuses", func(t *testing.T) {
		t.Parallel()
		r := remainingAuditReadCase(t, "stranded").receipt
		for _, status := range []WorktreeMergeStatus{WorktreeMergeConflict, WorktreeMergePublished, WorktreeMergeChecksPending, WorktreeMergeChecksFailed} {
			r.Status = status
			if !recoverableStrandedLandingStatus(status) {
				t.Fatalf("recoverable status refused: %s", status)
			}
			if err := validateStrandedLandingReceipt(r, r.ReceiptPath); err != nil {
				t.Fatal(err)
			}
		}
		if recoverableStrandedLandingStatus(WorktreeMergeLanded) {
			t.Fatal("landed status considered stranded")
		}
	})
	t.Run("unpublished exact identity and sources", func(t *testing.T) {
		t.Parallel()
		r := remainingAuditReadCase(t, "unpublished").receipt
		if err := validateUnpublishedValidationFailureReceipt(r, r.ReceiptPath); err != nil {
			t.Fatal(err)
		}
		preparing := r
		preparing.Status = WorktreeMergePreparing
		if err := validateUnpublishedValidationFailureReceipt(preparing, preparing.ReceiptPath); err != nil {
			t.Fatal(err)
		}
		published := r
		published.PullRequest = "7"
		published.Sources = nil
		if err := validateUnpublishedValidationFailureReceipt(published, published.ReceiptPath); err == nil || !strings.Contains(err.Error(), "not an exact unpublished") {
			t.Fatalf("shape refusal=%v", err)
		}
		incomplete := r
		incomplete.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
		incomplete.Sources[0].SHA = ""
		// Keep the actual deterministic operation ID consistent, so the later
		// complete-source gate must still refuse this incomplete recorded source.
		incomplete.ID = worktreeMergeOperationID(incomplete.Lane, incomplete.Sources)
		incomplete.Candidate.Task = incomplete.ID
		if err := validateUnpublishedValidationFailureReceipt(incomplete, incomplete.ReceiptPath); err == nil || !strings.Contains(err.Error(), "incomplete immutable source identity") {
			t.Fatalf("source refusal=%v", err)
		}
	})
	t.Run("adoption exact unlanded shape", func(t *testing.T) {
		t.Parallel()
		r := remainingAuditReadCase(t, "adoption").receipt
		if err := validatePublishedCandidateAdoptionReceipt(r, r.ReceiptPath); err != nil {
			t.Fatal(err)
		}
		r.PullRequest = "7"
		if err := validatePublishedCandidateAdoptionReceipt(r, r.ReceiptPath); err == nil || err.Error() != "receipt is not an exact unlanded prepare/conflict candidate awaiting publication adoption" {
			t.Fatalf("adoption refusal=%v", err)
		}
	})
}

func TestAuditPresenceAndUnpublishedEligibilityUseActualSidecars(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"retired", "stranded", "unpublished"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			tc := remainingAuditReadCase(t, kind)
			has := map[string]func(WorktreeMergeReceipt) (bool, error){"retired": hasRetiredPublicationAcknowledgement, "stranded": hasStrandedLandingAcknowledgement, "unpublished": hasUnpublishedValidationFailureAcknowledgement}[kind]
			if found, err := has(tc.receipt); found || err != nil {
				t.Fatalf("absent sidecar found=%v error=%v", found, err)
			}
			if err := os.WriteFile(tc.path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			found, err := has(tc.receipt)
			var syntax *json.SyntaxError
			if found || !errors.As(err, &syntax) {
				t.Fatalf("malformed sidecar found=%v error=%v", found, err)
			}
			tc.write()
			if found, err := has(tc.receipt); !found || err != nil {
				t.Fatalf("valid sidecar found=%v error=%v", found, err)
			}
			if kind == "retired" {
				if unpublished, err := effectiveUnpublishedConflict(tc.receipt); !unpublished || err != nil {
					t.Fatalf("retired evidence unpublished=%v error=%v", unpublished, err)
				}
				if err := os.WriteFile(tc.path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
				if unpublished, err := effectiveUnpublishedConflict(tc.receipt); unpublished || err == nil {
					t.Fatalf("malformed evidence broadened eligibility: %v %v", unpublished, err)
				}
				empty := tc.receipt
				empty.PullRequest = ""
				empty.PublishedCandidateSHA = ""
				if unpublished, err := effectiveUnpublishedConflict(empty); !unpublished || err != nil {
					t.Fatalf("unpublished receipt unnecessarily read malformed sidecar: %v %v", unpublished, err)
				}
			}
		})
	}
}

func TestAuditEvidenceComparisonExcludesReplayMetadata(t *testing.T) {
	t.Parallel()
	t.Run("retired", func(t *testing.T) {
		t.Parallel()
		tc := remainingAuditReadCase(t, "retired")
		tc.write()
		left, err := readRetiredPublicationAcknowledgement(tc.path, tc.receipt)
		if err != nil {
			t.Fatal(err)
		}
		right := left
		right.ID = "new-id"
		right.Actor = "different operator"
		right.Reason = "new replay reason"
		right.RecordedAt = right.RecordedAt.Add(time.Minute)
		if !sameRetiredPublicationAcknowledgement(left, right) {
			t.Fatal("replay metadata changed immutable-evidence comparison")
		}
		right.CurrentTargetSHA = "changed"
		if sameRetiredPublicationAcknowledgement(left, right) {
			t.Fatal("changed immutable evidence considered equal")
		}
	})
	t.Run("unpublished", func(t *testing.T) {
		t.Parallel()
		tc := remainingAuditReadCase(t, "unpublished")
		tc.write()
		left, err := readUnpublishedValidationFailureAcknowledgement(tc.path, tc.receipt)
		if err != nil {
			t.Fatal(err)
		}
		right := left
		right.ID = "new-id"
		right.Actor = "different operator"
		right.Reason = "new replay reason"
		right.RecordedAt = right.RecordedAt.Add(time.Minute)
		if !sameUnpublishedValidationFailureAcknowledgement(left, right) {
			t.Fatal("replay metadata changed immutable-evidence comparison")
		}
		right.PreservedSources = append([]WorktreeMergeSource(nil), left.PreservedSources...)
		right.PreservedSources[0].SHA = "changed"
		if sameUnpublishedValidationFailureAcknowledgement(left, right) {
			t.Fatal("changed preserved source considered equal")
		}
	})
}
