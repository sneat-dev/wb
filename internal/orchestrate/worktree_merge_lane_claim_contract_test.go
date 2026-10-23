package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestMergeLaneClaimPrivateFilesystemRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"invalid home ancestor", "reports file", "missing reports", "empty repository", "empty branch"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repository, branch := "acme/app", "feature/source"
			switch mode {
			case "invalid home ancestor":
				file := filepath.Join(root, "ancestor")
				if err := os.WriteFile(file, []byte("owned regular file"), 0600); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(file, "projects")
			case "reports file":
				home, err := wbhome.EnsureRoot(root)
				if err != nil {
					t.Fatal(err)
				}
				reports := filepath.Join(home, "reports")
				if err := os.MkdirAll(reports, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(reports, "worktree-merge"), []byte("owned regular file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty repository":
				repository = " \t"
			case "empty branch":
				branch = " \n"
			}
			claim, err := ActiveMergeLaneClaim(root, repository, branch)
			if claim != nil {
				t.Fatalf("unexpected claim %+v", claim)
			}
			if mode == "invalid home ancestor" || mode == "reports file" {
				var pe *os.PathError
				if !errors.Is(err, syscall.ENOTDIR) || (mode == "reports file" && !errors.As(err, &pe)) {
					t.Fatalf("expected native path refusal, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMergeLaneClaimSidecarDecodeFailuresAreNotClaims(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"landed failure", "supersession", "rebatch"} {
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
			path := filepath.Join(reports, "receipt.json")
			// Scanner input is a real persisted JSON record; it makes no claim of native Git custody.
			receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Repository: "acme/app", Target: "main", Status: WorktreeMergePrepared, Candidate: WorktreeMergeCandidate{Branch: "feature/source"}}
			if kind == "supersession" {
				receipt.Lane = worktreeMergeLaneID(receipt.Repository, receipt.Target)
				receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergeValidationFailed
				receipt.TargetSHA = "declared-target"
				receipt.Sources = []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(root, "source"), Branch: "feature/source", SHA: "declared-source"}}
				receipt.ID = worktreeMergeOperationID(receipt.Lane, receipt.Sources)
				receipt.Candidate.Task, receipt.Candidate.Worktree, receipt.Candidate.SHA = receipt.ID, filepath.Join(root, "candidate"), "declared-candidate"
				receipt.CreatedAt, receipt.UpdatedAt = time.Now(), time.Now()
				if err := validatePrepareFailureSupersessionReceipt(receipt, path); err != nil {
					t.Fatalf("declared decoder input shape: %v", err)
				}
			}
			writeMergeLaneReceipt(t, path, receipt)
			var sidecar string
			switch kind {
			case "landed failure":
				sidecar = landedFailureAcknowledgementPath(path)
			case "supersession":
				sidecar = validationFailureSupersessionPath(path)
			case "rebatch":
				sidecar = rebatchPath(path)
			}
			if err := os.WriteFile(sidecar, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			claim, err := ActiveMergeLaneClaim(root, "acme/app", "feature/source")
			if claim != nil || err == nil || !strings.Contains(err.Error(), sidecar) {
				t.Fatalf("claim=%+v error=%v; expected actual decoder refusal at %s", claim, err, sidecar)
			}
		})
	}
}

func TestMergeLaneClaimIgnoresNonReceiptEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(filepath.Join(reports, "000-directory.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "001-note.txt"), []byte("not a receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(reports, "receipt.json")
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Lane: "recorded-lane", Repository: "acme/app", Target: "main", Status: WorktreeMergePrepared, Candidate: WorktreeMergeCandidate{Branch: "feature/source"}}
	writeMergeLaneReceipt(t, path, receipt)
	if claim, err := ActiveMergeLaneClaim(root, "acme/app", "feature/unclaimed"); err != nil || claim != nil {
		t.Fatalf("unrelated branch inherited a scanner claim: %+v, %v", claim, err)
	}
	claim, err := ActiveMergeLaneClaim(root, " acme/app ", " feature/source ")
	if err != nil || claim == nil || claim.ReceiptPath != path || claim.Lane != "recorded-lane" || claim.Target != "main" || claim.Status != string(WorktreeMergePrepared) {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
}
