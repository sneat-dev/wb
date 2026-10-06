package orchestrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSupersededPullRequestSelectorRefusalsHaveNoRemoteEffects(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ selector, want string }{
		{"not-a-pr", "resolve superseded pull request number"},
		{strings.Repeat("9", 100), "parse superseded pull request number"},
	} {
		t.Run(row.want, func(t *testing.T) {
			t.Parallel()
			slept := false
			err := closeSupersededWorktreeMergePullRequest(t.Context(), "acme/app", row.selector, func(time.Duration) { slept = true })
			if err == nil || !strings.Contains(err.Error(), row.want) || slept {
				t.Fatalf("selector refusal=%v slept=%t", err, slept)
			}
		})
	}
}

func TestLandedFailureReceiptAcceptsOnlyExactTerminalFailureShapes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"prepare", "prepare phase", "prepare landed", "post target", "post phase", "post landing", "post status", "post head", "landed", "landed validation", "other status", "receipt identity"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := acknowledgementReadReceipt(t)
			r.Status = WorktreeMergeValidationFailed
			switch mode {
			case "prepare phase":
				r.Phase = WorktreeMergePhaseLand
			case "prepare landed":
				r.LandingSHA = r.Candidate.SHA
			case "post target", "post phase", "post landing", "post status", "post head":
				r.Status, r.Phase, r.LandingSHA = WorktreeMergePostTargetCIFailed, WorktreeMergePhaseLand, r.Candidate.SHA
				r.Checks.Status, r.Checks.Head = githubchecks.PullRequestWaitFailed, r.LandingSHA
				switch mode {
				case "post phase":
					r.Phase = WorktreeMergePhasePrepare
				case "post landing":
					r.LandingSHA = ""
				case "post status":
					r.Checks.Status = ""
				case "post head":
					r.Checks.Head = "different"
				}
			case "landed", "landed validation":
				r.Status, r.Phase, r.LandingSHA = WorktreeMergeLanded, WorktreeMergePhaseLand, r.Candidate.SHA
				r.Validation.Status = quality.StatusFailed
				if mode == "landed validation" {
					r.Validation.Status = quality.StatusPassed
				}
			case "other status":
				r.Status = WorktreeMergePrepared
			case "receipt identity":
				r.Lane = "other"
			}
			before := r
			err := validateLandedFailureAcknowledgementReceipt(r, r.ReceiptPath)
			valid := mode == "prepare" || mode == "post target" || mode == "landed"
			if (err == nil) != valid || !reflect.DeepEqual(before, r) {
				t.Fatalf("%s validation=%v receipt mutated=%t", mode, err, !reflect.DeepEqual(before, r))
			}
		})
	}
}

func TestTerminalCleanupAssetsRequireOrderedExactIdentity(t *testing.T) {
	t.Parallel()
	tc := makeAcknowledgementReadCase(t, "cleanup")
	assets, err := terminalWorkLogExpectations(tc.receipt)
	if err != nil || len(assets) < 2 {
		t.Fatalf("asset fixture=%+v %v", assets, err)
	}
	for _, mode := range []string{"equal", "nil", "short", "changed", "reordered"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			left := append([]worktrees.TerminalWorkLogExpectation(nil), assets...)
			right := append([]worktrees.TerminalWorkLogExpectation(nil), assets...)
			switch mode {
			case "nil":
				left = nil
				right = nil
			case "short":
				right = right[:len(right)-1]
			case "changed":
				right[0] = worktrees.TerminalWorkLogExpectation{}
			case "reordered":
				right[0], right[1] = right[1], right[0]
			}
			originalLeft := append([]worktrees.TerminalWorkLogExpectation(nil), left...)
			originalRight := append([]worktrees.TerminalWorkLogExpectation(nil), right...)
			if got := sameTerminalCleanupAssets(left, right); got != (mode == "equal" || mode == "nil") {
				t.Fatalf("ordered identity %s=%t", mode, got)
			}
			if !reflect.DeepEqual(left, originalLeft) || !reflect.DeepEqual(right, originalRight) {
				t.Fatal("comparison mutated assets")
			}
		})
	}
}

