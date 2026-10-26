package orchestrate

import (
	"context"
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

func TestAcknowledgeRetiredPublicationRequiresSupportedRetirementProof(t *testing.T) {
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
func TestAcknowledgeRetiredPublicationRefusesChangedRetirementEvidence(t *testing.T) {
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
func TestAcknowledgeRetiredPublicationRejectsInvalidStoredRetirementEvidence(t *testing.T) {
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

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesGitObservationFailures(t *testing.T) {
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
func TestAcknowledgeRetiredPublicationAcceptsCandidateOnlyPublication(t *testing.T) {
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
func TestAcknowledgeRetiredPublicationPropagatesSnapshotAndPersistenceFailures(t *testing.T) {
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
				ports.persist = func(string, WorktreeMergeRetiredPublicationAcknowledgement) error { return sentinel }
			}
			_, err := acknowledgeRetiredPublication(context.Background(), options, ports)
			if !errors.Is(err, sentinel) {
				t.Fatalf("%s failure = %v", mode, err)
			}
		})
	}
}

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesMalformedExistingAcknowledgement(t *testing.T) {
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

//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesCandidateRaceBeforePersist(t *testing.T) {
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

// TestAcknowledgeRetiredPublicationRacingSidecarIsIdempotentOrRefused drives a
// real sidecar that appears between the caller's existence check and the
// no-replace publication: identical evidence is idempotent, conflicting or
// malformed evidence refuses and keeps the original bytes untouched.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRacingSidecarIsIdempotentOrRefused(t *testing.T) {
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

// TestAcknowledgeRetiredPublicationRefusesMergerReceiptChangedBeforePersist
// changes the receipt after every proof but immediately before publication;
// the final exact-snapshot fence must refuse and leave no acknowledgement.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesMergerReceiptChangedBeforePersist(t *testing.T) {
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

// TestPersistRetiredPublicationAcknowledgementKeepsHeldParentNamespace
// swaps the parent path during publication: the held descriptor is the
// authority, so the evidence cannot be redirected into the replacement
// directory that now carries the caller's path.
func TestPersistRetiredPublicationAcknowledgementKeepsHeldParentNamespace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	held := filepath.Join(root, "evidence")
	if err := os.MkdirAll(held, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "evidence-held")
	inj := &filewrite.Injector{Step: filewrite.StepLink, Hook: func() {
		if err := os.Rename(held, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(held, 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	path := filepath.Join(held, "retired-publication-ack.json")
	if err := persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{ID: "held"}, inj); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "retired-publication-ack.json")); err != nil {
		t.Fatalf("acknowledgement was not published through the held parent: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement followed the swapped parent path: %v", err)
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

// TestAcknowledgeRetiredPublicationPreservesLegacyAcknowledgement retires a
// candidate whose exact receipt already carries an older valid acknowledgement
// without retirement fields: that legacy evidence remains idempotent and its
// original ID/bytes are returned rather than rewritten.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationPreservesLegacyAcknowledgement(t *testing.T) {
	options, ports, receipt, _ := absentRetiredPublicationFixture(t)
	ackPath := retiredPublicationAcknowledgementPath(receipt.ReceiptPath)
	dry := options
	dry.Apply = false
	bound, err := acknowledgeRetiredPublication(context.Background(), dry, ports)
	if err != nil {
		t.Fatal(err)
	}
	legacy := bound
	legacy.RetirementReceiptPath = ""
	legacy.RetirementReceiptSHA256 = ""
	legacy.RetirementProofSHA256 = ""
	legacy.RetirementCanonical = ""
	legacy.RetirementSourceSHA = ""
	legacy.RetirementRetiredRef = ""
	legacy.RetirementArchiveRef = ""
	legacy.RetirementArchiveSHA = ""
	legacy.RetirementPhase = ""
	legacy.ID = retiredPublicationAcknowledgementID(legacy)
	legacyBytes := mustMarshalRetiredPublicationAcknowledgement(t, legacy)
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

// TestAcknowledgeRetiredPublicationRefusesChangedFinalReceiptShape covers the
// last receipt revalidation: the locked digest still matches, but the receipt
// that produced it no longer validates, so publication refuses.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesChangedFinalReceiptShape(t *testing.T) {
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

// TestAcknowledgeRetiredPublicationRefusesTargetResolutionFailure covers the
// fetch's rev-parse boundary through the held session.
//
//nolint:paralleltest // absentRetiredPublicationFixture changes process-wide WB and Git configuration variables.
func TestAcknowledgeRetiredPublicationRefusesTargetResolutionFailure(t *testing.T) {
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
