//go:build e2e

package orchestrate

import (
	"bytes"
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

func TestE2ESealOwnerNativeObservationRefusalsRetainHistoricalAuthority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, stage, want string
		advanced          bool
	}{
		{"candidate clean", "original status 1", "validate failed candidate", false},
		{"initial target fetch", "original fetch 1", "fetch exact remote target", false},
		{"target tree", "original tree 1", "read current target tree", false},
		{"source head", "source head 1", "read receipted source", false},
		{"advanced source tree", "source tree 1", "read advanced receipted source tree", true},
		{"receipt hash", "hash", "", false},
		{"replacement custody", "replacement status 1", "replacement is not clean", false},
		{"replacement head", "replacement head 1", "read replacement HEAD", false},
		{"before tree", "replacement tree 1", "", false},
		{"initial ancestry", "replacement ancestry 1", "", false},
		{"ours merge", "ours", "create no-content ancestry seal", false},
		{"post merge clean", "replacement status 2", "ancestry seal is not clean", false},
		{"final head", "replacement head 2", "", false},
		{"final tree", "replacement tree 2", "", false},
		{"final ancestry", "final ancestry", "", false},
		{"late original custody", "original status 2", "failed candidate changed while sealing", false},
		{"late source", "source head 2", "receipted source changed while sealing", false},
		{"final target fetch", "replacement fetch 1", "fetch exact remote target", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, r, source := sealOwnerFixture(t)
			if tc.advanced {
				sealOwnerAdvanceSource(t, f, r, source)
			}
			receiptBytes, candidateBytes, sourceBytes := validationFailureSealImmutableBytes(t, f, r, source)
			sentinel := errors.New("selected " + tc.stage)
			counts := map[string]int{}
			consumed := 0
			abort := 0
			replacementDir := ""
			finalHeadRead := false
			run := &sealOwnerRunner{Runner: defaultRunner}
			run.before = func(_ context.Context, dir, name string, args []string) error {
				if name != "git" {
					return nil
				}
				label := ""
				switch dir {
				case r.Candidate.Worktree:
					label = "original"
				case source.WorktreeDir:
					label = "source"
				default:
					if dir != f.canonical && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) && replacementDir == "" {
						replacementDir = dir
					}
					if dir == replacementDir {
						label = "replacement"
					}
				}
				kind := ""
				switch {
				case reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}):
					kind = "status"
				case reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}):
					kind = "head"
				case len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && strings.HasSuffix(args[2], "^{tree}"):
					kind = "tree"
				case reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}):
					kind = "fetch"
				case len(args) == 3 && args[0] == "merge-base":
					kind = "ancestry"
				}
				key := label + " " + kind
				if label != "" && kind != "" {
					counts[key]++
				}
				stage := ""
				if kind != "" {
					stage = key + " " + fmtSealOrdinal(counts[key])
				}
				if label == "replacement" && kind == "head" && counts[key] == 2 {
					finalHeadRead = true
				}
				if name == "git" && len(args) > 4 && reflect.DeepEqual(args[:5], []string{"merge", "--strategy=ours", "--no-edit", "--message", "chore(wb): seal validation-failure ancestry"}) {
					stage = "ours"
				}
				if label == "replacement" && kind == "ancestry" && finalHeadRead {
					stage = "final ancestry"
				}
				if reflect.DeepEqual(args, []string{"merge", "--abort"}) {
					abort++
				}
				if stage == tc.stage && consumed == 0 {
					consumed++
					return sentinel
				}
				return nil
			}
			hash := worktreeMergeReceiptSHA256
			if tc.stage == "hash" {
				hash = func(path string) (string, error) {
					if path != r.ReceiptPath {
						t.Fatal("hash path")
					}
					consumed++
					return "", sentinel
				}
			}
			_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), run, readWorktreeMergeReceipt, hash)
			if !errors.Is(err, sentinel) || consumed != 1 || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("stage=%s consumed=%d counts=%v err=%v", tc.stage, consumed, counts, err)
			}
			if tc.stage == "ours" && abort != 1 {
				t.Fatalf("real best-effort merge abort count %d", abort)
			}
			if replacementDir != "" {
				if _, err := os.Stat(replacementDir); err != nil {
					t.Fatalf("managed recovery candidate not retained: %v", err)
				}
			}
			sealOwnerAssertReleased(t, f, r)
			assertValidationFailureSealImmutableBytes(t, f, r, source, receiptBytes, candidateBytes, sourceBytes)
		})
	}
}

