package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func zeroDeltaConflictFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newEngineFixture(t)
	writeEngineFile(t, filepath.Join(f.canonical, "source.txt"), "source\n")
	runEngineGit(t, f.canonical, "add", "source.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "feat: source already landed")
	sourceSHA := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(f.canonical, "later.txt"), "later\n")
	runEngineGit(t, f.canonical, "add", "later.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "feat: later target")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	targetSHA := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	home, err := wbhome.Root(f.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	r := WorktreeMergeReceipt{
		SchemaVersion: WorktreeMergeSchemaVersion, ID: "zero-delta-test",
		Lane: worktreeMergeLaneID(f.repository.Slug, "main"), Phase: WorktreeMergePhaseLand, Status: WorktreeMergeConflict,
		Repository: f.repository.Slug, Target: "main", TargetSHA: targetSHA, PreviousTargetSHA: targetSHA,
		PublishedCandidateSHA: targetSHA,
		Candidate:             WorktreeMergeCandidate{Task: "gone-candidate", Worktree: filepath.Join(f.githubDir, "gone-candidate"), Branch: "wb/integration/main/gone", SHA: targetSHA},
		Sources:               []WorktreeMergeSource{{Task: "gone-source", Worktree: filepath.Join(f.githubDir, "gone-source"), Branch: "feature/gone-source", SHA: sourceSHA}},
		ReceiptPath:           filepath.Join(home, "reports", "worktree-merge", "zero-delta-test.json"),
	}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return f, r
}

func zeroDeltaContext(t *testing.T, output string) context.Context {
	t.Helper()
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
		want := []string{"pr", "list", "--repo", "acme/app", "--head", "wb/integration/main/gone", "--state", "all", "--json", "number,url,headRefName", "--limit", "1000"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("GitHub query = %q, want exact all-state branch query %q", args, want)
		}
		return []byte(output), nil
	}})
}

func TestZeroDeltaPublicationAcknowledgementFreesNativeGitLane(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := zeroDeltaContext(t, "[]")
	opts := WorktreeMergeAbsorbedConflictAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}
	dry, err := AcknowledgeAbsorbedConflict(ctx, opts)
	if err != nil || len(dry.SourceProofs) != 1 || dry.SourceProofs[0].Method != "ancestor" {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote acknowledgement: %v", err)
	}
	opts.Apply, opts.Actor, opts.Reason = true, "test operator", "verified zero-delta publication"
	ack, err := AcknowledgeAbsorbedConflict(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := hasAbsorbedConflictAcknowledgement(r); err != nil || !acknowledged {
		t.Fatalf("registered acknowledgement = %v, %v", acknowledged, err)
	}
	if got, err := os.ReadFile(r.ReceiptPath); err != nil || !bytes.Equal(before, got) {
		t.Fatalf("historical receipt changed: %v", err)
	}
	if ack.CurrentTargetSHA != r.TargetSHA || ack.SourceProofs[0].SHA != r.Sources[0].SHA {
		t.Fatalf("acknowledgement missed native Git evidence: %+v", ack)
	}
	fresh := createMergeSource(t, f, "fresh-after-zero-delta", "feature/fresh-after-zero-delta", "fresh.txt", "fresh\n")
	prepared, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: f.githubDir, Sources: []string{fresh.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil || prepared.Status != WorktreeMergePrepared {
		t.Fatalf("new prepare after acknowledgement = %+v, %v", prepared, err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesAmbiguousEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*WorktreeMergeReceipt)
		prs  string
		want string
	}{
		{"candidate SHA drift", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = r.Sources[0].SHA }, "[]", "want an unpublished prepare conflict"},
		{"published SHA drift", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.Sources[0].SHA }, "[]", "want an unpublished prepare conflict"},
		{"previous target drift", func(r *WorktreeMergeReceipt) { r.PreviousTargetSHA = r.Sources[0].SHA }, "[]", "want an unpublished prepare conflict"},
		{"recorded PR", func(r *WorktreeMergeReceipt) { r.PullRequest = "https://example.test/pr/1" }, "[]", "want an unpublished prepare conflict"},
		{"recorded landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }, "[]", "want an unpublished prepare conflict"},
		{"unresolved source", func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = strings.Repeat("a", 40) }, "[]", "ancestry"},
		{"historical branch PR", nil, `[{"number":1,"url":"https://example.test/pr/1","headRefName":"wb/integration/main/gone"}]`, "has a pull request"},
		{"null PR lookup", nil, "null", "did not return a list"},
		{"malformed PR lookup", nil, "{broken", "decode all-state pull requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := zeroDeltaConflictFixture(t)
			if tc.edit != nil {
				tc.edit(&r)
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			}
			_, err := AcknowledgeAbsorbedConflict(zeroDeltaContext(t, tc.prs), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
				ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "adversarial fixture",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal wrote acknowledgement: %v", err)
			}
		})
	}
}

