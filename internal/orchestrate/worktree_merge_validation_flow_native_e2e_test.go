//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/mergevalidation"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type validationFlowPushRemoteRefusal struct {
	runner.Runner
	t        *testing.T
	worktree string
	cause    error
	calls    [][]string
}

func (r *validationFlowPushRemoteRefusal) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir != r.worktree || name != "git" {
		r.t.Fatalf("unexpected pre-push command %q %q %v", dir, name, args)
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	if reflect.DeepEqual(args, []string{"remote", "get-url", "--push", "origin"}) {
		return runner.Result{}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestE2EPrePushInspectionRefusalsDoNotRunHookOrPublish(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"push remote URL", "detached local branch"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			source := createMergeSource(t, fixture, "gate-inspection", "feature/gate-inspection", "change.txt", "change\n")
			head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
			if guard, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil || guard.Kind != "linked" {
				t.Fatalf("source native custody = %+v, %v", guard, err)
			}
			marker := filepath.Join(t.TempDir(), "hook-ran")
			hooks := t.TempDir()
			if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\n: > "+shellQuote(marker)+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, source.WorktreeDir, "config", "core.hooksPath", hooks)
			remoteRef := "refs/heads/gated-inspection"
			remoteBefore := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", remoteRef))
			if remoteBefore != "" {
				t.Fatalf("new remote target exists: %q", remoteBefore)
			}
			var gate *WorktreeMergePushGateReceipt
			var err error
			if stage == "push remote URL" {
				cause := errors.New("owned push URL observation refused")
				run := &validationFlowPushRemoteRefusal{Runner: defaultRunner, t: t, worktree: source.WorktreeDir, cause: cause}
				gate, err = runWorktreeMergePrePushGateWithRunner(t.Context(), run, source.WorktreeDir, head, remoteRef, 5*time.Second, 0, nil)
				want := [][]string{{"ls-remote", "--heads", "origin", remoteRef}, {"remote", "get-url", "--push", "origin"}}
				if !errors.Is(err, cause) || !strings.Contains(err.Error(), "resolve push remote for pre-push gate") || !reflect.DeepEqual(run.calls, want) {
					t.Fatalf("remote URL refusal = %v, calls %v", err, run.calls)
				}
			} else {
				// Both immediate restoration and fallback use the unchanged real Git runner.
				branch := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "branch", "--show-current"))
				t.Cleanup(func() {
					_, _, restoreErr := runCommand(context.Background(), defaultRunner, 30*time.Second, 0, source.WorktreeDir, "git", "checkout", branch)
					if restoreErr != nil {
						t.Errorf("restore source branch: %v", restoreErr)
					}
				})
				runEngineGit(t, source.WorktreeDir, "checkout", "--detach", head)
				gate, err = runWorktreeMergePrePushGateInjected(t.Context(), source.WorktreeDir, head, remoteRef, 5*time.Second, 0, nil)
				runEngineGit(t, source.WorktreeDir, "checkout", branch)
				if err == nil || !strings.Contains(err.Error(), "resolve local branch for pre-push gate") {
					t.Fatalf("detached local ref = %v", err)
				}
			}
			if gate != nil {
				t.Fatalf("inspection refusal returned passed gate: %+v", gate)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hook ran during inspection refusal: %v", err)
			}
			if after := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", remoteRef)); after != remoteBefore {
				t.Fatalf("refusal published remote ref: %q -> %q", remoteBefore, after)
			}
			if after := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); after != head {
				t.Fatalf("refusal changed source head: %q -> %q", head, after)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatalf("source custody after refusal: %v", err)
			}
		})
	}
}

func TestE2EDirectValidationPolicyRefusalPreservesNativeReceipt(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	source := createMergeSource(t, fixture, "changed-ci-input", "feature/changed-ci-input", ".github/workflows/changed.yml", "name: changed\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	receipt, plan := validationFlowDirectDeferral()
	receipt.Repository = fixture.repository.Slug
	receipt.TargetSHA = target
	receipt.Candidate.SHA = head
	receipt.Candidate.Worktree = source.WorktreeDir
	receipt.Validation.Revision = head
	receipt.ValidationDeferral.CandidateSHA = head
	before, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	err = applyOrDeferWorktreeMergeValidation(t.Context(), &receipt, plan, 5*time.Second, 0, 0, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "direct CI deferral refuses changed CI inputs: .github/workflows/changed.yml") {
		t.Fatalf("changed native CI input = %v", err)
	}
	after, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed direct policy observation stamped skipped evidence")
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
		t.Fatalf("input refusal changed HEAD: %s", got)
	}
}

