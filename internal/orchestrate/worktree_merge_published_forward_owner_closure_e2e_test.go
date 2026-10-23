//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestE2EPublishedForwardOwnerEntryAndActualLockRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"expectation", "read", "eligibility", "lock"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			if stage == "expectation" {
				if _, err := PreparePublishedValidationFailureForwardRepair(t.Context(), WorktreeMergePublishedForwardRepairOptions{}); err == nil {
					t.Fatal("unpinned owner accepted")
				}
				return
			}
			f, r, _, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			want := ""
			switch stage {
			case "read":
				before, err := os.ReadFile(r.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(r.ReceiptPath, before, 0o600); err != nil {
						t.Error(err)
					}
				})
				if err := os.WriteFile(r.ReceiptPath, []byte("{malformed native receipt"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "decode merge receipt " + r.ReceiptPath + ": "
			case "eligibility":
				r.Status = WorktreeMergeComplete
				forwardClosureWriteRecord(t, r.ReceiptPath, r)
				want = "validation_failed"
			case "lock":
				lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := lock.Release(); err != nil {
						t.Error(err)
					}
				})
			}
			got, err := PreparePublishedValidationFailureForwardRepair(t.Context(), o)
			if err == nil || want != "" && !strings.Contains(err.Error(), want) {
				t.Fatalf("entry %s: %v", stage, err)
			}
			if stage == "read" {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) || !reflect.DeepEqual(got, WorktreeMergePublishedForwardRepair{}) {
					t.Fatalf("native decode type/zero-result contract: %+v %v", got, err)
				}
			}
			assertNoPublishedForwardRepairCandidate(t, f, r, o)
		})
	}
}