func TestZeroDeltaPublicationRecoveryRefusesUnavailablePullRequestLookup(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("provider unavailable")
	}})
	_, err := AcknowledgeAbsorbedConflict(ctx, WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "provider error refusal",
	})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("unavailable PR lookup = %v", err)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable PR lookup wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRequiresGraphAncestryEvenWhenContentMatches(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	runEngineGit(t, f.canonical, "checkout", "-b", "divergent-content", r.Sources[0].SHA)
	writeEngineFile(t, filepath.Join(f.canonical, "later.txt"), "later\n")
	runEngineGit(t, f.canonical, "add", "later.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "feat: duplicate target content on divergent branch")
	r.Sources[0].SHA = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "checkout", "main")
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	_, err := AcknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "divergent source refusal",
	})
	if err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("content-matching divergent source = %v", err)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("divergent source wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesDerivedPathException(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	_, err := AcknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "derived exception refusal",
		DerivedPaths: []string{"spec/generated/README.md"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not permit derived-path exceptions") {
		t.Fatalf("derived-path exception = %v", err)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derived-path exception wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesReappearingBranchesAndWorktrees(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, engineFixture, WorktreeMergeReceipt)
		want    string
	}{
		{"candidate worktree", func(t *testing.T, _ engineFixture, r WorktreeMergeReceipt) {
			if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
				t.Fatal(err)
			}
		}, "candidate worktree"},
		{"source worktree", func(t *testing.T, _ engineFixture, r WorktreeMergeReceipt) {
			if err := os.MkdirAll(r.Sources[0].Worktree, 0700); err != nil {
				t.Fatal(err)
			}
		}, "source worktree"},
		{"candidate local branch", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "branch", r.Candidate.Branch, r.Candidate.SHA)
		}, "local branch"},
		{"source local branch", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "branch", r.Sources[0].Branch, r.Sources[0].SHA)
		}, "local branch"},
		{"candidate remote branch", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "push", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
		}, "branch"},
		{"source remote branch", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "push", "origin", r.Sources[0].SHA+":refs/heads/"+r.Sources[0].Branch)
		}, "branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := zeroDeltaConflictFixture(t)
			tc.prepare(t, f, r)
			_, err := AcknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
				ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "reappearance refusal",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal wrote acknowledgement: %v", err)
			}
		})
	}
}

