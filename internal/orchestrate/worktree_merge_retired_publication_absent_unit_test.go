package orchestrate

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestObserveCandidateCheckoutDistinguishesPathReplacements(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "checkout")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if state, err := observeCandidateCheckout(directory); err != nil || state != candidateCheckoutPresent {
		t.Fatalf("directory state = %v, %v", state, err)
	}
	if state, err := observeCandidateCheckout(filepath.Join(root, "absent")); err != nil || state != candidateCheckoutAbsent {
		t.Fatalf("absent state = %v, %v", state, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if state, err := observeCandidateCheckout(link); err != nil || state != candidateCheckoutInvalid {
		t.Fatalf("symlink state = %v, %v", state, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a checkout"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err := observeCandidateCheckout(file); err != nil || state != candidateCheckoutInvalid {
		t.Fatalf("file state = %v, %v", state, err)
	}
	if state, err := observeCandidateCheckout(filepath.Join(file, "child")); err == nil || state != candidateCheckoutInvalid {
		t.Fatalf("inspection error state = %v, %v", state, err)
	}
}

func TestValidateRetiredPublicationRetirementEvidenceRejectsIncompleteOrInvalidFields(t *testing.T) {
	t.Parallel()
	base := func() WorktreeMergeRetiredPublicationAcknowledgement {
		return WorktreeMergeRetiredPublicationAcknowledgement{
			RetirementReceiptPath: "/projects/.wb/reports/worktree-retire/task/acme-app.json", RetirementReceiptSHA256: strings.Repeat("a", 64),
			RetirementProofSHA256: strings.Repeat("b", 64), RetirementCanonical: "/projects/acme/app",
			RetirementSourceSHA: strings.Repeat("c", 40), RetirementRetiredRef: "retired/20260101-task-ccccccc",
			RetirementArchiveRef: "retired/backstage-retired/20260101-task-ccccccc", RetirementArchiveSHA: strings.Repeat("d", 40), RetirementPhase: "complete",
		}
	}
	if err := validateRetiredPublicationRetirementEvidence(base()); err != nil {
		t.Fatalf("baseline retirement evidence: %v", err)
	}
	cases := []struct {
		name   string
		change func(*WorktreeMergeRetiredPublicationAcknowledgement)
		want   string
	}{
		{"incomplete", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementPhase = "" }, "incomplete"},
		{"relative receipt path", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementReceiptPath = "relative.json" }, "receipt path"},
		{"unclean receipt path", func(a *WorktreeMergeRetiredPublicationAcknowledgement) {
			a.RetirementReceiptPath = "/projects/./receipt.json"
		}, "receipt path"},
		{"relative canonical", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementCanonical = "relative" }, "canonical clone"},
		{"unclean canonical", func(a *WorktreeMergeRetiredPublicationAcknowledgement) {
			a.RetirementCanonical = "/projects/./acme/app"
		}, "canonical clone"},
		{"bad receipt digest", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementReceiptSHA256 = "short" }, "digests are invalid"},
		{"bad proof digest", func(a *WorktreeMergeRetiredPublicationAcknowledgement) {
			a.RetirementProofSHA256 = strings.Repeat("A", 64)
		}, "digests are invalid"},
		{"bad source object", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementSourceSHA = "not-an-object" }, "source or archive object"},
		{"bad archive object", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementArchiveSHA = "not-an-object" }, "source or archive object"},
		{"bad retired ref", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementRetiredRef = "refs/heads/topic" }, "refs are invalid"},
		{"wrong phase", func(a *WorktreeMergeRetiredPublicationAcknowledgement) { a.RetirementPhase = "original_deleted" }, "want complete"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ack := base()
			tt.change(&ack)
			if err := validateRetiredPublicationRetirementEvidence(ack); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("evidence error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSameRetiredPublicationRetirementEvidenceComparesExactProof(t *testing.T) {
	t.Parallel()
	left := WorktreeMergeRetiredPublicationAcknowledgement{
		RetirementReceiptPath: "/receipt", RetirementReceiptSHA256: strings.Repeat("a", 64), RetirementProofSHA256: strings.Repeat("b", 64),
		RetirementCanonical: "/canonical", RetirementSourceSHA: strings.Repeat("c", 40), RetirementRetiredRef: "retired/ref",
		RetirementArchiveRef: "retired/archive", RetirementArchiveSHA: strings.Repeat("d", 40), RetirementPhase: "complete",
	}
	right := left
	if !sameRetiredPublicationRetirementEvidence(left, right) {
		t.Fatal("identical retirement evidence compared different")
	}
	right.RetirementProofSHA256 = strings.Repeat("e", 64)
	if sameRetiredPublicationRetirementEvidence(left, right) {
		t.Fatal("different retirement proof compared equal")
	}
}

func TestProveRetiredPullRequestRequiresExactClosedHead(t *testing.T) {
	t.Parallel()
	candidate := strings.Repeat("a", 40)
	receipt := WorktreeMergeReceipt{
		Repository: "acme/app", PullRequest: "https://example.test/acme/app/pull/1", PublishedCandidateSHA: candidate,
		Candidate: WorktreeMergeCandidate{Branch: "feature", SHA: candidate},
	}
	view := func(state, mergedAt, headRef, headOID string) string {
		return `{"state":"` + state + `","closedAt":"2026-09-01T00:00:00Z","mergedAt":"` + mergedAt +
			`","headRefName":"` + headRef + `","headRefOid":"` + headOID + `","mergeCommit":{"oid":""}}`
	}
	cases := []struct {
		name    string
		receipt WorktreeMergeReceipt
		output  string
		readErr error
		want    string
	}{
		{"closed exact head", receipt, view("CLOSED", "", "feature", candidate), nil, ""},
		{"closed exact candidate fallback", WorktreeMergeReceipt{Repository: "acme/app", PullRequest: "https://example.test/acme/app/pull/1", Candidate: WorktreeMergeCandidate{Branch: "feature", SHA: candidate}}, view("CLOSED", "", "feature", candidate), nil, ""},
		{"closed wrong head OID", receipt, view("CLOSED", "", "feature", strings.Repeat("b", 40)), nil, "head"},
		{"closed missing head OID", receipt, view("CLOSED", "", "feature", ""), nil, "head"},
		{"closed wrong head ref", receipt, view("CLOSED", "", "other", candidate), nil, "head branch"},
		{"merged", receipt, view("MERGED", "2026-09-01T00:00:00Z", "feature", candidate), nil, "acknowledge-stranded-landing"},
		{"open", receipt, view("OPEN", "", "feature", candidate), nil, "want CLOSED"},
		{"read failure", receipt, "", errors.New("gh unavailable"), "read pull-request retirement state"},
		{"malformed", receipt, "{broken", nil, "decode pull-request retirement state"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			readPR := func(context.Context, string, ...string) (string, error) { return tt.output, tt.readErr }
			_, err := proveRetiredPullRequest(context.Background(), tt.receipt, readPR)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("proved retired pull request: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("pull-request proof error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAcknowledgeRetiredPublicationRefusesUninspectableOrReplacedCandidate(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"inspection failure", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root, receipt := terminalOwnerPolicyReceipt(t)
			candidate := filepath.Join(root, "candidate")
			if mode == "inspection failure" {
				blocker := filepath.Join(root, "blocker")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				candidate = filepath.Join(blocker, "candidate")
			} else if err := os.WriteFile(candidate, []byte("not a checkout"), 0o600); err != nil {
				t.Fatal(err)
			}
			receipt.Candidate.Worktree = candidate
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			_, err := acknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
				ProjectsRoot: root, Receipt: receipt.ReceiptPath,
			}, productionRetiredPublicationPorts())
			want := "is not a real directory"
			if mode == "inspection failure" {
				want = "inspect candidate worktree"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal = %v, want containing %q", mode, err, want)
			}
		})
	}
}

