//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreeretire"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func retiredPublicationClosedGH(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, headSHA string) {
	t.Helper()
	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", headSHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")
}

// fakeHeldRetirement is the test implementation of orchestrate's narrow held
// retirement capability. Production returns worktrees' descriptor-bound proof.
type fakeHeldRetirement struct {
	evidence worktrees.RetiredCheckoutEvidence
	git      func(context.Context, ...string) (string, error)
	validate func() error
	closed   bool
}

func (fake *fakeHeldRetirement) Evidence() worktrees.RetiredCheckoutEvidence {
	return fake.evidence
}

func (fake *fakeHeldRetirement) Git(ctx context.Context, args ...string) (string, error) {
	if fake.git != nil {
		return fake.git(ctx, args...)
	}
	output, _, err := runCommand(ctx, defaultRunner, 0, 0, fake.evidence.Canonical, "git", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (fake *fakeHeldRetirement) Validate() error {
	if fake.validate != nil {
		return fake.validate()
	}
	return nil
}

func (fake *fakeHeldRetirement) Close() error {
	fake.closed = true
	return nil
}

// absentRetiredPublicationFixture prepares the exact evidence shape of a
// supported preserved retirement without running the destructive retirement:
// the candidate is closed unmerged, its remote branch is gone, the candidate
// object is available in the canonical clone, and the checkout path is absent.
func absentRetiredPublicationFixture(t *testing.T) (WorktreeMergeRetiredPublicationAcknowledgementOptions, retiredPublicationPorts, WorktreeMergeReceipt, worktrees.RetiredCheckoutEvidence) {
	t.Helper()
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-absent-unit", "feature/retired-absent-unit", "absent-unit")
	retiredPublicationClosedGH(t, fixture, receipt, receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "fetch", "origin", "+refs/heads/"+receipt.Candidate.Branch+":refs/wb/test/retired-candidate")
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)
	if err := os.RemoveAll(receipt.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	evidence := worktrees.RetiredCheckoutEvidence{
		Canonical: fixture.canonical,
		Retirement: worktreeretire.Transaction{
			Task: receipt.Candidate.Task, Repository: receipt.Repository, SourceSHA: receipt.Candidate.SHA,
			RetiredRef: "retired/20260101-" + receipt.Candidate.Branch + "-" + receipt.Candidate.SHA[:7],
			ArchiveRef: "retired/backstage-retired/20260101-" + receipt.Candidate.Branch + "-" + receipt.Candidate.SHA[:7],
			ArchiveSHA: strings.Repeat("e", 40), Phase: "complete",
		},
		ReceiptPath:   filepath.Join(fixture.githubDir, ".wb", "reports", "worktree-retire", receipt.Candidate.Task, "acme-app.json"),
		ReceiptSHA256: strings.Repeat("a", 64),
		ProofSHA256:   strings.Repeat("b", 64),
	}
	ports := productionRetiredPublicationPorts()
	ports.proveRetirement = func(context.Context, worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
		return &fakeHeldRetirement{evidence: evidence}, nil
	}
	options := WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "operator", Reason: "supported preserved retirement",
	}
	return options, ports, receipt, evidence
}

func TestE2EAcknowledgeRetiredPublicationRequiresSupportedRetirementProof(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-absent-noproof", "feature/retired-absent-noproof", "noproof")
	if err := os.RemoveAll(receipt.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	ports := productionRetiredPublicationPorts()
	ports.proveRetirement = func(context.Context, worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
		return nil, errors.New("retirement receipt missing")
	}
	_, err := acknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: false,
	}, ports)
	if err == nil || !strings.Contains(err.Error(), "absent without a supported preserved retirement") {
		t.Fatalf("absent candidate without retirement = %v", err)
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesChangedRetirementEvidence(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	changed := ack
	changed.RetirementProofSHA256 = strings.Repeat("9", 64)
	changed.ID = retiredPublicationAcknowledgementID(changed)
	if err := os.Remove(ackPath); err != nil {
		t.Fatal(err)
	}
	if err := persistRetiredPublicationAcknowledgement(ackPath, changed); err != nil {
		t.Fatal(err)
	}
	_, err = acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "different immutable evidence") {
		t.Fatalf("changed retirement evidence = %v", err)
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRejectsInvalidStoredRetirementEvidence(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err != nil {
		t.Fatal(err)
	}
	ack.RetirementPhase = "planned"
	ack.ID = retiredPublicationAcknowledgementID(ack)
	if err := os.Remove(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	if err := persistRetiredPublicationAcknowledgement(ack.AcknowledgementPath, ack); err != nil {
		t.Fatal(err)
	}
	if _, err := readRetiredPublicationAcknowledgement(ack.AcknowledgementPath, receipt); err == nil || !strings.Contains(err.Error(), "invalid retirement evidence") {
		t.Fatalf("stored retirement evidence = %v", err)
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesGitObservationFailures(t *testing.T) {
	for _, command := range []string{"ls-remote", "fetch", "merge-base"} {
		//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
		t.Run(command, func(t *testing.T) {
			options, ports, _, _ := absentRetiredPublicationFixture(t)
			ports.run = prepareOwnerObservedRunner{Runner: ports.run, before: func(_ context.Context, _ string, name string, args []string) error {
				if name == "git" && len(args) > 0 && args[0] == command {
					return errors.New("injected git observation failure")
				}
				return nil
			}}
			base := ports.proveRetirement
			ports.proveRetirement = func(ctx context.Context, options worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
				held, err := base(ctx, options)
				if err != nil {
					return nil, err
				}
				fake := held.(*fakeHeldRetirement)
				fake.git = func(ctx context.Context, args ...string) (string, error) {
					output, _, err := runCommand(ctx, ports.run, 0, 0, fake.evidence.Canonical, "git", args...)
					if err != nil {
						return "", err
					}
					return strings.TrimSpace(output), nil
				}
				return fake, nil
			}
			_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			want := map[string]string{"ls-remote": "inspect candidate publication state", "fetch": "fetch exact remote target", "merge-base": "verify candidate"}[command]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal = %v, want containing %q", command, err, want)
			}
		})
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationAcceptsCandidateOnlyPublication(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	receipt.PublishedCandidateSHA = ""
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	options.Apply = false
	if _, err := acknowledgeRetiredPublication(context.Background(), options, ports); err != nil {
		t.Fatalf("candidate-only publication = %v", err)
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationPropagatesSnapshotAndPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"final snapshot", "persist"} {
		//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
		t.Run(mode, func(t *testing.T) {
			options, ports, _, _ := absentRetiredPublicationFixture(t)
			sentinel := errors.New("injected " + mode + " failure")
			original := ports.readReceipt
			switch mode {
			case "final snapshot":
				reads := 0
				ports.readReceipt = func(path string) (WorktreeMergeReceipt, string, error) {
					reads++
					if reads > 2 {
						return WorktreeMergeReceipt{}, "", sentinel
					}
					return original(path)
				}
			case "persist":
				ports.persist = func(*publicationNamespace, string, WorktreeMergeRetiredPublicationAcknowledgement) error {
					return sentinel
				}
			}
			_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			if !errors.Is(err, sentinel) {
				t.Fatalf("%s failure = %v", mode, err)
			}
		})
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesMalformedExistingAcknowledgement(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	if err := os.MkdirAll(filepath.Dir(ackPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ackPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "decode retired-publication acknowledgement") {
		t.Fatalf("malformed acknowledgement = %v", err)
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesCandidateRaceBeforePersist(t *testing.T) {
	for _, mode := range []string{"appeared", "inspection failure", "hook failure"} {
		//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
		t.Run(mode, func(t *testing.T) {
			options, ports, receipt, _ := absentRetiredPublicationFixture(t)
			ports.beforePersist = func() error {
				switch mode {
				case "appeared":
					return os.MkdirAll(receipt.Candidate.Worktree, 0o700)
				case "inspection failure":
					parent := filepath.Dir(receipt.Candidate.Worktree)
					if err := os.RemoveAll(parent); err != nil {
						return err
					}
					return os.WriteFile(parent, []byte("not a directory"), 0o600)
				default:
					return errors.New("injected acknowledgement failure")
				}
			}
			_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			want := "changed while proving retirement"
			switch mode {
			case "inspection failure":
				want = "recheck candidate worktree"
			case "hook failure":
				want = "injected acknowledgement failure"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal = %v, want containing %q", mode, err, want)
			}
			if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
				t.Fatalf("%s wrote acknowledgement: %v", mode, statErr)
			}
		})
	}
}

// TestE2EAcknowledgeRetiredPublicationRacingSidecarIsIdempotentOrRefused drives a
// real sidecar that appears between the caller's existence check and the
// no-replace publication: identical evidence is idempotent, conflicting or
// malformed evidence refuses and keeps the original bytes untouched.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRacingSidecarIsIdempotentOrRefused(t *testing.T) {
	for _, mode := range []string{"matching", "conflicting", "malformed"} {
		//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
		t.Run(mode, func(t *testing.T) {
			options, ports, receipt, _ := absentRetiredPublicationFixture(t)
			ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
			dry := options
			dry.Apply = false
			expected, err := acknowledgeRetiredPublication(context.Background(), dry, ports)
			if err != nil {
				t.Fatal(err)
			}
			var racing []byte
			switch mode {
			case "matching":
				racing = nil
			case "conflicting":
				conflicting := expected
				conflicting.CurrentTargetSHA = strings.Repeat("c", 40)
				conflicting.ID = retiredPublicationAcknowledgementID(conflicting)
				racing = mustMarshalRetiredPublicationAcknowledgement(t, conflicting)
			case "malformed":
				racing = []byte("{broken")
			}
			ports.beforePersist = func() error {
				if err := os.MkdirAll(filepath.Dir(ackPath), 0o700); err != nil {
					return err
				}
				if racing == nil {
					return persistRetiredPublicationAcknowledgement(ackPath, expected)
				}
				return os.WriteFile(ackPath, racing, 0o600)
			}
			ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			switch mode {
			case "matching":
				if err != nil {
					t.Fatalf("matching sidecar = %v", err)
				}
				if ack.ID != expected.ID {
					t.Fatalf("matching sidecar returned ID %s, want original %s", ack.ID, expected.ID)
				}
			case "conflicting":
				if err == nil || !strings.Contains(err.Error(), "different immutable evidence") {
					t.Fatalf("conflicting sidecar = %v", err)
				}
			case "malformed":
				if err == nil || !strings.Contains(err.Error(), "decode retired-publication acknowledgement") {
					t.Fatalf("malformed sidecar = %v", err)
				}
			}
			got, readErr := os.ReadFile(ackPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := racing
			if mode == "matching" {
				want = mustMarshalRetiredPublicationAcknowledgement(t, expected)
			}
			if string(got) != string(want) {
				t.Fatalf("%s sidecar bytes were rewritten", mode)
			}
		})
	}
}

func mustMarshalRetiredPublicationAcknowledgement(t *testing.T, ack WorktreeMergeRetiredPublicationAcknowledgement) []byte {
	t.Helper()
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(contents, '\n')
}

// TestE2EAcknowledgeRetiredPublicationRefusesMergerReceiptChangedBeforePersist
// changes the receipt after every proof but immediately before publication;
// the final exact-snapshot fence must refuse and leave no acknowledgement.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesMergerReceiptChangedBeforePersist(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ports.beforePersist = func() error {
		changed := receipt
		changed.Failure = "changed after every proof"
		return persistWorktreeMergeReceipt(changed)
	}
	_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "changed while proving retired publication") {
		t.Fatalf("changed merger receipt = %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed merger receipt published an acknowledgement: %v", statErr)
	}
}

// TestE2EAcknowledgeRetiredPublicationPreservesLegacyAcknowledgement retires a
// candidate whose exact receipt already carries an older valid acknowledgement
// without retirement fields: that legacy evidence remains idempotent and its
// original ID/bytes are returned rather than rewritten.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationPreservesLegacyAcknowledgement(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	dry := options
	dry.Apply = false
	bound, err := acknowledgeRetiredPublication(context.Background(), dry, ports)
	if err != nil {
		t.Fatal(err)
	}
	legacy := bound
	// Reproduce exactly what the pre-adjunct writer persisted for this same
	// receipt: the shared schema, the same legacy fields, no retirement key and
	// the historical digest input. The id is derived from those old bytes rather
	// than guessed, and the independent reference digest proves the input.
	clearRetiredPublicationRetirementEvidence(&legacy)
	legacy.ID = referenceLegacyAcknowledgementID(legacy)
	if got := retiredPublicationAcknowledgementID(legacy); got != legacy.ID {
		t.Fatalf("legacy bytes hash to %s, want the historical digest input %s", got, legacy.ID)
	}
	legacyBytes := mustMarshalRetiredPublicationAcknowledgement(t, legacy)
	if bytes.Contains(legacyBytes, []byte("retirement_")) {
		t.Fatalf("legacy bytes carry a retirement key: %s", legacyBytes)
	}
	if err := os.WriteFile(ackPath, legacyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err != nil {
		t.Fatalf("legacy acknowledgement = %v", err)
	}
	if got.ID != legacy.ID || retiredPublicationHasRetirementEvidence(got) {
		t.Fatalf("legacy acknowledgement = %+v", got)
	}
	after, readErr := os.ReadFile(ackPath)
	if readErr != nil || string(after) != string(legacyBytes) {
		t.Fatalf("legacy acknowledgement bytes were rewritten: %v", readErr)
	}
}

// TestE2EAcknowledgeRetiredPublicationRefusesChangedFinalReceiptShape covers the
// last receipt revalidation: the locked digest still matches, but the receipt
// that produced it no longer validates, so publication refuses.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesChangedFinalReceiptShape(t *testing.T) {
	options, ports, _, _ := absentRetiredPublicationFixture(t)
	original := ports.readReceipt
	reads := 0
	ports.readReceipt = func(path string) (WorktreeMergeReceipt, string, error) {
		receipt, digest, err := original(path)
		reads++
		if reads == 3 {
			receipt.Lane = ""
		}
		return receipt, digest, err
	}
	_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "inconsistent immutable receipt identity") {
		t.Fatalf("changed final receipt shape = %v", err)
	}
}

// TestE2EAcknowledgeRetiredPublicationRefusesTargetResolutionFailure covers the
// fetch's rev-parse boundary through the held session.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesTargetResolutionFailure(t *testing.T) {
	options, ports, _, _ := absentRetiredPublicationFixture(t)
	base := ports.proveRetirement
	ports.proveRetirement = func(ctx context.Context, proofOptions worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
		held, err := base(ctx, proofOptions)
		if err != nil {
			return nil, err
		}
		fake := held.(*fakeHeldRetirement)
		fake.git = func(ctx context.Context, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "rev-parse" {
				return "", errors.New("injected target resolution failure")
			}
			output, _, err := runCommand(ctx, ports.run, 0, 0, fake.evidence.Canonical, "git", args...)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(output), nil
		}
		return fake, nil
	}
	_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "injected target resolution failure") {
		t.Fatalf("target resolution failure = %v", err)
	}
}

// TestE2EAcknowledgeRetiredPublicationRefusesParentSwapBeforePublication
// replaces the acknowledgement's bound parent after every proof but before
// persistence, leaving byte-identical receipt evidence in the replacement.
// The held namespace must refuse: no acknowledgement may be reported at a
// path the admitted evidence no longer occupies, and none may be left in the
// detached directory as a substitute success.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesParentSwapBeforePublication(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	parent := filepath.Dir(ackPath)
	detached := parent + "-held"
	receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ports.beforePersist = func() error {
		if err := os.Rename(parent, detached); err != nil {
			return err
		}
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		return os.WriteFile(receipt.ReceiptPath, receiptBytes, 0o600)
	}
	ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "acknowledgement parent changed before publication") {
		t.Fatalf("parent swap before publication = %+v, %v", ack, err)
	}
	if ack.ID != "" || ack.AcknowledgementPath != "" {
		t.Fatalf("parent swap returned a successful acknowledgement: %+v", ack)
	}
	if _, statErr := os.Stat(ackPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent swap published an acknowledgement at its bound path: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(detached, filepath.Base(ackPath))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent swap left a detached acknowledgement: %v", statErr)
	}
}

// TestE2EAcknowledgeRetiredPublicationRefusesIntermediateAncestorSymlinkBeforePublication
// replaces an ancestor of the acknowledgement's parent with a symlink after
// every proof but before persistence, leaving the final parent directory at its
// exact inode and the receipt bytes readable through the link. Directory
// identity alone still matches, so only a no-follow refusal can reject an
// acknowledgement the supported reader could never reopen. Both fences are
// covered: the final receipt snapshot, and the namespace authorization that
// runs immediately before staging.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestE2EAcknowledgeRetiredPublicationRefusesIntermediateAncestorSymlinkBeforePublication(t *testing.T) {
	for _, boundary := range []string{"receipt fence", "namespace authorization"} {
		//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables; the two rows share that environment and stay sequential.
		t.Run(boundary, func(t *testing.T) {
			options, ports, receipt, _ := absentRetiredPublicationFixture(t)
			ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
			ancestor := filepath.Dir(filepath.Dir(ackPath))
			detached := ancestor + "-held"
			heldParent, err := os.Stat(filepath.Dir(ackPath))
			if err != nil {
				t.Fatal(err)
			}
			swap := func() { linkIntermediateAncestorAsSymlink(t, ancestor) }
			want := ""
			switch boundary {
			case "receipt fence":
				ports.beforePersist = func() error {
					swap()
					return nil
				}
				want = "symlink"
			case "namespace authorization":
				persist := ports.persist
				ports.persist = func(namespace *publicationNamespace, path string, ack WorktreeMergeRetiredPublicationAcknowledgement) error {
					swap()
					return persist(namespace, path, ack)
				}
				want = "no longer holds the admitted directory"
			}
			ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s ancestor symlink = %+v, %v, want %q", boundary, ack, err, want)
			}
			if ack.ID != "" || ack.AcknowledgementPath != "" {
				t.Fatalf("%s ancestor symlink returned a successful acknowledgement: %+v", boundary, ack)
			}
			current, statErr := os.Stat(filepath.Dir(ackPath))
			if statErr != nil || !os.SameFile(heldParent, current) {
				t.Fatalf("%s final parent directory was not left at its exact inode: %v", boundary, statErr)
			}
			if _, statErr := os.Stat(ackPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s ancestor symlink published an acknowledgement at its bound path: %v", boundary, statErr)
			}
			if _, statErr := os.Stat(filepath.Join(detached, filepath.Base(filepath.Dir(ackPath)), filepath.Base(ackPath))); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s ancestor symlink left a detached acknowledgement: %v", boundary, statErr)
			}
		})
	}
}