func TestZeroDeltaPublicationRecoveryRefusesReceiptChangedUnderLaneLock(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	reads := 0
	read := func(path string) (WorktreeMergeReceipt, error) {
		reads++
		if reads == 2 {
			changed := r
			changed.Failure = "changed between admission and lane lock"
			if err := persistWorktreeMergeReceipt(changed); err != nil {
				t.Fatal(err)
			}
		}
		return readWorktreeMergeReceipt(path)
	}
	_, err := acknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "receipt drift refusal",
	}, read, worktreeMergeReceiptSHA256, nil)
	if err == nil || !strings.Contains(err.Error(), "changed immutable evidence") || reads != 2 {
		t.Fatalf("refusal = %v, reads = %d", err, reads)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesReceiptChangedDuringProof(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	reads := 0
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(context.Context, string, ...string) ([]byte, error) {
		reads++
		if reads == 1 {
			changed := r
			changed.Failure = "changed during the first GitHub proof"
			if err := persistWorktreeMergeReceipt(changed); err != nil {
				t.Fatal(err)
			}
		}
		return []byte("[]"), nil
	}})
	_, err := AcknowledgeAbsorbedConflict(ctx, WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "proof time receipt drift refusal",
	})
	if err == nil || !strings.Contains(err.Error(), "changed immutable evidence during recovery proof") || reads != 1 {
		t.Fatalf("proof time receipt drift = %v, reads = %d", err, reads)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proof time receipt drift wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRechecksPullRequestBeforeWrite(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	reads := 0
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) < 2 || args[0] != "pr" || args[1] != "list" {
			t.Fatalf("unexpected GitHub query: %q", args)
		}
		reads++
		if reads == 1 {
			return []byte("[]"), nil
		}
		return []byte(`[{"number":2,"url":"https://example.test/pr/2","headRefName":"wb/integration/main/gone"}]`), nil
	}})
	_, err := AcknowledgeAbsorbedConflict(ctx, WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "late pull request refusal",
	})
	if err == nil || !strings.Contains(err.Error(), "has a pull request") || reads != 2 {
		t.Fatalf("late PR refusal = %v, reads = %d", err, reads)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late PR refusal wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesLateEvidenceChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, engineFixture, WorktreeMergeReceipt)
		want   string
	}{
		{"candidate worktree reappears", func(t *testing.T, _ engineFixture, r WorktreeMergeReceipt) {
			if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
				t.Fatal(err)
			}
		}, "reappeared before acknowledgement"},
		{"local source branch reappears", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "branch", r.Sources[0].Branch, r.Sources[0].SHA)
		}, "local branch"},
		{"remote source branch reappears", func(t *testing.T, f engineFixture, r WorktreeMergeReceipt) {
			runEngineGit(t, f.canonical, "push", "origin", r.Sources[0].SHA+":refs/heads/"+r.Sources[0].Branch)
		}, "branch"},
		{"remote unavailable", func(t *testing.T, f engineFixture, _ WorktreeMergeReceipt) {
			missing := f.repository.CloneURL + ".late-missing"
			if err := os.Rename(f.repository.CloneURL, missing); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Rename(missing, f.repository.CloneURL); err != nil {
					t.Error(err)
				}
			})
		}, "inspect branch"},
		{"target advances", func(t *testing.T, f engineFixture, _ WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(f.canonical, "new-target.txt"), "new target\n")
			runEngineGit(t, f.canonical, "add", "new-target.txt")
			runEngineGit(t, f.canonical, "commit", "-m", "feat: advance target during acknowledgement")
			runEngineGit(t, f.canonical, "push", "origin", "main")
		}, "current target changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := zeroDeltaConflictFixture(t)
			calls := 0
			hash := func(path string) (string, error) {
				calls++
				if calls == 3 {
					tc.change(t, f, r)
				}
				return worktreeMergeReceiptSHA256(path)
			}
			_, err := acknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
				ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "late evidence refusal",
			}, readWorktreeMergeReceipt, hash, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls < 3 {
				t.Fatalf("late evidence refusal = %v, hash calls = %d, want %q", err, calls, tc.want)
			}
			if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("late evidence refusal wrote acknowledgement: %v", err)
			}
		})
	}
}

func TestZeroDeltaPublicationRecoveryRefusesLateReceiptHashFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failureCall  int
		failureValue string
		want         string
	}{
		{"initial read fails", 1, "error", "hash read failed"},
		{"locked read fails", 2, "error", "hash read failed"},
		{"prewrite read fails", 4, "error", "hash read failed"},
		{"prewrite hash drifts", 4, "different", "changed immutable evidence before acknowledgement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := zeroDeltaConflictFixture(t)
			calls := 0
			hash := func(path string) (string, error) {
				calls++
				if calls == tc.failureCall {
					if tc.failureValue == "error" {
						return "", errors.New("hash read failed")
					}
					return strings.Repeat("f", 64), nil
				}
				return worktreeMergeReceiptSHA256(path)
			}
			_, err := acknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
				ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "hash refusal",
			}, readWorktreeMergeReceipt, hash, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != tc.failureCall {
				t.Fatalf("hash refusal = %v, hash calls = %d, want %q", err, calls, tc.want)
			}
		})
	}
}

