package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

//nolint:paralleltest // Native Git fixture configures process-wide Git and WB environment.
func TestCleanupMissingHistoryRetiresOnlyAfterDurableAttestation(t *testing.T) {
	for _, missing := range []string{"run", "claims directory", "claim"} {
		//nolint:paralleltest // Native fixture owns process environment.
		t.Run(missing, func(t *testing.T) {
			fixture, created, head, mergedAt := prepareMergedTask(t, "missing-history")
			installMergedPullRequestFixture(t, head, mergedAt)
			projection, err := readWorkLogProjection(created.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			runPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
			missingPath := runPath
			if missing != "run" {
				missingPath = filepath.Join(runPath, "claims")
			}
			if missing == "claim" {
				missingPath = filepath.Join(missingPath, projection.ClaimID+".json")
			}
			if err := os.RemoveAll(missingPath); err != nil {
				t.Fatal(err)
			}
			options := CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "missing-history", DeleteRemote: true,
				Now: func() time.Time { return mergedAt.Add(time.Hour) }}
			plan, err := Cleanup(context.Background(), options)
			if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible {
				t.Fatalf("dry-run = %+v, %v", plan.Results, err)
			}
			if _, err := os.Lstat(missingPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry-run recreated history: %v", err)
			}
			entry := plan.Results[0].ListResult
			receiptPath := filepath.Join(lifecycleBacklogDirectory(fixture.home), "missing-history", lifecycleBacklogID(entry, "removed")+".json")
			if _, err := os.Lstat(receiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry-run wrote attestation: %v", err)
			}
			options.Apply = true
			options.afterCleanupWorktreeRemoval = func(string) error {
				var receipt cleanupMissingHistoryReceipt
				data, err := os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.HeadSHA != head || receipt.Projection != projection || receipt.ManifestSHA256 == "" || receipt.RecordedAt.IsZero() || receipt.Landed.Target != "main" || !strings.Contains(receipt.MissingHistory, "unavailable") {
					t.Fatalf("incomplete retirement audit: %+v", receipt)
				}
				return errors.New("interrupted after removal")
			}
			if _, err := Cleanup(context.Background(), options); err == nil || !strings.Contains(err.Error(), "interrupted after removal") {
				t.Fatalf("interruption = %v", err)
			}
			before, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"claims", "terminals", "original-prompt.txt"} {
				path := filepath.Join(runPath, forbidden)
				if forbidden == "claims" {
					path = filepath.Join(path, projection.ClaimID+".json")
				}
				if _, err := os.Lstat(path); err == nil {
					t.Fatalf("synthesized history: %s", path)
				}
			}
			options.afterCleanupWorktreeRemoval = nil
			outcome, err := Cleanup(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if !cleanupApplied(outcome, "missing-history") {
				t.Fatalf("retry did not finish: %+v", outcome)
			}
			after, err := os.ReadFile(receiptPath)
			if err != nil || string(before) != string(after) {
				t.Fatalf("retry rewrote immutable audit: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Native fixture owns process-wide environment and serial subtests restore its files.
func TestMissingHistoryRefusesUnverifiableOrMismatchedEvidence(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "missing-history-refusals")
	installMergedPullRequestFixture(t, head, mergedAt)
	projection, err := readWorkLogProjection(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "missing-history-refusals", Now: func() time.Time { return mergedAt.Add(time.Hour) }})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible {
		t.Fatalf("plan = %+v %v", plan.Results, err)
	}
	entry := plan.Results[0].ListResult
	runPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	if err := os.RemoveAll(runPath); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(created.WorktreeDir, ".wb", "local", manifestName)
	projectionPath := filepath.Join(created.WorktreeDir, workLogProjectionDirectory, workLogProjectionName)
	for _, scenario := range []string{"missing projection", "malformed projection", "projection symlink", "inactive projection", "missing journal", "missing manifest", "manifest symlink", "manifest malformed", "manifest invalid", "manifest mismatch", "claim ID mismatch", "open PR", "dirty flag", "shared layout", "uncontained head", "unknown target", "dirty checkout", "changed head", "changed branch", "cancelled Git", "run symlink", "claim symlink", "claim malformed", "orphan terminal", "terminal malformed"} {
		//nolint:paralleltest // Cases mutate and restore one shared native fixture.
		t.Run(scenario, func(t *testing.T) {
			current := entry
			ctx := context.Background()
			write := func(path string, data []byte) {
				t.Helper()
				old, readErr := os.ReadFile(path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if readErr == nil {
						if err := os.WriteFile(path, old, 0o600); err != nil {
							t.Error(err)
						}
					} else {
						_ = os.Remove(path)
					}
				})
			}
			hide := func(path string, symlink bool) {
				t.Helper()
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if err := os.Symlink(path+".saved", path); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() {
					_ = os.Remove(path)
					if err := os.Rename(path+".saved", path); err != nil {
						t.Error(err)
					}
				})
			}
			switch scenario {
			case "missing projection":
				hide(projectionPath, false)
			case "malformed projection":
				write(projectionPath, []byte("{"))
			case "projection symlink":
				hide(projectionPath, true)
			case "inactive projection":
				changed := projection
				changed.Lifecycle = "terminal"
				data, _ := json.Marshal(changed)
				write(projectionPath, data)
			case "missing journal":
				hide(filepath.Dir(manifestPath), false)
			case "missing manifest":
				hide(manifestPath, false)
			case "manifest symlink":
				hide(manifestPath, true)
			case "manifest malformed":
				write(manifestPath, []byte("["))
			case "manifest invalid":
				write(manifestPath, []byte("version: 999"))
			case "manifest mismatch", "claim ID mismatch":
				manifest, err := ReadManifest(created.WorktreeDir)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "manifest mismatch" {
					manifest.Branch = "other"
				} else {
					manifest.ClaimID = strings.Repeat("b", 64)
					changed := projection
					changed.ClaimID = manifest.ClaimID
					data, _ := json.Marshal(changed)
					write(projectionPath, data)
				}
				data, _ := yaml.Marshal(manifest)
				write(manifestPath, data)
			case "open PR":
				current.OpenPullRequest = &PullRequest{URL: "https://github.com/acme/app/pull/147"}
			case "dirty flag":
				current.Clean = false
			case "shared layout":
				current.Local = false
			case "uncontained head":
				current.RemoteTargetSHA = created.BaseSHA
			case "unknown target":
				current.RemoteTargetSHA = strings.Repeat("f", 40)
			case "dirty checkout":
				write(filepath.Join(created.WorktreeDir, "untracked.txt"), []byte("uncommitted"))
			case "changed head":
				current.HeadSHA = created.BaseSHA
			case "changed branch":
				gitTest(t, created.WorktreeDir, "symbolic-ref", "HEAD", "refs/heads/other")
				t.Cleanup(func() { gitTest(t, created.WorktreeDir, "symbolic-ref", "HEAD", "refs/heads/"+entry.Branch) })
			case "cancelled Git":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "run symlink":
				if err := os.MkdirAll(filepath.Dir(runPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), runPath); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(runPath) })
			case "claim symlink", "claim malformed", "orphan terminal", "terminal malformed":
				t.Cleanup(func() { _ = os.RemoveAll(runPath) })
				path := filepath.Join(runPath, "claims", projection.ClaimID+".json")
				if scenario == "orphan terminal" || scenario == "terminal malformed" {
					path = filepath.Join(runPath, "terminals", projection.ClaimID+".json")
				}
				if scenario == "claim symlink" {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), path); err != nil {
						t.Fatal(err)
					}
				} else {
					data := []byte("{")
					if scenario == "orphan terminal" {
						data = []byte("{}")
					}
					write(path, data)
				}
			}
			matched, release, err := retireCleanupMissingHistory(ctx, fixture.home, current)
			release()
			if matched || (err == nil && scenario != "missing projection") {
				t.Fatalf("unsafe recovery: matched=%v err=%v", matched, err)
			}
			if _, err := os.Lstat(filepath.Join(lifecycleBacklogDirectory(fixture.home), "missing-history")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal wrote recovery receipt: %v", err)
			}
		})
	}
}