func TestRebatchSupersessionRequiresResolvedHomeAndExactDurableChain(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"home loop", "missing", "wrong original", "no close intent", "wrong pull request", "exact"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if mode == "home loop" {
				loop := filepath.Join(root, "loop")
				if err := os.Symlink("loop", loop); err != nil {
					t.Fatal(err)
				}
				got, err := worktreeMergeReplacementRecordsSupersession(loop, "acme/app", "main", nil, "original", "41")
				if got || err == nil {
					t.Fatalf("home loop supersession=%t %v", got, err)
				}
				return
			}
			r := acknowledgementReadReceipt(t)
			home, err := wbhome.Root(root)
			if err != nil {
				t.Fatal(err)
			}
			r.ReceiptPath = filepath.Join(home, "reports", "worktree-merge", worktreeMergeOperationID(r.Lane, r.Sources)+".json")
			original := filepath.Join(root, "original.json")
			r.RebatchOf, r.SupersededPullRequest = original, "41"
			switch mode {
			case "wrong original":
				r.RebatchOf = "other"
			case "no close intent":
				r.SupersededPullRequest = ""
			case "wrong pull request":
				r.SupersededPullRequest = "42"
			}
			if mode != "missing" {
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			got, err := worktreeMergeReplacementRecordsSupersession(root, r.Repository, r.Target, r.Sources, original, "41")
			if mode == "missing" {
				if got || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing chain=%t %v", got, err)
				}
				return
			}
			if err != nil || got != (mode == "exact") {
				t.Fatalf("%s chain=%t %v", mode, got, err)
			}
		})
	}
}