func TestE2ESealOwnerSourceProofAndDescendantTreeContracts(t *testing.T) {
	t.Parallel()
	t.Run("real dirty source", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		writeEngineFile(t, filepath.Join(source.WorktreeDir, "dirty.txt"), "dirty\n")
		_, err := validateValidationFailureSealSource(t.Context(), defaultRunner, f.githubDir, r, r.Sources[0], r.TargetSHA)
		if err == nil || !strings.Contains(err.Error(), "is not clean") {
			t.Fatalf("source custody %v", err)
		}
	})
	t.Run("real differing descendant tree", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		writeEngineFile(t, filepath.Join(source.WorktreeDir, "different.txt"), "different\n")
		runEngineGit(t, source.WorktreeDir, "add", "different.txt")
		runEngineGit(t, source.WorktreeDir, "commit", "-m", "test: descendant different tree")
		tree := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD^{tree}"))
		_, err := validateValidationFailureSealSource(t.Context(), defaultRunner, f.githubDir, r, r.Sources[0], tree)
		if err == nil || !strings.Contains(err.Error(), "differs from landed target tree") {
			t.Fatalf("descendant tree %v", err)
		}
	})
	t.Run("actual same-tree descendant seal", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		head := sealOwnerAdvanceSource(t, f, r, source)
		before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
		result, err := PrepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r))
		if err != nil || result.Status != "validation_failure_seal_prepared" {
			t.Fatalf("native seal %+v %v", result, err)
		}
		found := false
		for _, root := range result.RequiredRoots {
			if root.Kind == "landed_source_descendant:"+r.Sources[0].Task && root.SHA == head {
				found = true
			}
			if contains, err := isMergeAncestor(t.Context(), result.Candidate.Worktree, root.SHA, result.Candidate.SHA); err != nil || !contains {
				t.Fatalf("native required root %+v %t %v", root, contains, err)
			}
		}
		if !found {
			t.Fatal("observed native descendant not retained")
		}
		if got := strings.TrimSpace(runEngineGit(t, result.Candidate.Worktree, "rev-parse", "HEAD^{tree}")); got != result.TargetTreeSHA {
			t.Fatalf("seal tree %s != %s", got, result.TargetTreeSHA)
		}
		assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
		sealOwnerAssertReleased(t, f, r)
	})
}