func TestE2EPublishedForwardEvidenceExactReadHashAndBindingRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"read", "eligibility", "receipt hash error", "receipt digest", "candidate custody", "claim read", "claim digest", "self shape", "supersession hash error", "supersession digest", "correction binding", "correction hash", "source pins", "fetch", "source repository"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			sentinel := errors.New("selected native evidence observation refusal")
			consumed := false
			run := defaultRunner
			hash := worktreeMergeReceiptSHA256
			read := os.ReadFile
			want := ""
			wantIdentity := false
			claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.githubDir, r, r.Candidate)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{r.ReceiptPath, claim.ClaimPath, s.AcknowledgementPath}
			verify := forwardClosurePinnedBytes(t, paths...)
			switch stage {
			case "read":
				o.Receipt = filepath.Join(t.TempDir(), "missing")
				r.ReceiptPath = o.Receipt
				wantIdentity = true
				sentinel = os.ErrNotExist
			case "eligibility":
				r.Status = WorktreeMergeComplete
				forwardClosureWriteRecord(t, r.ReceiptPath, r)
				want = "validation_failed"
			case "receipt hash error", "supersession hash error", "correction hash":
				target := r.ReceiptPath
				if stage == "supersession hash error" {
					target = s.AcknowledgementPath
				}
				if stage == "correction hash" {
					c := forwardClosureCorrection(t, r, s, o)
					if err := persistSelfSupersessionCorrection(c.CorrectionPath, c); err != nil {
						t.Fatal(err)
					}
					target = c.CorrectionPath
				}
				hash = func(path string) (string, error) {
					if path == target {
						consumed = true
						return "", sentinel
					}
					return worktreeMergeReceiptSHA256(path)
				}
				wantIdentity = true
			case "receipt digest":
				o.ExpectedReceiptSHA256 = strings.Repeat("a", 64)
				want = "receipt SHA256"
			case "candidate custody":
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) {
						consumed = true
						return sentinel
					}
					return nil
				}}
				want = "validate failed candidate"
				wantIdentity = true
			case "claim read":
				read = func(path string) ([]byte, error) {
					if path == claim.ClaimPath {
						consumed = true
						return nil, sentinel
					}
					return os.ReadFile(path)
				}
				wantIdentity = true
				want = "read immutable failed candidate claim"
			case "claim digest":
				o.ExpectedImmutableClaimSHA256 = strings.Repeat("a", 64)
				want = "immutable claim SHA256"
			case "self shape":
				s.Replacement.Task += "-different"
				s.ID = validationFailureSupersessionID(s)
				forwardClosureWriteRecord(t, s.AcknowledgementPath, s)
				want = "exact self-supersession repair shape"
			case "supersession digest":
				o.ExpectedSupersessionSHA256 = strings.Repeat("a", 64)
				want = "supersession SHA256"
			case "correction binding":
				c := forwardClosureCorrection(t, r, s, o)
				c.ImmutableClaimSHA256 = strings.Repeat("a", 64)
				c.ID = selfSupersessionCorrectionID(c)
				if err := persistSelfSupersessionCorrection(c.CorrectionPath, c); err != nil {
					t.Fatal(err)
				}
				want = "does not retain immutable"
			case "source pins":
				o.ExpectedSourceSHAs[1] = strings.Repeat("a", 40)
				want = "does not match expected"
			case "fetch":
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}) {
						consumed = true
						return sentinel
					}
					return nil
				}}
				wantIdentity = true
				want = "fetch exact remote target"
			case "source repository":
				other := f
				other.canonical = filepath.Join(f.githubDir, "acme", "other")
				other.repository.Slug = "acme/other"
				other.repository.Path = other.canonical
				runEngineGit(t, filepath.Dir(other.canonical), "clone", "--no-hardlinks", f.repository.CloneURL, other.canonical)
				runEngineGit(t, other.canonical, "config", "user.name", "WB Test")
				runEngineGit(t, other.canonical, "config", "user.email", "wb@example.test")
				source := createMergeSource(t, other, "other-forward-source", "feature/other-forward-source", "other.txt", "other\n")
				o.Sources = []string{source.WorktreeDir}
				o.ExpectedSourceSHAs = []string{strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))}
				want = "repair sources belong to"
			}
			_, err = inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, run, hash, read)
			if err == nil || want != "" && !strings.Contains(err.Error(), want) {
				t.Fatalf("evidence %s: %v want %s", stage, err, want)
			}
			if wantIdentity && !errors.Is(err, sentinel) {
				t.Fatalf("selected primary identity lost: %v", err)
			}
			if wantIdentity && stage != "read" && !consumed {
				t.Fatal("selected late observation not consumed")
			}
			if stage != "eligibility" && stage != "self shape" && stage != "read" {
				verify()
			}
			assertNoPublishedForwardRepairCandidate(t, f, r, o)
		})
	}
}

