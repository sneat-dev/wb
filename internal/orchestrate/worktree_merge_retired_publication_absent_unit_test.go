package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// intermediateAncestorEvidencePath builds an acknowledgement path whose parent
// has one further ancestor component the namespace tests can replace. The
// parent and the ancestor are real directories below an isolated temporary
// root; no user state is touched.
func intermediateAncestorEvidencePath(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	ancestor := filepath.Join(root, "reports")
	parent := filepath.Join(ancestor, "worktree-merge")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return ancestor, filepath.Join(parent, "ack.json")
}

// linkIntermediateAncestorAsSymlink renames an ancestor away and leaves a
// symlink of the same name in its place. The bound path still resolves to the
// identical final directory through the link, so directory identity alone
// cannot refuse it: only a full no-follow re-walk of every component can.
func linkIntermediateAncestorAsSymlink(t *testing.T, ancestor string) {
	t.Helper()
	detached := ancestor + "-held"
	if err := os.Rename(ancestor, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(detached), ancestor); err != nil {
		t.Fatal(err)
	}
}

// TestSecureLexicalDirectoryStillMatchesRefusesUnavailableDescriptor keeps the
// guard honest for a caller that holds no descriptor at all: an unavailable
// namespace can never be corroborated to match its path.
func TestSecureLexicalDirectoryStillMatchesRefusesUnavailableDescriptor(t *testing.T) {
	t.Parallel()
	if secureLexicalDirectoryStillMatches(filepath.Join(t.TempDir(), "evidence"), nil) {
		t.Fatal("corroborated a namespace without a held descriptor")
	}
}

// TestOpenPublicationNamespaceRefusesIntermediateAncestorSymlink refuses an
// already-symlinked ancestor at admission instead of admitting a namespace the
// supported reader could never reopen.
func TestOpenPublicationNamespaceRefusesIntermediateAncestorSymlink(t *testing.T) {
	t.Parallel()
	ancestor, path := intermediateAncestorEvidencePath(t)
	linkIntermediateAncestorAsSymlink(t, ancestor)
	namespace, err := openPublicationNamespace(path, false)
	if err == nil {
		_ = namespace.Close()
		t.Fatal("admitted a namespace through a symlinked ancestor")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ancestor symlink admission = %v, want no-follow refusal", err)
	}
}

// TestPersistRetiredPublicationAcknowledgementRefusesIntermediateAncestorSymlinkDuringPublication
// turns an ancestor into a symlink while publication is in flight, at the two
// durable boundaries the flow verifies. The final held directory keeps its
// exact inode, so only the full lexical re-walk refuses. Persistence must fail
// rather than report an acknowledgement the supported reader cannot discover.
func TestPersistRetiredPublicationAcknowledgementRefusesIntermediateAncestorSymlinkDuringPublication(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepLink, filewrite.StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			ancestor, path := intermediateAncestorEvidencePath(t)
			inj := &filewrite.Injector{Step: step, Hook: func() { linkIntermediateAncestorAsSymlink(t, ancestor) }}
			err := persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{ID: "ancestor-link"}, inj)
			if err == nil || !strings.Contains(err.Error(), "namespace") {
				t.Fatalf("%s intermediate ancestor symlink = %v, want namespace refusal", step, err)
			}
			if namespace, openErr := openPublicationNamespace(path, false); openErr == nil {
				_ = namespace.Close()
				t.Fatalf("%s reported through a namespace the supported reader cannot reopen", step)
			}
		})
	}
}