func TestE2ESealOwnerPhysicalTemporalRefusalsPreserveRecords(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"target during create", "before tree", "dirty after merge", "final tree", "late candidate", "source advanced", "source rewound", "late target", "final false ancestry"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, r, source := sealOwnerFixture(t)
			advanced := name == "source advanced" || name == "source rewound"
			observed := ""
			if advanced {
				observed = sealOwnerAdvanceSource(t, f, r, source)
			}
			receiptBytes, candidateBytes, sourceBytes := validationFailureSealImmutableBytes(t, f, r, source)
			replacementDir := ""
			counts := map[string]int{}
			consumed := 0
			want := ""
			restore := func() {}
			run := &sealOwnerRunner{Runner: defaultRunner}
			mutateFile := func(dir string, commit bool) {
				writeEngineFile(t, filepath.Join(dir, "seal-observation.txt"), "physical late mutation\n")
				if commit {
					runEngineGit(t, dir, "add", "seal-observation.txt")
					runEngineGit(t, dir, "commit", "-m", "test: physical late tree mutation")
				}
			}
			mutateTarget := func() {
				runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "test: target temporal mutation")
				runEngineGit(t, f.canonical, "push", "origin", "main")
			}
			sourceRestored := false
			sourceRestore := func() {
				if sourceRestored {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, _, err := runCommand(cleanupCtx, defaultRunner, 0, 0, source.WorktreeDir, "git", "reset", "--hard", observed); err != nil {
					t.Error(err)
					return
				}
				if got, err := mergeRevision(cleanupCtx, defaultRunner, source.WorktreeDir, "HEAD"); err != nil || got != observed {
					t.Errorf("restored native source HEAD %s want %s err=%v", got, observed, err)
					return
				}
				sourceRestored = true
			}
			run.before = func(ctx context.Context, dir, program string, args []string) error {
				if program != "git" {
					return nil
				}
				if replacementDir == "" && dir != r.Candidate.Worktree && dir != source.WorktreeDir && dir != f.canonical && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) {
					replacementDir = dir
				}
				if dir != replacementDir {
					return nil
				}
				key := strings.Join(args, " ")
				counts[key]++
				if consumed == 0 && name == "before tree" && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) && counts[key] == 1 {
					consumed++
					mutateFile(dir, true)
					want = "differs from fetched target tree"
				}
				if consumed == 0 && name == "dirty after merge" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) && counts[key] == 2 {
					consumed++
					mutateFile(dir, false)
					want = "ancestry seal is not clean"
				}
				if consumed == 0 && name == "final false ancestry" && len(args) == 3 && args[0] == "merge-base" && args[1] == r.Sources[0].SHA && counts["rev-parse --verify HEAD^{commit}"] == 2 {
					consumed++
					want = "does not contain required receipted_source"
					head := args[2]
					tree := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", head+"^{tree}"))
					target := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "origin/main"))
					if contains, err := isMergeAncestor(ctx, dir, args[1], head); err != nil || !contains {
						t.Fatalf("native pre-replacement ancestry %t %v", contains, err)
					}
					replacement := strings.TrimSpace(runEngineGit(t, dir, "commit-tree", tree, "-p", target, "-m", "test: connected false ancestry"))
					installed := false
					restore = func() {
						if !installed {
							return
						}
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if _, _, err := runCommand(cleanupCtx, defaultRunner, 0, 0, dir, "git", "replace", "-d", head); err != nil {
							t.Error(err)
							return
						}
						installed = false
						if contains, err := isMergeAncestor(cleanupCtx, dir, args[1], head); err != nil || !contains {
							t.Errorf("restored native ancestry %t %v", contains, err)
						}
					}
					t.Cleanup(restore)
					runEngineGit(t, dir, "replace", head, replacement)
					installed = true
					if contains, err := isMergeAncestor(ctx, dir, args[1], head); err != nil || contains {
						t.Fatalf("native connected false ancestry %t %v", contains, err)
					}
				}
				return nil
			}
			run.after = func(_ context.Context, dir, program string, args []string, result runner.Result, err error) {
				if program != "git" || err != nil || result.ExitCode != 0 {
					return
				}
				if name == "final false ancestry" && consumed == 1 && len(args) == 3 && args[0] == "merge-base" && args[1] == r.Sources[0].SHA && dir == replacementDir {
					if strings.TrimSpace(result.CombinedOutput) == args[1] {
						t.Fatal("delegated query was not native false")
					}
					restore()
					return
				}
				if consumed != 0 {
					return
				}
				if name == "target during create" && dir == r.Candidate.Worktree && len(args) == 3 && args[0] == "rev-parse" && strings.HasSuffix(args[2], "^{tree}") {
					consumed++
					want = "while creating ancestry seal"
					mutateTarget()
					return
				}
				if dir != replacementDir {
					return
				}
				if name == "final tree" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"}) && counts[strings.Join(args, " ")] == 2 {
					consumed++
					want = "ancestry seal changed target tree"
					mutateFile(dir, true)
					return
				}
				if len(args) != 3 || args[0] != "rev-parse" || !strings.HasSuffix(args[2], "^{tree}") || counts["rev-parse --verify HEAD^{commit}"] != 2 {
					return
				}
				switch name {
				case "late candidate":
					consumed++
					want = "failed candidate changed while sealing"
					mutateFile(r.Candidate.Worktree, false)
					t.Cleanup(func() { _ = os.Remove(filepath.Join(r.Candidate.Worktree, "seal-observation.txt")) })
				case "source advanced":
					consumed++
					want = "advanced from observed"
					t.Cleanup(sourceRestore)
					runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "test: source advanced during seal")
				case "source rewound":
					consumed++
					want = "no longer matches observed descendant"
					t.Cleanup(sourceRestore)
					runEngineGit(t, source.WorktreeDir, "reset", "--hard", r.Sources[0].SHA)
				case "late target":
					consumed++
					want = "while sealing; refusing result"
					mutateTarget()
				}
			}
			_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256)
			restore()
			if advanced {
				sourceRestore()
			}
			if consumed != 1 || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("physical %s consumed=%d counts=%v want=%q err=%v", name, consumed, counts, want, err)
			}
			if replacementDir != "" {
				if _, err := os.Stat(replacementDir); err != nil {
					t.Fatalf("late refusal removed managed candidate %v", err)
				}
			}
			sealOwnerAssertReleased(t, f, r)
			assertValidationFailureSealImmutableBytes(t, f, r, source, receiptBytes, candidateBytes, sourceBytes)
		})
	}
}

