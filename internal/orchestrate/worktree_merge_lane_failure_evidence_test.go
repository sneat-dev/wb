package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestActiveMergeLanePhysicalDirectoryAndEntryPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"reports file", "missing reports", "ignored entries", "malformed selected receipt"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home, err := wbhome.EnsureRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			reports := filepath.Join(home, "reports", "worktree-merge")
			lane := worktreeMergeLaneID("acme/app", "main")
			path := filepath.Join(reports, lane+".json")
			switch mode {
			case "reports file":
				if err := os.MkdirAll(filepath.Dir(reports), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(reports, []byte("owned regular file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ignored entries":
				if err := os.MkdirAll(filepath.Join(reports, lane+"-000-directory.json"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(reports, lane+"-001-note.txt"), []byte("not a receipt"), 0600); err != nil {
					t.Fatal(err)
				}
				writeMergeLaneReceipt(t, path, WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Repository: "acme/app", Target: "main", Status: WorktreeMergePrepared, Candidate: WorktreeMergeCandidate{Branch: "feature/source"}})
			case "malformed selected receipt":
				if err := os.MkdirAll(reports, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane)
			switch mode {
			case "reports file":
				var native *os.PathError
				if receipt != nil || !errors.As(err, &native) || !errors.Is(err, syscall.ENOTDIR) || native.Path != reports {
					t.Fatalf("directory refusal receipt %+v, err %v", receipt, err)
				}
			case "missing reports":
				if receipt != nil || err != nil {
					t.Fatalf("missing reports %+v, %v", receipt, err)
				}
			case "ignored entries":
				if receipt == nil || receipt.ReceiptPath != path || err != nil {
					t.Fatalf("ignored entries hid live receipt %+v, %v", receipt, err)
				}
			case "malformed selected receipt":
				if receipt != nil || err == nil || !strings.Contains(err.Error(), "decode merge receipt "+path) {
					t.Fatalf("selected malformed receipt %+v, %v", receipt, err)
				}
				claim, claimErr := ActiveMergeLaneClaim(root, "acme/app", "feature/source")
				if claim != nil || claimErr != nil {
					t.Fatalf("branch scanner malformed sibling policy %+v, %v", claim, claimErr)
				}
			}
		})
	}
}