// This fixture asserts the reader's byte contracts only. Native custody is
// exercised separately by the existing protocol and failure-replay fixtures.
func TestLegacySupersessionFinalReadRejectsCorruptBytesAfterIdentityAuthentication(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validation", "conflict"} {
		for _, mode := range []string{"exact", "decode", "identity", "identity authentication"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				t.Parallel()
				r := acknowledgementReadReceipt(t)
				candidate := r.Candidate
				r.Candidate.SHA = ""
				if kind == "validation" {
					r.Status = WorktreeMergeValidationFailed
					r.Validation.Repository, r.Validation.Path, r.Validation.Revision = r.Repository, candidate.Worktree, candidate.SHA
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
				receiptBefore, err := os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				hash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				identityPath := legacyConflictIdentityPath(r.ReceiptPath)
				if kind == "validation" {
					identityPath = legacyValidationFailureIdentityPath(r.ReceiptPath)
					identity := WorktreeMergeLegacyValidationFailureIdentity{SchemaVersion: worktreeMergeLegacyValidationFailureIdentitySchemaVersion, Status: "legacy_validation_failed_identity_correlated", ReceiptPath: r.ReceiptPath, AcknowledgementPath: identityPath, ReceiptSHA256: hash, ReceiptID: r.ID, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: r.TargetSHA, Candidate: candidate, ClaimBaseSHA: r.TargetSHA, Sources: r.Sources, Actor: "private reader", Reason: "byte contract", RecordedAt: r.CreatedAt}
					identity.ID = legacyValidationFailureIdentityID(identity)
					if mode == "identity authentication" {
						identity.ID = "tampered"
					}
					writeAcknowledgementReadJSON(t, identityPath, identity)
				} else {
					identity := WorktreeMergeLegacyConflictIdentity{SchemaVersion: worktreeMergeLegacyConflictIdentitySchemaVersion, Status: "legacy_conflict_identity_correlated", ReceiptPath: r.ReceiptPath, AcknowledgementPath: identityPath, ReceiptSHA256: hash, ReceiptID: r.ID, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: r.TargetSHA, Candidate: candidate, ClaimBaseSHA: r.TargetSHA, Sources: r.Sources, Actor: "private reader", Reason: "byte contract", RecordedAt: r.CreatedAt}
					identity.ID = legacyConflictIdentityID(identity)
					if mode == "identity authentication" {
						identity.ID = "tampered"
					}
					writeAcknowledgementReadJSON(t, identityPath, identity)
				}
				path := validationFailureSupersessionPath(r.ReceiptPath)
				ack := WorktreeMergeValidationFailureSupersession{SchemaVersion: worktreeMergeValidationFailureSupersessionSchemaVersion, Status: "validation_failure_superseded", ReceiptPath: r.ReceiptPath, AcknowledgementPath: path, ReceiptID: r.ID, ReceiptSHA256: hash, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target, ReceiptTargetSHA: r.TargetSHA, CurrentTargetSHA: r.TargetSHA, OriginalCandidate: candidate, OriginalClaimBaseSHA: r.TargetSHA, ReplacementClaimBaseSHA: r.TargetSHA, Replacement: WorktreeMergeCandidate{Task: "replacement", Worktree: filepath.Join(filepath.Dir(path), "replacement"), Branch: "feature/replacement", SHA: "replacement-sha"}, Sources: r.Sources, Actor: "private reader", Reason: "byte contract", RecordedAt: r.CreatedAt}
				ack.ID = validationFailureSupersessionID(ack)
				if mode == "identity" {
					ack.ID = "tampered"
				}
				writeAcknowledgementReadJSON(t, path, ack)
				if mode == "decode" {
					if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				sidecarBefore, err := os.ReadFile(identityPath)
				if err != nil {
					t.Fatal(err)
				}
				ackBefore, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				got, effective, err := readValidationFailureSupersessionWithLegacyIdentity(path, r)
				if mode == "exact" {
					if err != nil || got.ID != ack.ID || effective.Candidate != candidate || r.Candidate.SHA != "" {
						t.Fatalf("effective legacy=%+v %+v %v", got, effective, err)
					}
				} else {
					if err == nil || !reflect.ValueOf(got).IsZero() || !reflect.ValueOf(effective).IsZero() {
						t.Fatalf("legacy refusal=%+v %+v %v", got, effective, err)
					}
					if mode == "decode" {
						var syntax *json.SyntaxError
						if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "decode validation-failed supersession "+path+": ") {
							t.Fatalf("final decode=%v", err)
						}
					}
					if mode == "identity authentication" && !strings.Contains(err.Error(), "invalid immutable evidence") {
						t.Fatalf("identity authentication=%v", err)
					}
				}
				for file, before := range map[string][]byte{r.ReceiptPath: receiptBefore, identityPath: sidecarBefore, path: ackBefore} {
					after, readErr := os.ReadFile(file)
					if readErr != nil || !bytes.Equal(before, after) {
						t.Fatalf("reader changed %s: %v", file, readErr)
					}
				}
			})
		}
	}
	// Current exact receipts need no legacy sidecar; missing and invalid current
	// receipts retain the original read error without pretending to be legacy.
	t.Run("current receipt", func(t *testing.T) {
		t.Parallel()
		tc := makeAcknowledgementReadCase(t, "supersession")
		tc.write()
		ack, effective, err := readValidationFailureSupersessionWithLegacyIdentity(tc.path, tc.receipt)
		if err != nil || ack.ID != tc.id() || !reflect.DeepEqual(effective, tc.receipt) {
			t.Fatalf("current read=%+v %+v %v", ack, effective, err)
		}
		if err := os.Remove(tc.path); err != nil {
			t.Fatal(err)
		}
		_, _, err = readValidationFailureSupersessionWithLegacyIdentity(tc.path, tc.receipt)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent supersession sentinel=%v", err)
		}
		if err := os.WriteFile(tc.path, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err = readValidationFailureSupersessionWithLegacyIdentity(tc.path, tc.receipt)
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("current decode=%v", err)
		}
	})
}
