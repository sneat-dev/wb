//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type legacyMissingClaimNativeFixture struct {
	git            *gitFixture
	options        AbortOptions
	entry          ListResult
	claimPath      string
	recoveryPath   string
	runPath        string
	outboxPath     string
	manifestPath   string
	projectionPath string
	head           string
}

func newLegacyMissingClaimNativeFixture(t *testing.T, task string, prompt bool) legacyMissingClaimNativeFixture {
	t.Helper()
	workLog := WorkLogOptions{Model: "unknown"}
	if prompt {
		workLog.OriginalPrompt = writeWorkLogPromptFile(t, "original legacy recovery instruction\n")
		workLog.RequireOriginalPrompt = true
	}
	gitFixture, created, _, squashSHA, mergedAt := prepareAbsorbedCandidateWithWorkLog(t, task, workLog)
	integrationHead := gitTestOutput(t, gitFixture.canonical, "rev-parse", "integration/"+task)
	installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, mergedAt)
	projection, err := readWorkLogProjection(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(gitFixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	claimPath := filepath.Join(runPath, "claims", projection.ClaimID+".json")
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	options := AbortOptions{ProjectsRoot: gitFixture.projectsRoot, Task: task,
		Disposition: AbortDiscarded, AbsorbedBy: "77", DeleteRemote: true}
	results, err := Abort(context.Background(), options)
	if err != nil || len(results) != 1 || !results[0].WorkLogRecoveryPlanned {
		t.Fatalf("baseline legacy recovery plan = %#v, %v", results, err)
	}
	return legacyMissingClaimNativeFixture{git: gitFixture, options: options, entry: results[0].ListResult,
		claimPath: claimPath, recoveryPath: filepath.Join(runPath, "recoveries", projection.ClaimID+"-missing-claim.json"),
		runPath: runPath, outboxPath: filepath.Join(gitFixture.home, "worklogs", projection.EffortID, "outbox", projection.RunID+"-"+projection.ClaimID+"-claimed.json"),
		manifestPath:   filepath.Join(created.WorktreeDir, ".wb", "local", "manifest.yaml"),
		projectionPath: filepath.Join(created.WorktreeDir, workLogProjectionDirectory, workLogProjectionName),
		head:           gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD")}
}

func (fixture legacyMissingClaimNativeFixture) changeManifest(t *testing.T, change func(*Manifest)) {
	t.Helper()
	manifest, err := ReadManifest(fixture.entry.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	change(&manifest)
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture legacyMissingClaimNativeFixture) assertNoRecoveryPublication(t *testing.T) {
	t.Helper()
	for _, path := range []string{fixture.claimPath, fixture.recoveryPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refusal published recovery evidence at %s: %v", path, err)
		}
	}
	if got := gitTestOutput(t, fixture.entry.WorktreeDir, "rev-parse", "HEAD"); got != fixture.head {
		t.Fatalf("refusal changed source HEAD from %s to %s", fixture.head, got)
	}
}

func assertLegacyEvidenceBytesUnchanged(t *testing.T, paths ...string) func() {
	t.Helper()
	before := make(map[string][]byte, len(paths))
	missing := make([]string, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		switch {
		case err == nil:
			before[path] = content
		case errors.Is(err, os.ErrNotExist):
			missing = append(missing, path)
		case errors.Is(err, syscall.ENOTDIR):
			// A tracked child may sit below a separate blocking parent file.
		default:
			t.Fatalf("read immutable evidence %s: %v", path, err)
		}
	}
	return func() {
		t.Helper()
		for path, content := range before {
			if after, err := os.ReadFile(path); err != nil || string(after) != string(content) {
				t.Fatalf("refusal changed immutable evidence %s: %q, %v", path, after, err)
			}
		}
		for _, path := range missing {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal published new immutable evidence at %s: %v", path, err)
			}
		}
	}
}

//nolint:paralleltest // absorbing PR and Git fixtures configure process-wide environment.
func TestE2ELegacyMissingClaimPlannerRefusesChangedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, legacyMissingClaimNativeFixture)
	}{
		{"missing-manifest", "read immutable worktree manifest", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			if err := os.Remove(f.manifestPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"reconstructed-provenance", "worktree manifest provenance", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			f.changeManifest(t, func(m *Manifest) { m.Provenance = ProvenanceReconstructed; m.InferredFields = []string{"base"} })
		}},
		{"invalid-projection", "read local Work Log projection", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			if err := os.WriteFile(f.projectionPath, []byte("{invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"deterministic-claim-id", "manifest claim ID does not match", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			f.changeManifest(t, func(m *Manifest) { m.BaseSHA = strings.Repeat("a", 40) })
		}},
		{"invalid-execution-identity", "manifest execution identity is invalid", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			f.changeManifest(t, func(m *Manifest) { m.CLI = "invalid CLI identifier" })
		}},
		{"blocked-outbox-directory", "open historical Work Log outbox", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			outbox := filepath.Dir(f.outboxPath)
			if err := os.Rename(outbox, outbox+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outbox, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each native fixture changes process-wide Git and PR environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLegacyMissingClaimNativeFixture(t, "legacy-batch-"+tc.name, false)
			tc.change(t, fixture)
			assertBytes := assertLegacyEvidenceBytesUnchanged(t, fixture.manifestPath, fixture.projectionPath, fixture.outboxPath)
			if _, err := planLegacyMissingClaimRecovery(fixture.git.home, fixture.options, fixture.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s planner refusal = %v, want %q", tc.name, err, tc.want)
			}
			if tc.name == "missing-manifest" {
				if err := recoverLegacyMissingClaimForAbort(fixture.git.home, fixture.options, fixture.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("recovery skipped missing manifest refusal: %v", err)
				}
			}
			assertBytes()
			fixture.assertNoRecoveryPublication(t)
		})
	}
}