func TestE2EPublishedForwardOwnerNativePostCreateRefusalsAndAbort(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"replacement clean", "replacement HEAD", "merge required root", "final HEAD", "final clean", "abort secondary", "final ancestor error", "final ancestor false"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			verify := forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
			evidence, err := inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("selected real candidate observation refusal")
			consumed := false
			candidateDir := ""
			headQueries := 0
			cleanQueries := 0
			final := false
			finalHead := ""
			finalMatches := 0
			var restoreReplacement func()
			unique := make(map[string]bool)
			for _, root := range evidence.roots {
				if root.SHA != "" {
					unique[root.SHA] = true
				}
			}
			run := forwardClosureRunner{Runner: defaultRunner, observe: func(ctx context.Context, dir, name string, args []string) error {
				if name != "git" || dir == r.Candidate.Worktree || dir == f.canonical {
					return nil
				}
				for _, source := range o.Sources {
					if dir == source {
						return nil
					}
				}
				// The new managed candidate is discovered by an authentic native read;
				// no positive Create result is supplied by this observation.
				if candidateDir == "" {
					candidateDir = dir
				}
				if dir != candidateDir {
					return nil
				}
				if reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) {
					cleanQueries++
				}
				if reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) {
					headQueries++
				}
				selected := false
				switch stage {
				case "replacement clean":
					selected = cleanQueries == 1 && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
				case "replacement HEAD":
					selected = headQueries == 1 && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
				case "merge required root":
					selected = len(args) == 3 && args[0] == "merge" && args[1] == "--no-edit" && args[2] == evidence.roots[0].SHA
				case "final HEAD":
					selected = headQueries == len(unique)+2 && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
				case "final clean", "abort secondary":
					selected = cleanQueries == 2 && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
				case "final ancestor error", "final ancestor false":
					selected = final && finalHead != "" && reflect.DeepEqual(args, []string{"merge-base", evidence.roots[0].SHA, finalHead})
					if selected {
						finalMatches++
					}
				}
				if !selected {
					return nil
				}
				consumed = true
				if stage == "abort secondary" {
					view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: dir})
					if err != nil || view.Claim == nil {
						t.Fatalf("actual candidate claim: %+v %v", view, err)
					}
					path := view.Claim.ClaimPath
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.WriteFile(path, before, 0o600); err != nil {
							t.Error(err)
							return
						}
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if _, err := worktrees.Abort(cleanupCtx, worktrees.AbortOptions{ProjectsRoot: f.githubDir, Task: o.RepairTask(), Base: r.Target, Apply: true, Disposition: worktrees.AbortDiscarded, DeleteRemote: true, All: true}); err != nil {
							t.Error(err)
						}
					})
					if err := os.WriteFile(path, []byte("not JSON\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "final ancestor false" {
					// Replace only the newly created HEAD with a real same-tree descendant
					// of the target. The required current source root is then truthfully absent.
					head := args[2]
					tree := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", head+"^{tree}"))
					replacement := strings.TrimSpace(runEngineGit(t, dir, "commit-tree", tree, "-p", r.TargetSHA, "-m", "test: native final-root interpretation"))
					restoreReplacement = forwardClosureNativeReplacement(t, f.canonical, head, replacement, evidence.roots[0].SHA)
					return nil
				}
				return sentinel
			}}
			run.after = func(_ context.Context, dir, name string, args []string, result runner.Result, queryErr error) {
				if stage == "final ancestor false" && restoreReplacement != nil && dir == candidateDir && name == "git" && reflect.DeepEqual(args, []string{"merge-base", evidence.roots[0].SHA, finalHead}) {
					if queryErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) == evidence.roots[0].SHA || strings.TrimSpace(result.CombinedOutput) == "" {
						t.Fatalf("delegated connected native false: %+v %v", result, queryErr)
					}
					restoreReplacement()
					restoreReplacement = nil
				}
			}
			got, err := preparePublishedValidationFailureForwardRepair(t.Context(), o, run, worktreeMergeReceiptSHA256, os.ReadFile, nil, func() {
				finalHead = strings.TrimSpace(runEngineGit(t, candidateDir, "rev-parse", "--verify", "HEAD^{commit}"))
				if finalHead == "" {
					t.Fatal("native final HEAD absent")
				}
				final = true
			})
			if strings.HasPrefix(stage, "final ancestor") && finalMatches != 1 {
				t.Fatalf("exact final pair consumed %d times", finalMatches)
			}
			if !consumed || err == nil {
				t.Fatalf("post-create %s: %+v %v consumed=%v HEADs=%d clean=%d", stage, got, err, consumed, headQueries, cleanQueries)
			}
			if stage == "final ancestor false" {
				if !strings.Contains(err.Error(), "does not contain required") {
					t.Fatalf("successful-false native diagnostic: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("selected primary identity lost: %v", err)
			}
			if stage == "abort secondary" {
				if !strings.Contains(err.Error(), "retire invalid published forward-repair candidate") {
					t.Fatalf("actual Abort secondary failure missing: %v", err)
				}
			} else {
				assertNoPublishedForwardRepairCandidate(t, f, r, o)
			}
			verify()
			forwardClosureAssertReleased(t, f.githubDir, r.Lane)
		})
	}
}