func TestZeroDeltaPublicationRecoveryRefusesShapeChangedUnderLaneLock(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	reads := 0
	read := func(path string) (WorktreeMergeReceipt, error) {
		reads++
		if reads == 2 {
			changed := r
			changed.Phase = WorktreeMergePhasePrepare
			changed.PublishedCandidateSHA = ""
			return changed, nil
		}
		return readWorktreeMergeReceipt(path)
	}
	_, err := acknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "shape drift refusal",
	}, read, worktreeMergeReceiptSHA256, nil)
	if err == nil || !strings.Contains(err.Error(), "changed recovery shape") || reads != 2 {
		t.Fatalf("shape drift = %v, reads = %d", err, reads)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesLateUninspectableWorktree(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	parent := filepath.Join(f.githubDir, "late-parent")
	r.Candidate.Worktree = filepath.Join(parent, "candidate")
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	calls := 0
	hash := func(path string) (string, error) {
		calls++
		if calls == 3 {
			if err := os.WriteFile(parent, []byte("occupied"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return worktreeMergeReceiptSHA256(path)
	}
	_, err := acknowledgeAbsorbedConflict(zeroDeltaContext(t, "[]"), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "worktree inspection refusal",
	}, readWorktreeMergeReceipt, hash, nil)
	if err == nil || !strings.Contains(err.Error(), "reinspect zero-delta recovery worktree") || calls != 3 {
		t.Fatalf("late uninspectable worktree = %v, hash calls = %d", err, calls)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesCandidateReappearingAfterPrewriteStat(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	receiptHash, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := 0
	resolveAfterReappearance := func(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt) (string, error) {
		resolverCalls++
		if err := os.MkdirAll(receipt.Candidate.Worktree, 0700); err != nil {
			t.Fatal(err)
		}
		return absorbedConflictGitRoot(ctx, projectsRoot, receipt)
	}
	err = recheckZeroDeltaPublicationBeforeWrite(context.Background(), f.githubDir, r, r.ReceiptPath, f.canonical, r.TargetSHA, receiptHash, worktreeMergeReceiptSHA256, resolveAfterReappearance)
	if err == nil || !strings.Contains(err.Error(), "Git root changed before acknowledgement") || resolverCalls != 1 {
		t.Fatalf("candidate reappearance between Stat and resolver = %v, resolver calls = %d", err, resolverCalls)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate reappearance wrote acknowledgement: %v", err)
	}
}

func TestZeroDeltaPublicationRecoveryRefusesTargetDisappearingBeforeWrite(t *testing.T) {
	f, r := zeroDeltaConflictFixture(t)
	reads := 0
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Read: func(context.Context, string, ...string) ([]byte, error) {
		reads++
		if reads == 2 {
			runEngineGit(t, f.repository.CloneURL, "update-ref", "-d", "refs/heads/main")
		}
		return []byte("[]"), nil
	}})
	_, err := AcknowledgeAbsorbedConflict(ctx, WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "test", Reason: "target disappearance refusal",
	})
	if err == nil || !strings.Contains(err.Error(), "target") || reads != 2 {
		t.Fatalf("target disappearance = %v, GitHub reads = %d", err, reads)
	}
	if _, err := os.Stat(absorbedConflictAcknowledgementPath(r.ReceiptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disappeared target wrote acknowledgement: %v", err)
	}
}

func TestAbsorbedConflictValidationRejectsMalformedRetirementEvidence(t *testing.T) {
	_, r := zeroDeltaConflictFixture(t)
	r.Phase = WorktreeMergePhasePrepare
	r.PullRequest = "https://example.test/pr/1"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retiredPublicationAcknowledgementPath(r.ReceiptPath), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	err := validateAbsorbedConflictReceipt(r, r.ReceiptPath)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed retirement sidecar = %v", err)
	}
}