func TestE2ESealOwnerNativeNoMergeAndCreateRefusals(t *testing.T) {
	t.Parallel()
	t.Run("all historical roots already contained", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:main")
		before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
		merges := 0
		run := &sealOwnerRunner{Runner: defaultRunner, before: func(_ context.Context, _, program string, args []string) error {
			if program == "git" && len(args) > 1 && args[0] == "merge" && args[1] == "--strategy=ours" {
				merges++
			}
			return nil
		}}
		options := sealOwnerOptions(f, r)
		options.Model = " "
		result, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), options, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256)
		if err != nil || result.Status != "validation_failure_seal_prepared" || merges != 0 || result.Candidate.SHA != r.Candidate.SHA {
			t.Fatalf("no merge %+v count=%d err=%v", result, merges, err)
		}
		assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
		sealOwnerAssertReleased(t, f, r)
	})
	t.Run("actual session required create refusal", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
		options := sealOwnerOptions(f, r)
		options.SessionRequired = true
		options.AgentRuntime = ""
		options.AgentID = ""
		_, err := PrepareValidationFailedWorktreeMergeSeal(t.Context(), options)
		if err == nil || !strings.Contains(err.Error(), "create ancestry seal worktree") {
			t.Fatalf("native Create refusal %v", err)
		}
		assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
		sealOwnerAssertReleased(t, f, r)
	})
	t.Run("real multiple repository task inventory", func(t *testing.T) {
		t.Parallel()
		f, r, source := sealOwnerFixture(t)
		other := filepath.Join(f.githubDir, "acme", "other")
		runEngineGit(t, f.canonical, "clone", f.repository.CloneURL, other)
		runEngineGit(t, other, "config", "user.name", "WB Test")
		runEngineGit(t, other, "config", "user.email", "wb@example.test")
		task := r.ID + "-ancestry-seal"
		branch := "wb/recovery/" + r.Target + "/" + mergeOperationSuffix(r.ID) + "-ancestry-seal"
		created, err := worktrees.Create(t.Context(), []string{r.Repository, "acme/other"}, worktrees.CreateOptions{ProjectsRoot: f.githubDir, Operation: task, Branch: branch, BranchChosen: true, Base: r.Target, WorkLog: worktrees.WorkLogOptions{Model: "test-model", AgentRuntime: "test"}})
		if err != nil || len(created) != 2 {
			t.Fatalf("native shared task setup %+v %v", created, err)
		}
		before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
		_, err = PrepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r))
		if err == nil || !strings.Contains(err.Error(), "resolves to 2 worktrees") {
			t.Fatalf("actual multi-repository task %v", err)
		}
		assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
		sealOwnerAssertReleased(t, f, r)
	})
}

//nolint:paralleltest // TMPDIR/TMP/TEMP are process-wide scratch policy; mutation starts only after authentic source proof and receipt hashing.
func TestE2ESealOwnerNativeScratchRefusalOccursAfterEvidence(t *testing.T) {
	f, r, source := sealOwnerFixture(t)
	before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
	originalScratch := make(map[string]struct {
		value   string
		present bool
	})
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		value, present := os.LookupEnv(key)
		originalScratch[key] = struct {
			value   string
			present bool
		}{value, present}
	}
	scratchRestored := false
	restoreScratch := func() {
		if scratchRestored {
			return
		}
		for key, original := range originalScratch {
			var err error
			if original.present {
				err = os.Setenv(key, original.value)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				t.Error(err)
			}
		}
		scratchRestored = true
	}
	t.Cleanup(restoreScratch)
	blocker := filepath.Join(t.TempDir(), "scratch-file")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	consumed := 0
	hash := func(path string) (string, error) {
		if path != r.ReceiptPath {
			t.Fatalf("hash path %s", path)
		}
		digest, err := worktreeMergeReceiptSHA256(path)
		if err != nil {
			return "", err
		}
		consumed++
		for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
			t.Setenv(key, blocker)
		}
		return digest, nil
	}
	_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), defaultRunner, readWorktreeMergeReceipt, hash)
	restoreScratch()
	var pathErr *os.PathError
	if consumed != 1 || !errors.As(err, &pathErr) || filepath.Dir(pathErr.Path) != blocker || !strings.HasPrefix(filepath.Base(pathErr.Path), "wb-validation-failure-seal-prompt-") {
		t.Fatalf("native scratch stage consumed=%d error=%v", consumed, err)
	}
	assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
	sealOwnerAssertReleased(t, f, r)
}