//nolint:paralleltest // Native Git fixture configures process-wide environment.
func TestMissingHistoryAttestationIsIdempotentAcrossAdvancingTarget(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "missing-history-retry")
	installMergedPullRequestFixture(t, head, mergedAt)
	projection, err := readWorkLogProjection(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	if err := os.RemoveAll(runPath); err != nil {
		t.Fatal(err)
	}
	plan, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "missing-history-retry", Now: func() time.Time { return mergedAt.Add(time.Hour) }})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible {
		t.Fatalf("plan = %+v %v", plan.Results, err)
	}
	entry := plan.Results[0].ListResult
	matched, release, err := retireCleanupMissingHistory(context.Background(), fixture.home, entry)
	if err != nil || !matched {
		t.Fatalf("attest = %v %v", matched, err)
	}
	release()
	path := filepath.Join(lifecycleBacklogDirectory(fixture.home), "missing-history", lifecycleBacklogID(entry, "removed")+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "later landing")
	entry.RemoteTargetSHA = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	matched, release, err = retireCleanupMissingHistory(context.Background(), fixture.home, entry)
	if err != nil || !matched {
		t.Fatalf("retry = %v %v", matched, err)
	}
	release()
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("retry changed original receipt: %v", err)
	}
}

//nolint:paralleltest // Native fixture and immutable-write fault seam own process-wide state.
func TestMissingHistoryRefusesClaimRacesAndUnwritableAudit(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "missing-history-audit-faults")
	installMergedPullRequestFixture(t, head, mergedAt)
	projection, err := readWorkLogProjection(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "missing-history-audit-faults", Now: func() time.Time { return mergedAt.Add(time.Hour) }})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible {
		t.Fatalf("plan = %+v %v", plan.Results, err)
	}
	entry := plan.Results[0].ListResult
	if _, matched, err := planCleanupMissingHistory(context.Background(), fixture.home, entry); matched || err != nil {
		t.Fatalf("ordinary claim selected missing-history path: %v %v", matched, err)
	}
	runPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	claimPath := filepath.Join(runPath, "claims", projection.ClaimID+".json")
	claim, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(created.WorktreeDir, ".wb", "local", manifestName)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	backlogPath := lifecycleBacklogDirectory(fixture.home)
	receiptName := lifecycleBacklogID(entry, "removed") + ".json"
	for _, scenario := range []string{"lock symlink", "dirty after lock", "claim appears", "manifest changes", "backlog symlink", "attestation directory symlink", "malformed attestation", "conflicting attestation", "write collision"} {
		//nolint:paralleltest // Serial cases mutate one fixture and restore its metadata.
		t.Run(scenario, func(t *testing.T) {
			if err := os.RemoveAll(runPath); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(backlogPath); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.RemoveAll(runPath)
				_ = os.RemoveAll(backlogPath)
				_ = os.Remove(filepath.Join(created.WorktreeDir, "untracked.txt"))
				if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
					t.Error(err)
				}
			})
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := func(path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			}
			var afterLock func()
			switch scenario {
			case "lock symlink":
				link(filepath.Join(runPath, "locks"))
			case "dirty after lock":
				afterLock = func() { write(filepath.Join(created.WorktreeDir, "untracked.txt"), []byte("changed")) }
			case "claim appears":
				afterLock = func() { write(claimPath, claim) }
			case "manifest changes":
				afterLock = func() {
					var changed Manifest
					if err := yaml.Unmarshal(manifest, &changed); err != nil {
						t.Fatal(err)
					}
					changed.CreatedAt = changed.CreatedAt.Add(time.Second)
					data, err := yaml.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					write(manifestPath, data)
				}
			case "backlog symlink":
				link(backlogPath)
			case "attestation directory symlink":
				link(filepath.Join(backlogPath, "missing-history"))
			case "malformed attestation":
				write(filepath.Join(backlogPath, "missing-history", receiptName), []byte("{"))
			case "conflicting attestation":
				receipt, matched, err := planCleanupMissingHistory(context.Background(), fixture.home, entry)
				if err != nil || !matched {
					t.Fatalf("plan attestation = %v %v", matched, err)
				}
				receipt.RecordedAt = time.Now().UTC()
				receipt.RemoteTargetSHA = strings.Repeat("f", 40)
				data, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(backlogPath, "missing-history", receiptName), data)
			case "write collision":
				original := writeBytesImmutableAtBeforeRename
				t.Cleanup(func() { writeBytesImmutableAtBeforeRename = original })
				writeBytesImmutableAtBeforeRename = func(directory *os.File, name string) {
					if name == receiptName {
						writeBytesImmutableAtBeforeRename = original
						if err := writeBytesImmutableAt(directory, name, []byte("{}"), 0o600, false); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			matched, release, err := retireCleanupMissingHistoryObserved(context.Background(), fixture.home, entry, afterLock)
			release()
			if err == nil || matched {
				t.Fatalf("unsafe retirement: matched=%v err=%v", matched, err)
			}
			if _, err := os.Stat(created.WorktreeDir); err != nil {
				t.Fatalf("refusal destroyed checkout: %v", err)
			}
		})
	}
}