// TestReadSecureBoundedRegularFileRefusesIntermediateAncestorSymlinkDuringEvidenceRead
// symlinks an ancestor between the bytes and the after-read corroboration. The
// bytes read may already belong to a path the supported reader would refuse, so
// no evidence may be returned.
func TestReadSecureBoundedRegularFileRefusesIntermediateAncestorSymlinkDuringEvidenceRead(t *testing.T) {
	t.Parallel()
	ancestor, path := intermediateAncestorEvidencePath(t)
	if err := os.WriteFile(path, []byte(`{"id":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := productionSecureEvidenceReadOps()
	ops.read = func(reader io.Reader) ([]byte, error) {
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		linkIntermediateAncestorAsSymlink(t, ancestor)
		return body, nil
	}
	body, err := readSecureBoundedRegularFileWith(path, maxRetiredPublicationEvidenceBytes, ops)
	if err == nil || !strings.Contains(err.Error(), "namespace changed") || body != nil {
		t.Fatalf("evidence read across a symlinked ancestor = %q, %v, want refusal", body, err)
	}
}

// TestReadSecureBoundedRegularFileInNamespaceRefusesIntermediateAncestorSymlinkDuringCollisionRead
// does the same during a collision reread through the admitted namespace, where
// an accepted detached read would let conflicting evidence be decided from a
// namespace the acknowledgement no longer occupies.
func TestReadSecureBoundedRegularFileInNamespaceRefusesIntermediateAncestorSymlinkDuringCollisionRead(t *testing.T) {
	t.Parallel()
	ancestor, path := intermediateAncestorEvidencePath(t)
	if err := os.WriteFile(path, []byte(`{"id":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	namespace, err := openPublicationNamespace(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	ops := productionSecureEvidenceReadOps()
	ops.read = func(reader io.Reader) ([]byte, error) {
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		linkIntermediateAncestorAsSymlink(t, ancestor)
		return body, nil
	}
	body, err := readSecureBoundedRegularFileInNamespaceWith(path, maxRetiredPublicationEvidenceBytes, namespace, ops)
	if err == nil || !strings.Contains(err.Error(), "namespace") || body != nil {
		t.Fatalf("collision reread across a symlinked ancestor = %q, %v, want refusal", body, err)
	}
}

// legacyAcknowledgementGolden is the document the retired-publication writer
// persisted before the retirement adjunct existed: the single persisted schema,
// every older immutable field, and no retirement key at all. Its id is the
// digest of the legacy fields only, so this fixture pins the historical digest
// input and byte shape instead of restating today's helper.
const legacyAcknowledgementGolden = `{
  "schema_version": 1,
  "id": "0f02fa2e861491909fae25c7c29da4b3e9ad1676da8fe832e2a59b9087aea3eb",
  "status": "retired_publication_acknowledged",
  "receipt_path": "/projects/.wb/reports/worktree-merge/acme-app/main/2026-01-01T00-00-00Z.json",
  "acknowledgement_path": "/projects/.wb/reports/worktree-merge/acme-app/main/2026-01-01T00-00-00Z.json.retired-publication.ack.json",
  "receipt_id": "0123456789abcdef",
  "receipt_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "receipt_phase": "prepare",
  "receipt_status": "conflict",
  "lane": "acme-app/main",
  "repository": "acme/app",
  "target": "main",
  "receipt_target_sha": "1111111111111111111111111111111111111111",
  "current_target_sha": "2222222222222222222222222222222222222222",
  "candidate_task": "retired-task",
  "candidate_worktree": "/projects/.worktrees/retired-task/acme/app",
  "candidate_branch": "feature/retired-task",
  "candidate_sha": "3333333333333333333333333333333333333333",
  "published_candidate_sha": "4444444444444444444444444444444444444444",
  "pull_request": "https://github.com/acme/app/pull/7",
  "pull_request_state": "CLOSED",
  "pull_request_closed_at": "2026-01-01T00:00:00Z",
  "pull_request_head_sha": "4444444444444444444444444444444444444444",
  "sources": [
    {
      "task": "retired-task",
      "worktree": "/projects/.worktrees/retired-task/acme/app",
      "branch": "feature/retired-task",
      "sha": "3333333333333333333333333333333333333333",
      "merged": false
    }
  ],
  "actor": "historical operator",
  "reason": "legacy writer record",
  "recorded_at": "2026-01-02T03:04:05Z"
}
`

// legacyAcknowledgementGoldenID is the digest of exactly those legacy fields.
const legacyAcknowledgementGoldenID = "0f02fa2e861491909fae25c7c29da4b3e9ad1676da8fe832e2a59b9087aea3eb"

// referenceLegacyAcknowledgementID recomputes the pre-adjunct digest from the
// documented legacy field order without calling the production helper, so the
// golden record proves the old digest input still hashes the same way.
func referenceLegacyAcknowledgementID(ack WorktreeMergeRetiredPublicationAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptPhase), string(ack.ReceiptStatus), ack.Lane, ack.Repository, ack.Target,
		ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.CandidateTask, ack.CandidateWorktree, ack.CandidateBranch, ack.CandidateSHA, ack.PublishedCandidateSHA,
		ack.PullRequest, ack.PullRequestState, ack.PullRequestClosedAt, ack.PullRequestHeadSHA, ack.Actor, ack.Reason,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// TestLegacyAcknowledgementBytesAndDigestInputRemainUnchanged replays genuine
// pre-adjunct bytes: the same document must still decode, keep its historical
// id, and re-encode byte for byte, so an old record's history, actor, reason
// and digest input are never rewritten by the retirement binding.
func TestLegacyAcknowledgementBytesAndDigestInputRemainUnchanged(t *testing.T) {
	t.Parallel()
	var legacy WorktreeMergeRetiredPublicationAcknowledgement
	if err := json.Unmarshal([]byte(legacyAcknowledgementGolden), &legacy); err != nil {
		t.Fatalf("decode golden legacy acknowledgement: %v", err)
	}
	if retiredPublicationHasRetirementEvidence(legacy) {
		t.Fatal("golden legacy acknowledgement carries a retirement adjunct")
	}
	if legacy.SchemaVersion != worktreeMergeRetiredPublicationAcknowledgementSchemaVersion {
		t.Fatalf("golden schema = %d, want the single persisted schema %d", legacy.SchemaVersion, worktreeMergeRetiredPublicationAcknowledgementSchemaVersion)
	}
	if got := retiredPublicationAcknowledgementID(legacy); got != legacyAcknowledgementGoldenID {
		t.Fatalf("legacy digest input changed: got %s, want historical %s", got, legacyAcknowledgementGoldenID)
	}
	if got := referenceLegacyAcknowledgementID(legacy); got != legacyAcknowledgementGoldenID {
		t.Fatalf("independent legacy digest = %s, want historical %s", got, legacyAcknowledgementGoldenID)
	}
	contents, err := marshalRetiredPublicationAcknowledgement(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != legacyAcknowledgementGolden {
		t.Fatalf("legacy acknowledgement bytes changed:\n%s", contents)
	}
	// A current absent-candidate record additionally hashes its retirement
	// adjunct, and clearing that adjunct on a comparison copy restores exactly
	// the historical input without stripping the proof the caller persists.
	bound := legacy
	bound.RetirementReceiptPath = "/projects/.wb/reports/worktree-retire/retired-task/acme-app.json"
	bound.RetirementReceiptSHA256 = strings.Repeat("b", 64)
	bound.RetirementProofSHA256 = strings.Repeat("c", 64)
	bound.RetirementCanonical = "/projects/acme/app"
	bound.RetirementSourceSHA = strings.Repeat("d", 40)
	bound.RetirementRetiredRef = "retired/20260101-feature-retired-task-3333333"
	bound.RetirementArchiveRef = "retired/backstage-retired/20260101-feature-retired-task-3333333"
	bound.RetirementArchiveSHA = strings.Repeat("e", 40)
	bound.RetirementPhase = "complete"
	if retiredPublicationAcknowledgementID(bound) == legacyAcknowledgementGoldenID {
		t.Fatal("retirement adjunct is not hashed into the acknowledgement id")
	}
	comparable := bound
	clearRetiredPublicationRetirementEvidence(&comparable)
	if got := retiredPublicationAcknowledgementID(comparable); got != legacyAcknowledgementGoldenID {
		t.Fatalf("clearing the adjunct changed the legacy digest input: %s", got)
	}
	if !retiredPublicationHasRetirementEvidence(bound) {
		t.Fatal("clearing the comparison copy stripped the persisted proof")
	}
}

// TestResolveExistingRetiredPublicationAcknowledgementBindsEveryRecordToItsOwnProof
// covers the compatibility shortcut's boundaries: an absent adjunct is never
// permission for changed proof, a still-present record stays bound to its own
// evidence, and only a matching older proof is idempotent.
func TestResolveExistingRetiredPublicationAcknowledgementBindsEveryRecordToItsOwnProof(t *testing.T) {
	t.Parallel()
	legacy := func() WorktreeMergeRetiredPublicationAcknowledgement {
		var record WorktreeMergeRetiredPublicationAcknowledgement
		if err := json.Unmarshal([]byte(legacyAcknowledgementGolden), &record); err != nil {
			t.Fatalf("decode golden legacy acknowledgement: %v", err)
		}
		return record
	}
	current := func() WorktreeMergeRetiredPublicationAcknowledgement {
		record := legacy()
		record.RetirementReceiptPath = "/projects/.wb/reports/worktree-retire/retired-task/acme-app.json"
		record.RetirementReceiptSHA256 = strings.Repeat("b", 64)
		record.RetirementProofSHA256 = strings.Repeat("c", 64)
		record.RetirementCanonical = "/projects/acme/app"
		record.RetirementSourceSHA = strings.Repeat("d", 40)
		record.RetirementRetiredRef = "retired/20260101-feature-retired-task-3333333"
		record.RetirementArchiveRef = "retired/backstage-retired/20260101-feature-retired-task-3333333"
		record.RetirementArchiveSHA = strings.Repeat("e", 40)
		record.RetirementPhase = "complete"
		record.ID = retiredPublicationAcknowledgementID(record)
		return record
	}
	cases := []struct {
		name     string
		existing func() WorktreeMergeRetiredPublicationAcknowledgement
		proof    func() WorktreeMergeRetiredPublicationAcknowledgement
		refuse   bool
	}{
		{name: "historical record replay converges on its own bytes", existing: legacy, proof: current},
		{name: "still-present record replay converges on its own bytes", existing: legacy, proof: legacy},
		{name: "current absent record replay converges on its own bytes", existing: current, proof: current},
		{name: "historical record refuses a changed current target", existing: legacy, proof: func() WorktreeMergeRetiredPublicationAcknowledgement {
			record := current()
			record.CurrentTargetSHA = strings.Repeat("9", 40)
			record.ID = retiredPublicationAcknowledgementID(record)
			return record
		}, refuse: true},
		{name: "historical record refuses a changed pull-request head", existing: legacy, proof: func() WorktreeMergeRetiredPublicationAcknowledgement {
			record := current()
			record.PullRequestHeadSHA = strings.Repeat("8", 40)
			record.ID = retiredPublicationAcknowledgementID(record)
			return record
		}, refuse: true},
		{name: "historical record refuses changed sources", existing: legacy, proof: func() WorktreeMergeRetiredPublicationAcknowledgement {
			record := current()
			record.Sources = append([]WorktreeMergeSource(nil), record.Sources...)
			record.Sources[0].SHA = strings.Repeat("7", 40)
			record.ID = retiredPublicationAcknowledgementID(record)
			return record
		}, refuse: true},
		{name: "historical record refuses a changed candidate", existing: legacy, proof: func() WorktreeMergeRetiredPublicationAcknowledgement {
			record := current()
			record.CandidateSHA = strings.Repeat("6", 40)
			record.ID = retiredPublicationAcknowledgementID(record)
			return record
		}, refuse: true},
		{name: "current absent record refuses a different retirement proof", existing: current, proof: func() WorktreeMergeRetiredPublicationAcknowledgement {
			record := current()
			record.RetirementProofSHA256 = strings.Repeat("f", 64)
			record.ID = retiredPublicationAcknowledgementID(record)
			return record
		}, refuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			existing, proof := tc.existing(), tc.proof()
			resolved, err := resolveExistingRetiredPublicationAcknowledgement(existing, proof, existing.AcknowledgementPath)
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "different immutable evidence") {
					t.Fatalf("changed proof = %+v, %v, want immutable-evidence refusal", resolved, err)
				}
				if resolved.ID != "" {
					t.Fatalf("refused proof returned an acknowledgement: %+v", resolved)
				}
				return
			}
			if err != nil {
				t.Fatalf("matching proof = %v", err)
			}
			if resolved.ID != existing.ID || resolved.RecordedAt != existing.RecordedAt {
				t.Fatalf("matching proof rewrote existing evidence: %+v", resolved)
			}
		})
	}
}