//nolint:paralleltest // absorbing PR and Git fixtures configure process-wide environment.
func TestE2ELegacyMissingClaimPlannerNormalizesUnknownModel(t *testing.T) {
	fixture := newLegacyMissingClaimNativeFixture(t, "legacy-batch-empty-model", false)
	fixture.changeManifest(t, func(m *Manifest) { m.Model = "" })
	plan, err := planLegacyMissingClaimRecovery(fixture.git.home, fixture.options, fixture.entry)
	if err != nil || plan.claim.Model != "unknown" || plan.claim.ModelProvenance != modelProvenanceUnknown {
		t.Fatalf("historical missing model reconstruction = %#v, %v", plan.claim, err)
	}
	fixture.assertNoRecoveryPublication(t)
}

//nolint:paralleltest // absorbing PR and Git fixtures configure process-wide environment.
func TestE2ELegacyMissingClaimPreflightDistinguishesProjectionAndClaimAbsence(t *testing.T) {
	fixture := newLegacyMissingClaimNativeFixture(t, "legacy-batch-preflight-absence", false)
	projectionBytes, err := os.ReadFile(fixture.projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.projectionPath); err != nil {
		t.Fatal(err)
	}
	if planned, err := preflightAbortWorkLog(fixture.git.home, fixture.options, fixture.entry); err != nil || planned {
		t.Fatalf("missing projection is not a recovery candidate: planned=%t, err=%v", planned, err)
	}
	if err := os.WriteFile(fixture.projectionPath, projectionBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	withoutLanding := fixture.options
	withoutLanding.AbsorbedBy = ""
	if planned, err := preflightAbortWorkLog(fixture.git.home, withoutLanding, fixture.entry); planned || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing private claim without exact landing authority = planned %t, %v", planned, err)
	}
	fixture.assertNoRecoveryPublication(t)
}