func TestE2EPublishedForwardEvidenceAdvancedTargetNativeAncestry(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"descendant", "unrelated", "query error", "revalidation false"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			base := newExplicitRootEngineFixture(t)
			parent := strings.TrimSpace(runEngineGit(t, base.canonical, "rev-parse", "HEAD"))
			writeEngineFile(t, filepath.Join(base.canonical, "target-parent.txt"), "native target T1\n")
			runEngineGit(t, base.canonical, "add", "target-parent.txt")
			runEngineGit(t, base.canonical, "commit", "-m", "test: native acknowledged target descendant")
			runEngineGit(t, base.canonical, "push", "origin", "main")
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, base)
			verify := forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
			targetParent := s.CurrentTargetSHA
			if stage == "unrelated" {
				targetParent = parent
			}
			tree := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", s.CurrentTargetSHA+"^{tree}"))
			advanced := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", tree, "-p", targetParent, "-m", "test: current target evidence"))
			runEngineGit(t, f.canonical, "push", "--force", "origin", advanced+":refs/heads/main")
			o.ExpectedCurrentTargetSHA = advanced
			sentinel := errors.New("selected native advanced target query refusal")
			consumed := false
			run := defaultRunner
			if stage == "query error" {
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", s.CurrentTargetSHA, advanced}) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			}
			evidence, err := inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, run, worktreeMergeReceiptSHA256, os.ReadFile)
			if stage == "unrelated" || stage == "query error" {
				if err == nil || !strings.Contains(err.Error(), "does not descend from self-supersession target") {
					t.Fatalf("native target %s refusal: %v", stage, err)
				}
				if stage == "query error" && !consumed {
					t.Fatal("exact native query not consumed")
				}
				verify()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage == "revalidation false" {
				var restoreReplacement func()
				replacement := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", tree, "-p", parent, "-m", "test: native late target interpretation"))
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if !consumed && dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", s.CurrentTargetSHA, advanced}) {
						consumed = true
						restoreReplacement = forwardClosureNativeReplacement(t, f.canonical, advanced, replacement, s.CurrentTargetSHA)
					}
					return nil
				}, after: func(_ context.Context, dir, name string, args []string, result runner.Result, queryErr error) {
					if restoreReplacement != nil && dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", s.CurrentTargetSHA, advanced}) {
						if queryErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) == s.CurrentTargetSHA || strings.TrimSpace(result.CombinedOutput) == "" {
							t.Fatalf("actual connected target false: %+v %v", result, queryErr)
						}
						restoreReplacement()
						restoreReplacement = nil
					}
				}}
			}
			err = revalidatePublishedForwardRepairEvidence(t.Context(), o, run, worktreeMergeReceiptSHA256, os.ReadFile, r.ReceiptPath, evidence.receiptHash, evidence.claimHash, evidence.supersessionHash, evidence.correctionHash, evidence.sources, evidence.repository, evidence.canonical, evidence.currentTarget)
			if stage == "descendant" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !consumed || err == nil || !strings.Contains(err.Error(), "no longer descends") {
				t.Fatalf("native late false: %v consumed=%v", err, consumed)
			}
			verify()
			assertNoPublishedForwardRepairCandidate(t, f, r, o)
		})
	}
}