func TestActiveMergeLaneFailureReadersPreserveFirstRefusalAndReceiptBytes(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing cleanup", "landed failure before supersession", "supersession", "stranded", "absorbed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home, err := wbhome.EnsureRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			reports := filepath.Join(home, "reports", "worktree-merge")
			if err := os.MkdirAll(reports, 0700); err != nil {
				t.Fatal(err)
			}
			lane := worktreeMergeLaneID("acme/app", "main")
			path := filepath.Join(reports, lane+".json")
			receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Lane: lane, Repository: "acme/app", Target: "main", Status: WorktreeMergePrepared, Phase: WorktreeMergePhasePrepare, Candidate: WorktreeMergeCandidate{Branch: "feature/source"}}
			var sidecar string
			switch kind {
			case "missing cleanup":
				receipt.Status = WorktreeMergeLanded
				receipt.LandingSHA = "declared-landed-sha"
				receipt.Cleanup = true
				sidecar = path + worktreeMergeMissingCleanupAcknowledgementSuffix
			case "landed failure before supersession":
				sidecar = landedFailureAcknowledgementPath(path)
			case "supersession":
				receipt.Status = WorktreeMergeValidationFailed
				receipt.TargetSHA = "declared-target"
				receipt.Sources = []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(root, "source"), Branch: "feature/source", SHA: "declared-source"}}
				receipt.ID = worktreeMergeOperationID(lane, receipt.Sources)
				receipt.Candidate.Task = receipt.ID
				receipt.Candidate.Worktree = filepath.Join(root, "candidate")
				receipt.Candidate.SHA = "declared-candidate"
				receipt.CreatedAt = time.Now()
				receipt.UpdatedAt = receipt.CreatedAt
				if err := validatePrepareFailureSupersessionReceipt(receipt, path); err != nil {
					t.Fatalf("physical supersession decoder input: %v", err)
				}
				sidecar = validationFailureSupersessionPath(path)
			case "stranded":
				sidecar = strandedLandingAcknowledgementPath(path)
			case "absorbed":
				sidecar = absorbedConflictAcknowledgementPath(path)
			}
			writeMergeLaneReceipt(t, path, receipt)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Real persisted malformed sidecars fail through their existing decoders.
			// The declared receipt shape supplies no native custody authority.
			if err := os.WriteFile(sidecar, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			later := validationFailureSupersessionPath(path)
			if kind == "landed failure before supersession" {
				if err := os.WriteFile(later, []byte("later invalid evidence must remain unread decision input"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			active, err := activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane)
			if active != nil || err == nil || !strings.Contains(err.Error(), sidecar) {
				t.Fatalf("lane refusal %+v, err %v; expected %s", active, err, sidecar)
			}
			if kind == "missing cleanup" && !strings.Contains(err.Error(), "validate missing-cleanup acknowledgement for "+path) {
				t.Fatalf("missing-cleanup owner lost context: %v", err)
			}
			if kind == "landed failure before supersession" && strings.Contains(err.Error(), later) {
				t.Fatalf("later evidence replaced first refusal: %v", err)
			}
			claim, claimErr := ActiveMergeLaneClaim(root, "acme/app", "feature/source")
			if kind == "landed failure before supersession" || kind == "supersession" {
				if claim != nil || claimErr == nil || claimErr.Error() != err.Error() {
					t.Fatalf("shared release refusal differs: lane %v, branch %+v %v", err, claim, claimErr)
				}
				released, releaseErr := mergeReceiptReleasedByFailureEvidence(context.Background(), root, receipt)
				if released || releaseErr == nil || releaseErr.Error() != err.Error() {
					t.Fatalf("release owner bypassed refusal: %t, %v", released, releaseErr)
				}
			} else if claim == nil || claim.ReceiptPath != path || claimErr != nil {
				t.Fatalf("lane-only acknowledgement changed branch policy: %+v, %v", claim, claimErr)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("scan rewrote receipt: %v", readErr)
			}
			sidecarBytes, readErr := os.ReadFile(sidecar)
			if readErr != nil || string(sidecarBytes) != "{" {
				t.Fatalf("scan rewrote refusal evidence: %q, %v", sidecarBytes, readErr)
			}
			if kind == "landed failure before supersession" {
				laterBytes, readErr := os.ReadFile(later)
				if readErr != nil || string(laterBytes) != "later invalid evidence must remain unread decision input" {
					t.Fatalf("first refusal changed later sidecar: %q, %v", laterBytes, readErr)
				}
			}
		})
	}
}

func TestActiveMergeLaneMissingCleanupStatPreservesNativeComponentLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reports := filepath.Join(root, "reports")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	lane := worktreeMergeLaneID("acme/app", "main")
	name := lane + "-" + strings.Repeat("x", 255-len(lane)-1-len(".json")) + ".json"
	path := filepath.Join(reports, name)
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Lane: lane, Repository: "acme/app", Target: "main", Status: WorktreeMergeLanded, LandingSHA: "declared-landed-sha", Cleanup: true}
	writeMergeLaneReceipt(t, path, receipt)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := path + worktreeMergeMissingCleanupAcknowledgementSuffix
	_, nativeErr := os.Stat(ackPath)
	var native *os.PathError
	if !errors.As(nativeErr, &native) || errors.Is(nativeErr, os.ErrNotExist) || native.Path != ackPath {
		t.Fatalf("private filesystem did not produce component-limit witness: %v", nativeErr)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane)
	var observed *os.PathError
	if active != nil || !errors.Is(err, native.Err) || !errors.As(err, &observed) || observed.Path != ackPath || observed.Op != native.Op || !strings.Contains(err.Error(), "inspect missing-cleanup acknowledgement "+ackPath) {
		t.Fatalf("native stat refusal lost identity: receipt %+v, err %v", active, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("stat refusal rewrote receipt: %v", readErr)
	}
}