func TestE2ESealOwnerRefusesAuthenticatedReplacementClaimDrift(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	writeEngineFile(t, filepath.Join(f.canonical, "seal-base-parent.txt"), "genuine target advance\n")
	runEngineGit(t, f.canonical, "add", "seal-base-parent.txt")
	runEngineGit(t, f.canonical, "commit", "-m", "test: target has distinct immutable parent")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	f, r, source := sealOwnerFixtureWithFixture(t, f)
	before, candidate, sourceClaim := validationFailureSealImmutableBytes(t, f, r, source)
	replacementDir := ""
	consumed := 0
	restore := func() {}
	run := &sealOwnerRunner{Runner: defaultRunner, after: func(_ context.Context, dir, program string, args []string, result runner.Result, err error) {
		if consumed != 0 || program != "git" || err != nil || result.ExitCode != 0 || dir == r.Candidate.Worktree || dir == source.WorktreeDir || dir == f.canonical || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"}) {
			return
		}
		replacementDir = dir
		view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: dir})
		if err != nil || view.Claim == nil {
			t.Fatalf("native created claim %v", err)
		}
		claim := view.Claim
		original, err := os.ReadFile(claim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		projection := filepath.Join(dir, ".wb-worklog", "recovery.json")
		pointer, err := os.ReadFile(projection)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(original, &record); err != nil {
			t.Fatal(err)
		}
		// A fully identified private historical claim can have a different real base
		// while still satisfying the native replacement validator's nonempty-base policy.
		differentBase := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", r.TargetSHA+"^"))
		if contains, err := isMergeAncestor(t.Context(), dir, differentBase, strings.TrimSpace(result.CombinedOutput)); err != nil || !contains {
			t.Fatalf("alternate base must be a real native target ancestor %t %v", contains, err)
		}
		if differentBase == claim.BaseSHA {
			t.Fatal("fixture needs distinct native candidate/base")
		}
		newID := worktrees.WorkLogClaimID(claim.EffortID, worktrees.CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: differentBase})
		newPath := filepath.Join(filepath.Dir(claim.ClaimPath), newID+".json")
		restored := false
		restore = func() {
			if restored {
				return
			}
			if err := os.WriteFile(projection, pointer, 0o600); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(claim.ClaimPath, original, 0o600); err != nil {
				t.Error(err)
			}
			if err := os.Remove(newPath); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
			restored = true
		}
		t.Cleanup(restore)
		record["base_sha"], record["claim_id"] = differentBase, newID
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(projection, bytes.ReplaceAll(pointer, []byte(claim.ClaimID), []byte(newID)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(claim.ClaimPath); err != nil {
			t.Fatal(err)
		}
		authenticView, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: dir})
		if err != nil || authenticView.Claim == nil || authenticView.Claim.ClaimID != newID || authenticView.Claim.Base != claim.Base || authenticView.Claim.BaseSHA != differentBase {
			t.Fatalf("native alternate claim corroboration: view=%+v err=%v", authenticView, err)
		}
		got, authenticated, err := validateValidationFailureReplacementWithRunner(t.Context(), defaultRunner, f.githubDir, r, dir)
		if err != nil || authenticated == nil || authenticated.BaseSHA != differentBase || got.Task != r.ID+"-ancestry-seal" {
			t.Fatalf("actual authenticated alternate base %+v %+v %v", got, authenticated, err)
		}
		consumed++
	}}
	_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256)
	restore()
	if consumed != 1 || err == nil || !strings.Contains(err.Error(), "Work Log does not match the exact task, branch, and fetched target identity") {
		t.Fatalf("exact replacement identity consumed=%d err=%v", consumed, err)
	}
	if _, err := os.Stat(replacementDir); err != nil {
		t.Fatalf("candidate not retained %v", err)
	}
	assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, sourceClaim)
	sealOwnerAssertReleased(t, f, r)
}

//nolint:paralleltest // Private XDG_CONFIG_HOME is process-wide configuration policy; the fixture remains native and explicitly rooted.
func TestE2ESealOwnerNativeListFilesystemErrorAfterEvidence(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	f, r, source := sealOwnerFixture(t)
	before, candidate, claim := validationFailureSealImmutableBytes(t, f, r, source)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("private blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	consumed := 0
	hash := func(path string) (string, error) {
		if path != r.ReceiptPath {
			t.Fatalf("hash path %s", path)
		}
		digest, err := worktreeMergeReceiptSHA256(path)
		if err != nil {
			return "", err
		}
		t.Cleanup(func() {
			if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
		})
		writeEngineFile(t, configPath, fmt.Sprintf("version: 1\nworktrees:\n  root: %q\n", blocker))
		consumed++
		return digest, nil
	}
	_, err := prepareValidationFailedWorktreeMergeSeal(t.Context(), sealOwnerOptions(f, r), defaultRunner, readWorktreeMergeReceipt, hash)
	if consumed != 1 || err == nil || !strings.Contains(err.Error(), "inspect ancestry seal worktree") || !strings.Contains(err.Error(), "read worktree tasks") {
		t.Fatalf("actual fatal List consumed=%d error=%v", consumed, err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	assertValidationFailureSealImmutableBytes(t, f, r, source, before, candidate, claim)
	sealOwnerAssertReleased(t, f, r)
}