func TestE2EPublishedForwardRevalidationKeepsNativeCustodyAndCorrectionOrdering(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"candidate custody", "new correction", "correction binding", "historical source", "inactive source"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			e, err := inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile)
			if err != nil {
				t.Fatal(err)
			}
			run := defaultRunner
			hash := worktreeMergeReceiptSHA256
			sentinel := errors.New("selected native revalidation refusal")
			consumed := false
			want := ""
			correctionHash := ""
			switch stage {
			case "candidate custody":
				want = "failed candidate changed"
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			case "new correction", "correction binding":
				c := forwardClosureCorrection(t, r, s, o)
				if stage == "correction binding" {
					c.ImmutableClaimSHA256 = strings.Repeat("a", 64)
					c.ID = selfSupersessionCorrectionID(c)
					want = "does not retain immutable"
				} else {
					want = "was corrected during"
				}
				if err := persistSelfSupersessionCorrection(c.CorrectionPath, c); err != nil {
					t.Fatal(err)
				}
				if stage == "correction binding" {
					correctionHash, err = worktreeMergeReceiptSHA256(c.CorrectionPath)
					if err != nil {
						t.Fatal(err)
					}
				}
			case "historical source":
				want = "revalidate immutable historical source evidence"
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == e.canonical && name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", r.Candidate.SHA + "^{commit}"}) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			case "inactive source":
				view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: o.Sources[1]})
				if err != nil || view.Claim == nil {
					t.Fatalf("native source claim: %v", err)
				}
				var claim map[string]any
				bytes, err := os.ReadFile(view.Claim.ClaimPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(bytes, &claim); err != nil {
					t.Fatal(err)
				}
				claim["lifecycle"] = "terminal"
				forwardClosureWriteRecord(t, view.Claim.ClaimPath, claim)
				want = "no authoritative active Work Log claim"
			}
			err = revalidatePublishedForwardRepairEvidence(t.Context(), o, run, hash, os.ReadFile, r.ReceiptPath, e.receiptHash, e.claimHash, e.supersessionHash, correctionHash, e.sources, e.repository, e.canonical, e.currentTarget)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("revalidation %s: %v want %s", stage, err, want)
			}
			if stage == "candidate custody" || stage == "historical source" {
				if !consumed || !errors.Is(err, sentinel) {
					t.Fatalf("native selected identity: %v consumed=%v", err, consumed)
				}
			}
			assertNoPublishedForwardRepairCandidate(t, f, r, o)
		})
	}
}

// The initial native source claim and HEAD are accepted before the second
// native Work Log observation refuses the physically terminalized private claim.
func TestE2EPublishedForwardSourceSecondNativeClaimObservationRefusesTerminalClaim(t *testing.T) {
	t.Parallel()
	f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
	verify := forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
	source := o.Sources[len(o.Sources)-1]
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: source})
	if err != nil || view.Claim == nil || view.Claim.Lifecycle != "active" {
		t.Fatalf("actual initial active source claim: %+v %v", view, err)
	}
	path := view.Claim.ClaimPath
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var claim map[string]any
	if err := json.Unmarshal(before, &claim); err != nil {
		t.Fatal(err)
	}
	claim["lifecycle"] = "terminal"
	restored := false
	restore := func() {
		if restored {
			return
		}
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		restored = true
	}
	t.Cleanup(func() {
		if !restored {
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	consumed := 0
	run := forwardClosureRunner{Runner: defaultRunner, observe: func(context.Context, string, string, []string) error { return nil }, after: func(_ context.Context, dir, name string, args []string, result runner.Result, queryErr error) {
		if dir != source || name != "git" || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) {
			return
		}
		if queryErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != o.ExpectedSourceSHAs[len(o.ExpectedSourceSHAs)-1] {
			t.Fatalf("actual accepted source HEAD observation: %+v %v", result, queryErr)
		}
		consumed++
		if consumed != 1 {
			t.Fatal("native terminalization stage consumed more than once")
		}
		forwardClosureWriteRecord(t, path, claim)
	}}
	_, err = inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, run, worktreeMergeReceiptSHA256, os.ReadFile)
	if consumed != 1 || err == nil || !strings.Contains(err.Error(), "repair source "+source+" has no exact active Work Log claim") {
		t.Fatalf("second native claim refusal: %v consumed=%d", err, consumed)
	}
	restore()
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("actual source claim restoration: %v", readErr)
	}
	verify()
	assertNoPublishedForwardRepairCandidate(t, f, r, o)
}

