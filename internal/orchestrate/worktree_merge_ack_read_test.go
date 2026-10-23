package orchestrate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type acknowledgementReadCase struct {
	name, path, description string
	partial                 bool
	receipt                 WorktreeMergeReceipt
	read                    func() (any, error)
	write                   func()
	change                  func(string, any)
	id                      func() string
}

func bindAcknowledgementReadCase[T mergeAcknowledgementDocument](t *testing.T, name, description, path string, receipt WorktreeMergeReceipt, document T, partial bool, identify func(T) string, read func() (T, error)) acknowledgementReadCase {
	t.Helper()
	documentValue := reflect.ValueOf(&document).Elem()
	documentValue.FieldByName("ID").SetString(identify(document))
	return acknowledgementReadCase{name: name, description: description, path: path, receipt: receipt, partial: partial,
		read:  func() (any, error) { return read() },
		write: func() { writeAcknowledgementReadJSON(t, path, document) },
		change: func(field string, value any) {
			documentValue.FieldByName(field).Set(reflect.ValueOf(value))
			documentValue.FieldByName("ID").SetString(identify(document))
		}, id: func() string { return documentValue.FieldByName("ID").String() }}
}

func writeAcknowledgementReadJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func acknowledgementReadReceipt(t *testing.T) WorktreeMergeReceipt {
	t.Helper()
	dir := t.TempDir()
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	sources := []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(dir, "source"), Branch: "feature/source", SHA: "source-sha"}}
	lane := worktreeMergeLaneID("acme/app", "main")
	id := worktreeMergeOperationID(lane, sources)
	return WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ID: id, Lane: lane,
		Repository: "acme/app", Target: "main", TargetSHA: "target-sha", Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict,
		ReceiptPath: filepath.Join(dir, id+".json"), Sources: sources, CreatedAt: at, UpdatedAt: at,
		Candidate: WorktreeMergeCandidate{Task: id, Worktree: filepath.Join(dir, "candidate"), Branch: "wb/recovery/candidate", SHA: "candidate-sha"}}
}