func TestReadRetiredPublicationAcknowledgementReportsMissingReceipt(t *testing.T) {
	t.Parallel()
	_, receipt := terminalOwnerPolicyReceipt(t)
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	if err := os.WriteFile(ackPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readRetiredPublicationAcknowledgement(ackPath, receipt); err == nil {
		t.Fatal("missing receipt hash accepted")
	}
}

func TestEffectiveUnpublishedConflictTreatsEmptyPublicationAsUnpublished(t *testing.T) {
	t.Parallel()
	if unpublished, err := effectiveUnpublishedConflict(WorktreeMergeReceipt{}); err != nil || !unpublished {
		t.Fatalf("empty publication = %v, %v", unpublished, err)
	}
}

// TestPersistRetiredPublicationAcknowledgementNeverReplacesExistingEvidence
// proves the publication primitive itself refuses an existing destination:
// bytes that appear after a caller's own existence check are never overwritten.
func TestPersistRetiredPublicationAcknowledgementNeverReplacesExistingEvidence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "retired-publication-ack.json")
	original := []byte(`{"original":"evidence"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	err := persistRetiredPublicationAcknowledgement(path, WorktreeMergeRetiredPublicationAcknowledgement{ID: "replacement"})
	if !errors.Is(err, errRetiredPublicationAcknowledgementExists) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("no-replace publication = %v, want existing-evidence refusal", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("existing evidence changed: %q %v", got, readErr)
	}
}

// TestPersistRetiredPublicationAcknowledgementRefusesParentSwapDuringPublication
// swaps the acknowledgement's bound parent while publication is in flight.
// The held directory descriptor can no longer be corroborated against the
// lexical parent, so persistence must refuse instead of reporting an
// acknowledgement that exists only in a detached renamed directory.
func TestPersistRetiredPublicationAcknowledgementRefusesParentSwapDuringPublication(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepLink, filewrite.StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			held := filepath.Join(root, "evidence")
			moved := filepath.Join(root, "evidence-held")
			if err := os.MkdirAll(held, 0o700); err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Hook: func() {
				if err := os.Rename(held, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(held, 0o700); err != nil {
					t.Fatal(err)
				}
			}}
			path := filepath.Join(held, "retired-publication-ack.json")
			err := persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{ID: "detached"}, inj)
			if err == nil || !strings.Contains(err.Error(), "namespace") {
				t.Fatalf("%s parent swap = %v, want namespace refusal", step, err)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s published an acknowledgement outside its bound namespace: %v", step, statErr)
			}
		})
	}
}

// TestReadSecureBoundedRegularFileInNamespaceRefusesParentSwapDuringCollisionRead
// swaps the bound parent while a collision reread is in flight: the bytes
// already read may describe the detached directory, so the read must refuse.
func TestReadSecureBoundedRegularFileInNamespaceRefusesParentSwapDuringCollisionRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	held := filepath.Join(root, "evidence")
	moved := filepath.Join(root, "evidence-held")
	if err := os.MkdirAll(held, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(held, "retired-publication-ack.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	namespace, err := openPublicationNamespace(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	ops := productionSecureEvidenceReadOps()
	ops.read = func(reader io.Reader) ([]byte, error) {
		if err := os.Rename(held, moved); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(held, 0o700); err != nil {
			return nil, err
		}
		return io.ReadAll(reader)
	}
	if _, err := readSecureBoundedRegularFileInNamespaceWith(path, maxRetiredPublicationEvidenceBytes, namespace, ops); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("parent swap during collision reread = %v, want namespace refusal", err)
	}
}

// TestReadWorktreeMergeReceiptSnapshotRefusesNonRegularOrOversizeEvidence
// keeps the secure snapshot reader's shape guarantees: a directory, a
// symlink and an oversize replacement are refused before any decode.
func TestReadWorktreeMergeReceiptSnapshotRefusesNonRegularOrOversizeEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "receipt.json")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeReceiptSnapshot(directory); err == nil || !strings.Contains(err.Error(), "not one bounded regular evidence file") {
		t.Fatalf("directory receipt snapshot = %v", err)
	}
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeReceiptSnapshot(link); err == nil {
		t.Fatal("symlinked receipt snapshot was followed")
	}
	oversize := filepath.Join(root, "oversize.json")
	if err := os.WriteFile(oversize, make([]byte, maxRetiredPublicationEvidenceBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeReceiptSnapshot(oversize); err == nil || !strings.Contains(err.Error(), "not one bounded regular evidence file") {
		t.Fatalf("oversize receipt snapshot = %v", err)
	}
}

// TestReadSecureBoundedRegularFileRefusesEachUnreadableShape drives every
// injected boundary of the evidence snapshot reader: parent open, file open,
// stat, read and a read that exceeds the bound without a matching stat.
func TestReadSecureBoundedRegularFileRefusesEachUnreadableShape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected evidence read failure")
	cases := []struct {
		name   string
		change func(*secureEvidenceReadOps)
		want   string
	}{
		{"parent open", func(ops *secureEvidenceReadOps) {
			ops.openParent = func(string) (*os.File, error) { return nil, boom }
		}, "injected evidence read failure"},
		{"file open", func(ops *secureEvidenceReadOps) {
			ops.openFile = func(*os.File, string) (*os.File, error) { return nil, boom }
		}, "injected evidence read failure"},
		{"stat", func(ops *secureEvidenceReadOps) {
			ops.stat = func(*os.File) (os.FileInfo, error) { return nil, boom }
		}, "injected evidence read failure"},
		{"read", func(ops *secureEvidenceReadOps) {
			ops.read = func(io.Reader) ([]byte, error) { return nil, boom }
		}, "injected evidence read failure"},
		{"read exceeds the bound", func(ops *secureEvidenceReadOps) {
			ops.read = func(io.Reader) ([]byte, error) { return make([]byte, maxRetiredPublicationEvidenceBytes+1), nil }
		}, "exceeds the bounded evidence size"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ops := productionSecureEvidenceReadOps()
			tt.change(&ops)
			if _, err := readSecureBoundedRegularFileWith(path, maxRetiredPublicationEvidenceBytes, ops); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("%s read = %v, want containing %q", tt.name, err, tt.want)
			}
		})
	}
}

// TestPersistRetiredPublicationAcknowledgementRefusesSymlinkedParent binds the
// publication namespace: a parent that resolves through a symlink is refused
// instead of publishing the evidence into an aliased directory.
func TestPersistRetiredPublicationAcknowledgementRefusesSymlinkedParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := persistRetiredPublicationAcknowledgement(filepath.Join(link, "ack.json"), WorktreeMergeRetiredPublicationAcknowledgement{ID: "symlinked"}); err == nil {
		t.Fatal("published evidence through a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(real, "ack.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence reached the symlink target: %v", err)
	}
}

// TestRetiredPublicationGitPropagatesRunnerFailureWithoutHeldSession covers
// the still-present-candidate path: a Git observation failure at that checkout
// is returned rather than being mistaken for an absent remote ref.
func TestRetiredPublicationGitPropagatesRunnerFailureWithoutHeldSession(t *testing.T) {
	t.Parallel()
	run := runnertest.New(t)
	run.Expect(func(runnertest.Call) bool { return true }, runner.Result{}, nil)
	run.FailCall(1, errors.New("injected runner failure"))
	if _, err := retiredPublicationGit(context.Background(), run, nil, t.TempDir(), "ls-remote", "origin"); err == nil || !strings.Contains(err.Error(), "injected runner failure") {
		t.Fatalf("runner failure = %v", err)
	}
}

// TestReadWorktreeMergeReceiptSnapshotRefusesMalformedReceipt keeps the
// shared decoder's refusal on the snapshot path, so a malformed replacement
// never yields a digest that could be bound into evidence.
func TestReadWorktreeMergeReceiptSnapshotRefusesMalformedReceipt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorktreeMergeReceiptSnapshot(path); err == nil || !strings.Contains(err.Error(), "decode merge receipt") {
		t.Fatalf("malformed receipt snapshot = %v", err)
	}
}