//nolint:paralleltest // Real per-test XDG_CONFIG_HOME and TMPDIR changes are process-wide; private native fixtures and child rows remain sequential.
func TestE2EPublishedForwardOwnerActualListAndPromptFilesystemRefusals(t *testing.T) {
	for _, stage := range []string{"configured list root", "scratch prompt"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2EPublishedForwardOwnerActualListAndPromptFilesystemRefusals; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			verify := forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
			consumed := false
			blocker := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(blocker, []byte("private blocker"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			run := defaultRunner
			if stage == "configured list root" {
				blocker := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(blocker, []byte("private blocker"), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(configHome, "wb", "worktrees.yaml")
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(_ context.Context, dir, name string, args []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}) {
						calls++
						if calls == 2 {
							consumed = true
							t.Cleanup(func() {
								if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
									t.Error(err)
								}
							})
							writeEngineFile(t, path, fmt.Sprintf("version: 1\nworktrees:\n  root: %q\n", blocker))
						}
					}
					return nil
				}}
			}
			if stage == "scratch prompt" {
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(context.Context, string, string, []string) error { return nil }, after: func(_ context.Context, dir, name string, args []string, result runner.Result, queryErr error) {
					if dir != r.Candidate.Worktree || name != "git" || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}) {
						return
					}
					calls++
					if calls != 2 {
						return
					}
					if queryErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != o.ExpectedCurrentTargetSHA {
						t.Fatalf("native prompt-stage target observation: %+v %v", result, queryErr)
					}
					consumed = true
					// Cover each platform's actual os.TempDir environment policy.
					for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
						t.Setenv(key, blocker)
					}
				}}
			}
			_, err := preparePublishedValidationFailureForwardRepair(t.Context(), o, run, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			if err == nil {
				t.Fatal("native filesystem refusal did not reach owner")
			}
			if stage == "configured list root" {
				if !consumed || !strings.Contains(err.Error(), "inspect published forward-repair worktree") || !strings.Contains(err.Error(), "read worktree tasks") {
					t.Fatalf("actual fatal List boundary: %v consumed=%v calls=%d", err, consumed, calls)
				}
				if err := os.Remove(filepath.Join(configHome, "wb", "worktrees.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "scratch prompt" {
				var pathErr *os.PathError
				if !consumed || calls != 2 || !errors.As(err, &pathErr) || filepath.Dir(pathErr.Path) != blocker || !strings.HasPrefix(filepath.Base(pathErr.Path), "wb-published-forward-repair-prompt-") {
					t.Fatalf("actual prompt CreateTemp boundary: %v consumed=%v calls=%d", err, consumed, calls)
				}
				// Later native no-candidate verification must have usable scratch IO.
				for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(key, configHome)
				}
			}
			verify()
			assertNoPublishedForwardRepairCandidate(t, f, r, o)
			forwardClosureAssertReleased(t, f.githubDir, r.Lane)
		})
	}
}