// These private records prove the reader's static byte/identity contracts, not
// Git ancestry, live claims or permission to land. The native originals retain
// those separate custody witnesses. Every mutation case gets a private root.
func makeAcknowledgementReadCase(t *testing.T, kind string) acknowledgementReadCase {
	t.Helper()
	receipt := acknowledgementReadReceipt(t)
	at := receipt.CreatedAt
	candidate := receipt.Candidate
	if kind == "collision" {
		receipt.Status = WorktreeMergePreparing
		receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: at, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...)}}
	}
	if kind == "rebatch" {
		receipt.Status = WorktreeMergePrepared
	}
	if kind == "landed" {
		receipt.Status = WorktreeMergeValidationFailed
	}
	if kind == "cleanup" {
		receipt.Status, receipt.Phase, receipt.LandingSHA = WorktreeMergeLanded, WorktreeMergePhaseLand, candidate.SHA
	}
	if kind == "legacy validation" || kind == "legacy conflict" {
		receipt.Candidate.SHA = ""
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	hash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "collision":
		path := receiptCollisionAcknowledgementPath(receipt.ReceiptPath)
		doc := WorktreeMergeReceiptCollisionAcknowledgement{SchemaVersion: worktreeMergeReceiptCollisionAcknowledgementSchemaVersion,
			Status: "receipt_collision_acknowledged", ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path,
			ReceiptSHA256: hash, ImmutableClaimSHA256: "static-claim-digest", ReceiptID: receipt.ID, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, Candidate: candidate, ClaimBaseSHA: receipt.TargetSHA,
			ExpectedTargetSHA: receipt.TargetSHA, ExpectedCandidateSHA: candidate.SHA, ExpectedCurrentSourceSHA: receipt.Sources[0].SHA,
			ExpectedHistoricalRefreshSourceSHA: receipt.SourceRefreshes[0].Sources[0].SHA,
			CurrentSources:                     receipt.Sources, HistoricalRefreshSources: receipt.SourceRefreshes[0].Sources,
			HistoricalValidationFailedOperatorAssertion: true, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "receipt-collision acknowledgement", path, receipt, doc, false, receiptCollisionAcknowledgementID,
			func() (WorktreeMergeReceiptCollisionAcknowledgement, error) {
				return readReceiptCollisionAcknowledgement(path, receipt)
			})
	case "rebatch":
		path := rebatchPath(receipt.ReceiptPath)
		replacement := acknowledgementReadReceipt(t)
		replacement.RebatchOf, replacement.Repository, replacement.Target, replacement.TargetSHA = receipt.ReceiptPath, receipt.Repository, receipt.Target, receipt.TargetSHA
		replacement.Candidate.Task, replacement.Candidate.SHA = "replacement", "replacement-sha"
		replacement.Sources = append(append([]WorktreeMergeSource(nil), receipt.Sources...), WorktreeMergeSource{Task: "extra", Worktree: filepath.Join(filepath.Dir(path), "extra"), Branch: "feature/extra", SHA: "extra-sha"})
		replacement.RebatchedCandidates = []WorktreeMergeCandidate{candidate}
		if err := persistWorktreeMergeReceipt(replacement); err != nil {
			t.Fatal(err)
		}
		doc := WorktreeMergePreparedRebatch{SchemaVersion: worktreeMergePreparedRebatchSchemaVersion, Status: "prepared_rebatched",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptID: receipt.ID, ReceiptSHA256: hash, ReceiptStatus: receipt.Status,
			Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			OriginalCandidate: candidate, OriginalSources: receipt.Sources, ReplacementReceiptPath: replacement.ReceiptPath,
			Replacement: replacement.Candidate, Sources: replacement.Sources, RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "prepared rebatch", path, receipt, doc, false, preparedRebatchID,
			func() (WorktreeMergePreparedRebatch, error) { return readPreparedWorktreeMergeRebatch(path, receipt) })
	case "landed":
		path := landedFailureAcknowledgementPath(receipt.ReceiptPath)
		doc := WorktreeMergeLandedFailureAcknowledgement{SchemaVersion: worktreeMergeLandedFailureAcknowledgementSchemaVersion, Status: "landed_failure_acknowledged",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptID: receipt.ID, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, ReceiptLandingSHA: receipt.LandingSHA,
			CurrentTargetSHA: receipt.TargetSHA, CandidateSHA: candidate.SHA, ClaimBaseSHA: receipt.TargetSHA, Sources: receipt.Sources, RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "landed-failure acknowledgement", path, receipt, doc, false, landedFailureAcknowledgementID,
			func() (WorktreeMergeLandedFailureAcknowledgement, error) {
				return readLandedFailureAcknowledgement(path, receipt)
			})
	case "advance":
		path := conflictCandidateAdvancePath(receipt.ReceiptPath)
		doc := WorktreeMergeConflictCandidateAdvance{SchemaVersion: worktreeMergeConflictCandidateAdvanceSchemaVersion, Status: "conflict_candidate_advanced",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptSHA256: hash, ReceiptID: receipt.ID, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			OriginalCandidate: candidate, AdvancedCandidateSHA: "advanced-sha", ClaimBaseSHA: receipt.TargetSHA, Sources: receipt.Sources, RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "conflict-candidate advance", path, receipt, doc, true, conflictCandidateAdvanceID,
			func() (WorktreeMergeConflictCandidateAdvance, error) { return readConflictCandidateAdvance(path) })
	case "legacy validation":
		path := legacyValidationFailureIdentityPath(receipt.ReceiptPath)
		doc := WorktreeMergeLegacyValidationFailureIdentity{SchemaVersion: worktreeMergeLegacyValidationFailureIdentitySchemaVersion, Status: "legacy_validation_failed_identity_correlated",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptSHA256: hash, ReceiptID: receipt.ID, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			Candidate: candidate, ClaimBaseSHA: receipt.TargetSHA, Sources: receipt.Sources, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "legacy validation-failed identity", path, receipt, doc, false, legacyValidationFailureIdentityID,
			func() (WorktreeMergeLegacyValidationFailureIdentity, error) {
				return readLegacyValidationFailureIdentity(path, receipt, candidate)
			})
	case "legacy conflict":
		path := legacyConflictIdentityPath(receipt.ReceiptPath)
		doc := WorktreeMergeLegacyConflictIdentity{SchemaVersion: worktreeMergeLegacyConflictIdentitySchemaVersion, Status: "legacy_conflict_identity_correlated",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptSHA256: hash, ReceiptID: receipt.ID, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			Candidate: candidate, ClaimBaseSHA: receipt.TargetSHA, Sources: receipt.Sources, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "legacy conflict identity", path, receipt, doc, false, legacyConflictIdentityID,
			func() (WorktreeMergeLegacyConflictIdentity, error) {
				return readLegacyConflictIdentity(path, receipt, candidate)
			})
	case "cleanup":
		path := receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix
		assets, err := terminalWorkLogExpectations(receipt)
		if err != nil {
			t.Fatal(err)
		}
		doc := WorktreeMergeMissingCleanupAcknowledgement{SchemaVersion: worktreeMergeMissingCleanupAcknowledgementSchemaVersion, Status: "missing_cleanup_acknowledged",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptSHA256: hash, ReceiptID: receipt.ID, Lane: receipt.Lane,
			Repository: receipt.Repository, Target: receipt.Target, LandingSHA: receipt.LandingSHA, CurrentTargetSHA: receipt.TargetSHA,
			Assets: assets, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "missing-cleanup acknowledgement", path, receipt, doc, true, missingCleanupAcknowledgementID,
			func() (WorktreeMergeMissingCleanupAcknowledgement, error) {
				return readMissingCleanupAcknowledgement(path, receipt)
			})
	case "supersession":
		path := validationFailureSupersessionPath(receipt.ReceiptPath)
		doc := WorktreeMergeValidationFailureSupersession{SchemaVersion: worktreeMergeValidationFailureSupersessionSchemaVersion, Status: "validation_failure_superseded",
			ReceiptPath: receipt.ReceiptPath, AcknowledgementPath: path, ReceiptID: receipt.ID, ReceiptSHA256: hash, ReceiptStatus: receipt.Status,
			Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			OriginalCandidate: candidate, OriginalClaimBaseSHA: receipt.TargetSHA, ReplacementClaimBaseSHA: receipt.TargetSHA,
			Replacement: WorktreeMergeCandidate{Task: "replacement", Worktree: filepath.Join(filepath.Dir(path), "replacement"), Branch: "feature/replacement", SHA: "replacement-sha"},
			Sources:     receipt.Sources, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "validation-failed supersession", path, receipt, doc, false, validationFailureSupersessionID,
			func() (WorktreeMergeValidationFailureSupersession, error) {
				return readValidationFailureSupersession(path, receipt)
			})
	case "correction":
		path := selfSupersessionCorrectionPath(receipt.ReceiptPath)
		supersession := WorktreeMergeValidationFailureSupersession{ID: "historical-self-supersession", AcknowledgementPath: validationFailureSupersessionPath(receipt.ReceiptPath),
			OriginalCandidate: candidate, Replacement: candidate, OriginalClaimBaseSHA: receipt.TargetSHA, ReplacementClaimBaseSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA}
		if err := persistValidationFailureSupersession(supersession.AcknowledgementPath, supersession); err != nil {
			t.Fatal(err)
		}
		supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		doc := WorktreeMergeSelfSupersessionCorrection{SchemaVersion: worktreeMergeSelfSupersessionCorrectionSchemaVersion, Status: "validation_failure_self_supersession_corrected",
			CorrectionPath: path, ReceiptPath: receipt.ReceiptPath, ReceiptSHA256: hash, SupersessionPath: supersession.AcknowledgementPath,
			SupersessionSHA256: supersessionHash, SupersessionID: supersession.ID, ImmutableClaimSHA256: "static-claim-digest",
			OriginalCandidate: candidate, OriginalClaimBaseSHA: receipt.TargetSHA, ReplacementClaimBaseSHA: receipt.TargetSHA, CurrentTargetSHA: receipt.TargetSHA,
			CorrectedReplacement: WorktreeMergeCandidate{Task: "replacement", Worktree: filepath.Join(filepath.Dir(path), "replacement"), Branch: "feature/replacement", SHA: "replacement-sha"},
			Sources:              receipt.Sources, Actor: "reader-fixture", Reason: "static read contract", RecordedAt: at}
		return bindAcknowledgementReadCase(t, kind, "self-supersession correction", path, receipt, doc, false, selfSupersessionCorrectionID,
			func() (WorktreeMergeSelfSupersessionCorrection, error) {
				return readSelfSupersessionCorrection(path, receipt, supersession)
			})
	default:
		t.Fatalf("unknown reader kind %q", kind)
		return acknowledgementReadCase{}
	}
}