//nolint:paralleltest // absorbing PR and Git fixtures configure process-wide environment.
func TestE2ELegacyMissingClaimPlannerRefusesPromptMetadataFaults(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, legacyMissingClaimNativeFixture)
	}{
		{"missing-archive", "read immutable original prompt archive", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			if err := os.Remove(filepath.Join(f.runPath, "original-prompt.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed-metadata", "read immutable original prompt metadata", func(t *testing.T, f legacyMissingClaimNativeFixture) {
			if err := os.WriteFile(filepath.Join(f.runPath, "original-prompt.json"), []byte("{invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each native fixture changes process-wide Git and PR environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLegacyMissingClaimNativeFixture(t, "legacy-batch-prompt-"+tc.name, true)
			tc.change(t, fixture)
			assertBytes := assertLegacyEvidenceBytesUnchanged(t, filepath.Join(fixture.runPath, "original-prompt.txt"), filepath.Join(fixture.runPath, "original-prompt.json"))
			if _, err := planLegacyMissingClaimRecovery(fixture.git.home, fixture.options, fixture.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s prompt refusal = %v, want %q", tc.name, err, tc.want)
			}
			assertBytes()
			fixture.assertNoRecoveryPublication(t)
		})
	}
}

//nolint:paralleltest // absorbing PR and Git fixtures configure process-wide environment.
func TestE2ELegacyMissingClaimRecoveryRefusesChangedPrivateEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, legacyMissingClaimNativeFixture, legacyMissingClaimPlan)
	}{
		{"blocked-lock-directory", "open claim-lock directory", func(t *testing.T, f legacyMissingClaimNativeFixture, _ legacyMissingClaimPlan) {
			path := filepath.Join(f.runPath, "locks")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"blocked-claims-directory", "claims", func(t *testing.T, f legacyMissingClaimNativeFixture, _ legacyMissingClaimPlan) {
			path := filepath.Dir(f.claimPath)
			if err := os.Rename(path, path+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed-claim", "different immutable bytes", func(t *testing.T, f legacyMissingClaimNativeFixture, plan legacyMissingClaimPlan) {
			plan.claim.Model = "different"
			wtLifeCovWriteJSON(t, f.claimPath, plan.claim)
		}},
		{"malformed-claim", "inspect missing private claim", func(t *testing.T, f legacyMissingClaimNativeFixture, _ legacyMissingClaimPlan) {
			if err := os.WriteFile(f.claimPath, []byte("{invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"blocked-recoveries-directory", "open missing-claim recovery receipts", func(t *testing.T, f legacyMissingClaimNativeFixture, _ legacyMissingClaimPlan) {
			if err := os.WriteFile(filepath.Dir(f.recoveryPath), []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed-receipt", "does not match current exact evidence", func(t *testing.T, f legacyMissingClaimNativeFixture, plan legacyMissingClaimPlan) {
			if err := os.Mkdir(filepath.Dir(f.recoveryPath), 0o700); err != nil {
				t.Fatal(err)
			}
			plan.recovery.RecoveredAt = time.Now().UTC()
			plan.recovery.HeadSHA = strings.Repeat("f", 40)
			wtLifeCovWriteJSON(t, f.recoveryPath, plan.recovery)
		}},
		{"malformed-receipt", "inspect missing-claim recovery receipt", func(t *testing.T, f legacyMissingClaimNativeFixture, _ legacyMissingClaimPlan) {
			if err := os.Mkdir(filepath.Dir(f.recoveryPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.recoveryPath, []byte("{invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"claim-without-receipt", "mandatory recovery receipt", func(t *testing.T, f legacyMissingClaimNativeFixture, plan legacyMissingClaimPlan) {
			wtLifeCovWriteJSON(t, f.claimPath, plan.claim)
			if err := os.Mkdir(filepath.Dir(f.recoveryPath), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each native fixture changes process-wide Git and PR environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLegacyMissingClaimNativeFixture(t, "legacy-recovery-"+tc.name, false)
			plan, err := planLegacyMissingClaimRecovery(fixture.git.home, fixture.options, fixture.entry)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, fixture, plan)
			evidencePaths := []string{fixture.claimPath, fixture.recoveryPath}
			switch tc.name {
			case "blocked-lock-directory":
				evidencePaths = append(evidencePaths, filepath.Join(fixture.runPath, "locks"))
			case "blocked-claims-directory":
				evidencePaths = append(evidencePaths, filepath.Dir(fixture.claimPath))
			case "blocked-recoveries-directory":
				evidencePaths = append(evidencePaths, filepath.Dir(fixture.recoveryPath))
			}
			assertBytes := assertLegacyEvidenceBytesUnchanged(t, evidencePaths...)
			if err := recoverLegacyMissingClaimForAbort(fixture.git.home, fixture.options, fixture.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s recovery refusal = %v, want %q", tc.name, err, tc.want)
			}
			assertBytes()
			if got := gitTestOutput(t, fixture.entry.WorktreeDir, "rev-parse", "HEAD"); got != fixture.head {
				t.Fatalf("%s refusal changed source HEAD from %s to %s", tc.name, fixture.head, got)
			}
		})
	}
}