func TestE2EPublishedForwardOwnerNativeListCreationAndTargetIdentity(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"multiple worktrees", "native create refusal", "target drift during create", "self replacement"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r, s, o := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			verify := forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
			consumed := false
			run := defaultRunner
			want := ""
			switch stage {
			case "multiple worktrees":
				other := filepath.Join(f.githubDir, "acme", "other")
				runEngineGit(t, filepath.Dir(other), "clone", "--no-hardlinks", f.repository.CloneURL, other)
				runEngineGit(t, other, "config", "user.name", "WB Test")
				runEngineGit(t, other, "config", "user.email", "wb@example.test")
				prompt := filepath.Join(t.TempDir(), "prompt.txt")
				if err := os.WriteFile(prompt, []byte("native multi-repository inventory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				branch := "wb/recovery/" + r.Target + "/" + mergeOperationSuffix(o.RepairTask()) + "-published-forward-repair"
				created, err := worktrees.Create(t.Context(), []string{r.Repository, "acme/other"}, worktrees.CreateOptions{ProjectsRoot: f.githubDir, Operation: o.RepairTask(), Base: r.Target, Branch: branch, BranchChosen: true, WorkLog: worktrees.WorkLogOptions{AgentRuntime: "test", Model: "test-model", OriginalPrompt: prompt, RequireOriginalPrompt: true}})
				if err != nil || len(created) != 2 {
					t.Fatalf("real two-repository setup: %+v %v", created, err)
				}
				listed, err := worktrees.List(t.Context(), worktrees.ListOptions{ProjectsRoot: f.githubDir, Task: o.RepairTask(), Base: r.Target, Workers: 1})
				if err != nil || len(listed) != 2 {
					t.Fatalf("real multiple inventory: %+v %v", listed, err)
				}
				want = "resolves to 2 worktrees"
			case "native create refusal":
				o.SessionRequired = true
				want = "create published forward-repair worktree"
			case "target drift during create":
				count := 0
				run = forwardClosureRunner{Runner: defaultRunner, observe: func(context.Context, string, string, []string) error { return nil }, after: func(_ context.Context, dir, name string, args []string, result runner.Result, queryErr error) {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}) {
						count++
						if count == 2 {
							if queryErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != o.ExpectedCurrentTargetSHA {
								t.Fatalf("authentic precreation target observation: %+v %v", result, queryErr)
							}
							consumed = true
							writeEngineFile(t, filepath.Join(f.canonical, "create-drift.txt"), "native late target advance\n")
							runEngineGit(t, f.canonical, "add", "create-drift.txt")
							runEngineGit(t, f.canonical, "commit", "-m", "test: target changed after exact native observation")
							runEngineGit(t, f.canonical, "push", "origin", "main")
						}
					}
				}}
				want = "identity drifted while creating"
			case "self replacement":
				// This is historical self-supersession input. The current public
				// supersession owner deliberately refuses self replacement.
				// Retain every native claim/receipt/source; qualify only its earlier
				// target observation to the actual recorded base before publication.
				s.CurrentTargetSHA = r.TargetSHA
				s.ID = validationFailureSupersessionID(s)
				if err := persistValidationFailureSupersession(s.AcknowledgementPath, s); err != nil {
					t.Fatal(err)
				}
				runEngineGit(t, r.Candidate.Worktree, "push", "--force", "origin", r.Candidate.SHA+":refs/heads/main")
				if contains, err := isMergeAncestorWithRunner(t.Context(), defaultRunner, r.Candidate.Worktree, r.TargetSHA, r.Candidate.SHA); err != nil || !contains {
					t.Fatalf("native recorded base to published candidate: %v %v", contains, err)
				}
				o.ExpectedCurrentTargetSHA = r.Candidate.SHA
				var err error
				o.ExpectedSupersessionSHA256, err = worktreeMergeReceiptSHA256(s.AcknowledgementPath)
				if err != nil {
					t.Fatal(err)
				}
				evidence, err := inspectPublishedForwardRepairEvidence(t.Context(), o, r.ReceiptPath, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile)
				if err != nil || evidence.currentTarget != r.Candidate.SHA || evidence.supersession.CurrentTargetSHA != r.TargetSHA {
					t.Fatalf("native historical input preflight: %+v %v", evidence, err)
				}
				// The historical sidecar qualification is declared fixture setup;
				// all owner-side writes remain forbidden from this exact baseline.
				verify = forwardClosurePinnedBytes(t, r.ReceiptPath, s.AcknowledgementPath)
				want = "unexpected identity or self replacement"
			}
			got, err := preparePublishedValidationFailureForwardRepair(t.Context(), o, run, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native creation %s: %+v %v want %s", stage, got, err, want)
			}
			if stage == "target drift during create" && !consumed {
				t.Fatal("exact post-fetch native target mutation not consumed")
			}
			if stage != "multiple worktrees" {
				assertNoPublishedForwardRepairCandidate(t, f, r, o)
			}
			verify()
			forwardClosureAssertReleased(t, f.githubDir, r.Lane)
		})
	}
}