//nolint:paralleltest // The owned Go protocol executable and cache are selected with t.Setenv.
func TestE2ECandidateBaselineReadFailuresPreserveValidationStage(t *testing.T) {
	for _, phase := range []string{"lint", "full"} {
		//nolint:paralleltest // Each row owns its process-wide PATH and WB validation cache until it returns.
		t.Run(phase, func(t *testing.T) {
			fixture := newExplicitRootEngineFixture(t)
			writeEngineGoModule(t, fixture.canonical, "package app\nfunc Value() int { return 1 }\n")
			writeEngineFile(t, filepath.Join(fixture.canonical, ".wb", "quality.yaml"), "version: 1\ngo_lint:\n  commands:\n    - [go, run, ./cmd/wb, deadcode]\n")
			runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", ".wb/quality.yaml")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: candidate validation protocol")
			head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "commands")
			script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(log) + "\n"
			if phase == "lint" {
				script += "case \"$1\" in run) echo candidate-lint-failure; exit 1 ;; esac\nexit 0\n"
			} else {
				script += "case \"$1\" in test) echo candidate-test-failure; exit 1 ;; esac\nexit 0\n"
			}
			if err := testenv.WriteExecutableFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("WB_VALIDATION_CACHE", t.TempDir())
			receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, TargetSHA: "unresolvable-target", Candidate: WorktreeMergeCandidate{Worktree: fixture.canonical, SHA: head}, ImportedMainDeadcode: &mergevalidation.ImportedMainDeadcode{CandidateSHA: "stale"}}
			err := validateWorktreeMergeCandidate(t.Context(), &receipt, 5*time.Second, 0, 0, 0, nil)
			prefix := "capture exact target lint baseline after candidate failure"
			if phase == "full" {
				prefix = "capture exact target validation baseline after candidate failure"
			}
			if err == nil || !strings.Contains(err.Error(), prefix) || !strings.Contains(err.Error(), "archive target unresolvable-target") {
				t.Fatalf("%s baseline failure = %v", phase, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "lint" && strings.Contains(string(calls), "test\n") {
				t.Fatalf("candidate tests ran after failed lint baseline: %s", calls)
			}
			if phase == "full" && (!strings.Contains(string(calls), "test\n") || receipt.Validation.Revision != head || receipt.Validation.Status != quality.StatusFailed) {
				t.Fatalf("full failure lost candidate evidence: %s, %+v", calls, receipt.Validation)
			}
			if receipt.ImportedMainDeadcode != nil {
				t.Fatal("stale imported evidence survived fresh candidate validation")
			}
			if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
				t.Fatalf("baseline failure changed candidate head: %s", got)
			}
		})
	}
}