func acknowledgementReaderKinds() []string {
	return []string{"collision", "rebatch", "landed", "advance", "legacy validation", "legacy conflict", "cleanup", "supersession", "correction"}
}

func TestAcknowledgementReadersPreserveNativeReadAndDecodeErrors(t *testing.T) {
	t.Parallel()
	for _, kind := range acknowledgementReaderKinds() {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			tc := makeAcknowledgementReadCase(t, kind)
			value, err := tc.read()
			if !errors.Is(err, os.ErrNotExist) || !reflect.ValueOf(value).IsZero() {
				t.Fatalf("missing file result=%#v error=%v", value, err)
			}
			if err := os.WriteFile(tc.path, []byte(`{"id":"decoded-before-type-error","schema_version":"wrong-type"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			value, err = tc.read()
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) || !strings.HasPrefix(err.Error(), "decode "+tc.description+" "+tc.path+": ") {
				t.Fatalf("decode error=%v, want exact context and actual type error", err)
			}
			gotID := reflect.ValueOf(value).FieldByName("ID").String()
			if tc.partial {
				if gotID != "decoded-before-type-error" {
					t.Fatalf("partial record ID=%q", gotID)
				}
			} else if !reflect.ValueOf(value).IsZero() {
				t.Fatalf("decode failure exposed partial record: %#v", value)
			}
		})
	}
}

func TestAcknowledgementReadersBindActualReceiptAndSidecarBytes(t *testing.T) {
	t.Parallel()
	for _, kind := range acknowledgementReaderKinds() {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			tc := makeAcknowledgementReadCase(t, kind)
			tc.write()
			value, err := tc.read()
			if err != nil || reflect.ValueOf(value).FieldByName("ID").String() != tc.id() {
				t.Fatalf("valid static read result=%#v error=%v", value, err)
			}
			// Recompute the real ID after changing schema; the schema guard
			// must refuse even a self-consistent document digest.
			tc.change("SchemaVersion", 999)
			tc.write()
			value, err = tc.read()
			if err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("changed schema accepted: result=%#v error=%v", value, err)
			}
			if tc.partial != !reflect.ValueOf(value).IsZero() {
				t.Fatalf("validation failure result custody: %#v", value)
			}
		})
	}
}

func TestAcknowledgementReadersReturnActualDigestAndReplacementRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"collision", "rebatch", "legacy validation", "legacy conflict", "cleanup", "supersession", "correction"} {
		t.Run(kind+" receipt digest", func(t *testing.T) {
			t.Parallel()
			tc := makeAcknowledgementReadCase(t, kind)
			tc.write()
			if err := os.Remove(tc.receipt.ReceiptPath); err != nil {
				t.Fatal(err)
			}
			value, err := tc.read()
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed receipt error=%v", err)
			}
			if tc.partial != !reflect.ValueOf(value).IsZero() {
				t.Fatalf("digest failure result custody: %#v", value)
			}
		})
	}
	for _, stage := range []string{"replacement missing", "replacement malformed", "collision malformed"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			tc := makeAcknowledgementReadCase(t, "rebatch")
			tc.write()
			var doc WorktreeMergePreparedRebatch
			contents, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(contents, &doc); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "replacement missing":
				if err = os.Remove(doc.ReplacementReceiptPath); err != nil {
					t.Fatal(err)
				}
			case "replacement malformed":
				if err = os.WriteFile(doc.ReplacementReceiptPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "collision malformed":
				if err = os.WriteFile(receiptCollisionAcknowledgementPath(tc.receipt.ReceiptPath), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			value, err := tc.read()
			if !reflect.ValueOf(value).IsZero() {
				t.Fatalf("replacement refusal exposed record: %#v", value)
			}
			if stage == "replacement missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replacement error=%v", err)
				}
			} else {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatalf("actual JSON error=%v", err)
				}
				expected := "decode merge receipt " + doc.ReplacementReceiptPath + ": "
				if stage == "collision malformed" {
					expected = "decode receipt-collision acknowledgement " + receiptCollisionAcknowledgementPath(tc.receipt.ReceiptPath) + ": "
				}
				if !strings.HasPrefix(err.Error(), expected) {
					t.Fatalf("decode context=%v", err)
				}
			}
		})
	}
	t.Run("correction supersession digest", func(t *testing.T) {
		t.Parallel()
		tc := makeAcknowledgementReadCase(t, "correction")
		tc.write()
		if err := os.Remove(validationFailureSupersessionPath(tc.receipt.ReceiptPath)); err != nil {
			t.Fatal(err)
		}
		value, err := tc.read()
		if !errors.Is(err, os.ErrNotExist) || !reflect.ValueOf(value).IsZero() {
			t.Fatalf("missing supersession result=%#v error=%v", value, err)
		}
	})
	t.Run("cleanup terminal assets", func(t *testing.T) {
		t.Parallel()
		tc := makeAcknowledgementReadCase(t, "cleanup")
		tc.write()
		receipt := tc.receipt
		receipt.Candidate.SHA = ""
		_, expected := terminalWorkLogExpectations(receipt)
		value, err := readMissingCleanupAcknowledgement(tc.path, receipt)
		if expected == nil || err == nil || err.Error() != expected.Error() || value.ID != tc.id() {
			t.Fatalf("terminal refusal result=%#v error=%v expected=%v", value, err, expected)
		}
	})
}

func TestSupersessionReadPreservesReceiptValidationBeforeDecode(t *testing.T) {
	t.Parallel()
	tc := makeAcknowledgementReadCase(t, "supersession")
	if err := os.WriteFile(tc.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := tc.receipt
	invalid.SchemaVersion = 999
	expected := validatePrepareFailureSupersessionReceipt(invalid, invalid.ReceiptPath)
	value, err := readValidationFailureSupersession(tc.path, invalid)
	if expected == nil || err == nil || err.Error() != expected.Error() || !reflect.ValueOf(value).IsZero() {
		t.Fatalf("validation precedence value=%#v error=%v expected=%v", value, err, expected)
	}
	value, err = readValidationFailureSupersession(tc.path, tc.receipt)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || !reflect.ValueOf(value).IsZero() || !strings.HasPrefix(err.Error(), "decode validation-failed supersession "+tc.path+": ") {
		t.Fatalf("valid receipt decode value=%#v error=%v", value, err)
	}
}

func TestLegacySupersessionReadPreservesMissingSidecarSentinelPolicy(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"legacy conflict", "legacy validation"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			receipt := acknowledgementReadReceipt(t)
			candidate := receipt.Candidate
			receipt.Candidate.SHA = ""
			identityPath := legacyConflictIdentityPath(receipt.ReceiptPath)
			description := "legacy conflict"
			if kind == "legacy validation" {
				receipt.Status = WorktreeMergeValidationFailed
				receipt.Validation.Repository = receipt.Repository
				receipt.Validation.Path = candidate.Worktree
				receipt.Validation.Revision = candidate.SHA
				identityPath = legacyValidationFailureIdentityPath(receipt.ReceiptPath)
				description = "legacy validation-failed"
			}
			path := validationFailureSupersessionPath(receipt.ReceiptPath)
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			ack, effective, err := readValidationFailureSupersessionWithLegacyIdentity(path, receipt)
			if err == nil || errors.Is(err, os.ErrNotExist) || !strings.HasPrefix(err.Error(), "read "+description+" identity "+identityPath+": ") || !reflect.ValueOf(ack).IsZero() || !reflect.ValueOf(effective).IsZero() {
				t.Fatalf("missing identity ack=%#v effective=%#v error=%v", ack, effective, err)
			}
			if err := os.WriteFile(identityPath, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err = readValidationFailureSupersessionWithLegacyIdentity(path, receipt)
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "decode "+description+" identity "+identityPath+": ") {
				t.Fatalf("identity decode error=%v", err)
			}
			candidate.Task = "wrong-candidate"
			writeAcknowledgementReadJSON(t, identityPath, struct {
				Candidate WorktreeMergeCandidate `json:"candidate"`
			}{candidate})
			_, _, err = readValidationFailureSupersessionWithLegacyIdentity(path, receipt)
			if err == nil || !strings.Contains(err.Error(), "mismatched candidate identity") {
				t.Fatalf("candidate refusal=%v", err)
			}
		})
	}
}