//nolint:paralleltest // The owned changing command observation and cache use t.Setenv; real Git DAG remains immutable.
func TestE2ELateCandidateAttestationReadsFullBaselineRatherThanLintCache(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	writeEngineGoModule(t, fixture.canonical, "package app\nfunc Value() int { return 1 }\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, ".wb", "quality.yaml"), "version: 1\ngo_lint:\n  commands:\n    - [go, run, ./cmd/wb, deadcode]\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target-marker"), "exact target\n")
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", ".wb/quality.yaml", "target-marker")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: exact validation target")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	if err := os.Remove(filepath.Join(fixture.canonical, "target-marker")); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "add", "target-marker")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: linear candidate")
	head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	if mergeBase := strings.TrimSpace(runEngineGit(t, fixture.canonical, "merge-base", target, head)); mergeBase != target {
		t.Fatalf("candidate lost genuine target ancestry: %s", mergeBase)
	}
	if merges := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-list", "--merges", target+".."+head)); merges != "" {
		t.Fatalf("no-merge candidate contains merges: %s", merges)
	}
	cache, bin, observations := t.TempDir(), t.TempDir(), t.TempDir()
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache not initially empty: %v, %v", entries, err)
	}
	log, counter := filepath.Join(observations, "commands"), filepath.Join(observations, "target-lint-count")
	t.Cleanup(func() {
		if data, readErr := os.ReadFile(log); readErr == nil {
			t.Logf("owned physical command observations: %s", data)
		}
		if data, readErr := os.ReadFile(counter); readErr == nil {
			t.Logf("owned target-lint observation count: %s", data)
		}
	})
	const output = "New unreachable functions (1):\n  app.go:2: app.Hidden\nerror: 1 function(s) are unreachable from main and are not in .wb/deadcode-baseline.txt; wire them up, delete them, or record them with --update-baseline\nexit status 1\n"
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$1\" \"$PWD\" >> " + shellQuote(log) + "\n" +
		"case \"$1\" in\nrun)\n if [ -f target-marker ]; then\n  n=0; if [ -f " + shellQuote(counter) + " ]; then n=$(cat " + shellQuote(counter) + "); fi\n  n=$((n+1)); printf '%s' \"$n\" > " + shellQuote(counter) + "\n  if [ \"$n\" -gt 1 ]; then exit 0; fi\n fi\n printf '%s' " + shellQuote(output) + "\n exit 1 ;;\nesac\nexit 0\n"
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_VALIDATION_CACHE", cache)
	receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, TargetSHA: target, Candidate: WorktreeMergeCandidate{SHA: head, Worktree: fixture.canonical}}
	err = validateWorktreeMergeCandidate(t.Context(), &receipt, 5*time.Second, 0, 0, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "absent from exact target") {
		t.Fatalf("late no-merge attestation did not refuse new deadcode: %v, validation %+v, baseline %+v", err, receipt.Validation, receipt.BaselineValidation)
	}
	if receipt.ImportedMainDeadcode != nil || receipt.Validation.Revision != head || receipt.BaselineValidation.Revision != target || receipt.Validation.Status != quality.StatusFailed || receipt.BaselineValidation.Status != quality.StatusPassed {
		t.Fatalf("late refusal lost exact evidence: %+v", receipt)
	}
	sequenceBytes, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	sequence := strings.Split(strings.TrimSpace(string(sequenceBytes)), "\n")
	var lintDirs []string
	for _, call := range sequence {
		if strings.HasPrefix(call, "run|") {
			lintDirs = append(lintDirs, strings.TrimPrefix(call, "run|"))
		}
	}
	if len(lintDirs) != 3 || lintDirs[0] != fixture.canonical || lintDirs[1] == lintDirs[2] || !strings.Contains(lintDirs[1], "wb-worktree-merge-target-") || !strings.Contains(lintDirs[2], "wb-worktree-merge-target-") {
		t.Fatalf("candidate/lint-only/full baseline invocation order not physical: %v", sequence)
	}
	count, err := os.ReadFile(counter)
	if err != nil || string(count) != "2" {
		t.Fatalf("full baseline reused lint-only observation: %q, %v", count, err)
	}
	entries, err = os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cache, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Key    quality.ValidationCacheKey `json:"key"`
			Report quality.VerificationReport `json:"report"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Key.TargetRevision != target {
			t.Fatalf("cache contains another revision: %+v", record.Key)
		}
		var checks []string
		for _, check := range record.Key.Checks {
			checks = append(checks, string(check))
		}
		keys[strings.Join(checks, ",")] = entry.Name()
	}
	lintKey, fullKey := keys["lint"], keys["lint,test,build,spec"]
	if len(keys) != 2 || lintKey == "" || fullKey == "" || lintKey == fullKey {
		t.Fatalf("different check sets lack separate durable keys: %v", keys)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
		t.Fatalf("late observation moved real candidate: %s", got)
	}
	t.Logf("physical validation observation sequence: %v; lint key %s; full key %s", sequence, lintKey, fullKey)
}

func TestE2EPrePushNativeRemoteAndHookFailuresRetainExactSource(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"remote unavailable", "hook rejects"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			source := createMergeSource(t, fixture, "pre-push-refusal", "feature/pre-push-refusal", "change.txt", "change\n")
			head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
			if guard, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil || guard.Kind != "linked" {
				t.Fatalf("source custody = %+v, %v", guard, err)
			}
			remoteRef := "refs/heads/refused-candidate"
			before := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", remoteRef))
			if before != "" {
				t.Fatalf("owned remote target already exists: %q", before)
			}
			hooks, observations := t.TempDir(), filepath.Join(t.TempDir(), "hook-input")
			script := "#!/bin/sh\nset -eu\nprintf '%s %s\\n' \"$1\" \"$2\" > " + shellQuote(observations) + "\ncat >> " + shellQuote(observations) + "\necho owned-hook-refusal >&2\nexit 1\n"
			if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-push"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, source.WorktreeDir, "config", "core.hooksPath", hooks)
			if phase == "remote unavailable" {
				// This path is inside this row's private fixture and has never existed.
				missingRemote := filepath.Join(t.TempDir(), "unavailable.git")
				t.Cleanup(func() {
					_, _, err := runCommand(context.Background(), defaultRunner, 30*time.Second, 0, source.WorktreeDir, "git", "remote", "set-url", "origin", fixture.repository.CloneURL)
					if err != nil {
						t.Errorf("restore owned origin: %v", err)
					}
				})
				runEngineGit(t, source.WorktreeDir, "remote", "set-url", "origin", missingRemote)
			}
			gate, err := runWorktreeMergePrePushGateInjected(t.Context(), source.WorktreeDir, head, remoteRef, 5*time.Second, 0, nil)
			if phase == "remote unavailable" {
				runEngineGit(t, source.WorktreeDir, "remote", "set-url", "origin", fixture.repository.CloneURL)
				if err == nil || !strings.Contains(err.Error(), "inspect exact remote ref before pre-push gate") {
					t.Fatalf("native unavailable remote refusal = %v", err)
				}
				if _, statErr := os.Stat(observations); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("remote refusal ran hook: %v", statErr)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "managed pre-push gate failed before opening the push connection") || !strings.Contains(err.Error(), "owned-hook-refusal") {
					t.Fatalf("native hook refusal = %v", err)
				}
				data, readErr := os.ReadFile(observations)
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := "origin " + fixture.repository.CloneURL + "\nrefs/heads/feature/pre-push-refusal " + head + " " + remoteRef + " " + strings.Repeat("0", 40) + "\n"
				if string(data) != want {
					t.Fatalf("hook did not observe exact candidate update once: %q, want %q", data, want)
				}
			}
			if gate != nil {
				t.Fatalf("refusal returned positive gate: %+v", gate)
			}
			if after := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", remoteRef)); after != before {
				t.Fatalf("refusal published target: %q -> %q", before, after)
			}
			if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
				t.Fatalf("refusal moved source: %s", got)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatalf("custody after refusal: %v", err)
			}
		})
	}
}

func TestE2ELocalCandidatePassClearsDeferralAndNeedsNoTargetBaseline(t *testing.T) {
	t.Parallel()
	for _, fingerprintFault := range []bool{false, true} {
		name := "fingerprint retained"
		if fingerprintFault {
			name = "policy becomes unreadable after checks"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newExplicitRootEngineFixture(t)
			writeEngineGoModule(t, fixture.canonical, "package app\nfunc Value() int { return 42 }\n")
			writeEngineFile(t, filepath.Join(fixture.canonical, "app_test.go"), "package app\nimport \"testing\"\nfunc TestValue(t *testing.T) { if got := Value(); got != 42 { t.Fatalf(\"Value() = %d\", got) } }\n")
			writeEngineFile(t, filepath.Join(fixture.canonical, ".wb", "quality.yaml"), "version: 1\n")
			runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", "app_test.go", ".wb/quality.yaml")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: native candidate checks")
			target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			// Create fetches the exact base from this row's owned local bare origin.
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			source := createMergeSource(t, fixture, "native-candidate-pass", "feature/native-candidate-pass", "candidate.txt", "exact candidate\n")
			for _, name := range []string{"go.mod", "app.go", "app_test.go", ".wb/quality.yaml"} {
				want, err := os.ReadFile(filepath.Join(fixture.canonical, name))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(source.WorktreeDir, name))
				if err != nil || string(got) != string(want) {
					t.Fatalf("candidate lost published Go check input %s: %q, %v", name, got, err)
				}
			}
			head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
			if base := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "merge-base", target, head)); base != target {
				t.Fatalf("candidate lost actual target ancestry: %s", base)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatal(err)
			}
			policy := filepath.Join(source.WorktreeDir, ".wb", "quality.yaml")
			savedPolicy := filepath.Join(t.TempDir(), "quality.yaml")
			policyMoved := false
			restorePolicy := func() {
				if !policyMoved {
					return
				}
				if err := os.Remove(policy); err != nil {
					t.Fatalf("remove owned policy read fault: %v", err)
				}
				if err := os.Rename(savedPolicy, policy); err != nil {
					t.Fatalf("restore exact candidate policy: %v", err)
				}
				policyMoved = false
			}
			t.Cleanup(restorePolicy)
			receipt := WorktreeMergeReceipt{Repository: fixture.repository.Slug, TargetSHA: target, ReceiptPath: filepath.Join(t.TempDir(), "receipt.json"),
				Candidate:            WorktreeMergeCandidate{SHA: head, Worktree: source.WorktreeDir},
				ValidationDeferral:   &WorktreeMergeValidationDeferral{CandidateSHA: "stale-candidate", Reason: "previous CI deferral"},
				ValidationIdentity:   &WorktreeMergeValidationIdentity{CandidateSHA: "stale-candidate"},
				ImportedMainDeadcode: &mergevalidation.ImportedMainDeadcode{CandidateSHA: "stale-candidate"}}
			var events []progress.Event
			var completedChecks []string
			err := applyOrDeferWorktreeMergeValidation(t.Context(), &receipt, worktreeMergeValidationPlan{}, 30*time.Second, 0, 0, 0, func(event progress.Event) {
				events = append(events, event)
				if event.Phase == "validate_candidate" && event.State == progress.Completed {
					completedChecks = append(completedChecks, strings.SplitN(event.Detail, ":", 2)[0])
					if fingerprintFault && strings.HasPrefix(event.Detail, "build: go build ./...:") {
						// The last external check finished successfully; policy was already loaded.
						// A directory at the exact owned policy path makes os.ReadFile fail natively.
						if !reflect.DeepEqual(completedChecks, []string{"lint", "test", "build"}) {
							t.Fatalf("fingerprint fault preceded real completed checks: %v", completedChecks)
						}
						if err := os.Rename(policy, savedPolicy); err != nil {
							t.Fatal(err)
						}
						policyMoved = true
						if err := os.Mkdir(policy, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
			faultObserved := policyMoved
			restorePolicy()
			if err != nil {
				t.Fatalf("actual native candidate checks failed: %v, receipt %+v", err, receipt)
			}
			if faultObserved != fingerprintFault || !reflect.DeepEqual(completedChecks, []string{"lint", "test", "build"}) {
				t.Fatalf("completed checks/fault = %v/%t, wanted all native checks/%t", completedChecks, faultObserved, fingerprintFault)
			}
			if receipt.Validation.Status != quality.StatusPassed || receipt.Validation.Revision != head || !receipt.Validation.WorkspaceClean || receipt.ValidationDeferral != nil || receipt.ImportedMainDeadcode != nil {
				t.Fatalf("local pass retained stale evidence or lost actual pass: %+v", receipt)
			}
			for _, check := range []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild} {
				matches := 0
				for _, result := range receipt.Validation.Results {
					if result.Check == check && result.Language == "go" {
						matches++
						if result.Status != quality.StatusPassed || result.Attempts != 1 || !strings.HasPrefix(result.Command, "go ") {
							t.Fatalf("%s lacked real successful command evidence: %+v", check, result)
						}
					}
				}
				if matches != 1 {
					t.Fatalf("%s result count = %d", check, matches)
				}
			}
			if fingerprintFault {
				if receipt.ValidationIdentity != nil {
					t.Fatalf("unreadable policy retained reusable identity: %+v", receipt.ValidationIdentity)
				}
			} else if receipt.ValidationIdentity == nil || receipt.ValidationIdentity.CandidateSHA != head || receipt.ValidationIdentity.TargetSHA != target {
				t.Fatalf("native successful checks lost exact fingerprint: %+v", receipt.ValidationIdentity)
			}
			baseline := receipt.BaselineValidation
			if baseline.Status != quality.StatusSkipped || baseline.Revision != target || baseline.Path != "git:"+target || !baseline.WorkspaceClean || len(baseline.Results) != 1 || baseline.Results[0].Detail != "candidate passed every configured local check; target baseline was not needed" {
				t.Fatalf("successful candidate unnecessarily captured baseline: %+v", baseline)
			}
			for _, event := range events {
				if event.Phase == "validate_target_baseline" {
					t.Fatalf("successful candidate invoked target validation: %+v", events)
				}
			}
			if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
				t.Fatalf("local checks changed candidate head: %s", got)
			}
			if _, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main"}); err != nil {
				t.Fatalf("custody after local validation: %v", err)
			}
		})
	}
}
