//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Native Git and merged-PR fixtures pin process-wide XDG_CONFIG_HOME and PATH.
func TestE2EGCRetiresCorroboratedMissingHistoryWithDurableAudit(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "gc-missing-worklog-claim")
	installMergedPullRequestFixtures(t, nil, time.Time{})
	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Dir(filepath.Dir(claimPath))
	promptPath := filepath.Join(runPath, "original-prompt.txt")
	promptBefore, promptErr := os.ReadFile(promptPath)
	receiptDirectory := filepath.Join(lifecycleBacklogDirectory(fixture.home), "missing-history")
	opts := GCOptions{
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot,
		Tasks:            []string{"gc-missing-worklog-claim"},
		SkipSizes:        true,
		DeleteRemote:     true,
		Now:              func() time.Time { return mergedAt.Add(time.Hour) },
	}

	dryRun, err := GC(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	dryEntry := entryFor(t, dryRun, "gc-missing-worklog-claim")
	if dryEntry.Class != GCClassContained || !dryEntry.Eligible || dryEntry.Applied {
		t.Fatalf("dry entry = %#v, want eligible contained preview", dryEntry)
	}
	if _, err := os.Stat(receiptDirectory); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote retirement audit: %v", err)
	}
	if _, err := os.Stat(claimPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run recreated missing claim: %v", err)
	}

	opts.Apply = true
	applied, err := GC(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	applyEntry := entryFor(t, applied, "gc-missing-worklog-claim")
	if !applyEntry.Eligible || !applyEntry.Applied {
		t.Fatalf("apply entry = %#v, want completed retirement", applyEntry)
	}
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("retired checkout survived: %v", err)
	}
	for _, repository := range []string{fixture.remote, fixture.canonical} {
		if err := exec.Command("git", "-C", repository, "show-ref", "--verify", "refs/heads/"+result.Branch).Run(); err == nil {
			t.Fatalf("retired branch survived in %s", repository)
		}
	}
	files, err := os.ReadDir(receiptDirectory)
	if err != nil || len(files) != 1 {
		t.Fatalf("retirement audits = %v, %v", files, err)
	}
	data, err := os.ReadFile(filepath.Join(receiptDirectory, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var receipt cleanupMissingHistoryReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Type != "legacy_missing_history_retirement" || receipt.HeadSHA != head || receipt.Projection != projection ||
		receipt.RecordedAt.IsZero() || receipt.ManifestSHA256 == "" || receipt.RemoteTargetSHA == "" || !strings.Contains(receipt.MissingHistory, "unavailable") {
		t.Fatalf("incomplete retirement audit: %+v", receipt)
	}
	for _, path := range []string{claimPath, filepath.Join(runPath, "terminals", projection.ClaimID+".json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("apply synthesized historical claim/terminal at %s: %v", path, err)
		}
	}
	promptAfter, afterErr := os.ReadFile(promptPath)
	if (promptErr == nil && (afterErr != nil || string(promptAfter) != string(promptBefore))) ||
		(os.IsNotExist(promptErr) && !os.IsNotExist(afterErr)) {
		t.Fatalf("apply reconstructed or changed prompt archive: before=%v after=%v", promptErr, afterErr)
	}
}

//nolint:paralleltest // Native Git and merged-PR fixtures pin process-wide XDG_CONFIG_HOME and PATH.
func TestE2EGCRefusesMismatchedMissingWorkLogClaimBeforeApply(t *testing.T) {
	fixture, result, _, mergedAt := prepareMergedTask(t, "gc-mismatched-worklog-claim")
	installMergedPullRequestFixtures(t, nil, time.Time{})
	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	projection.ClaimID = strings.Repeat("b", 64)
	data, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, workLogProjectionDirectory, workLogProjectionName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	remoteHead := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+result.Branch)
	opts := GCOptions{SessionFreshness: DisableSessionFreshness, ProjectsRoot: fixture.projectsRoot,
		Tasks: []string{"gc-mismatched-worklog-claim"}, SkipSizes: true, DeleteRemote: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) }}
	for _, apply := range []bool{false, true} {
		opts.Apply = apply
		outcome, err := GC(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, outcome, "gc-mismatched-worklog-claim")
		if entry.Class != GCClassContained || entry.Eligible || entry.Applied || !strings.Contains(entry.Reason, "immutable manifest, projection and cleanup identity do not match") {
			t.Fatalf("apply=%v entry = %#v, want corroboration refusal", apply, entry)
		}
		if _, err := os.Stat(result.WorktreeDir); err != nil {
			t.Fatalf("apply=%v removed refused checkout: %v", apply, err)
		}
		if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+result.Branch); got != remoteHead {
			t.Fatalf("apply=%v changed refused remote branch: %s -> %s", apply, remoteHead, got)
		}
		if _, err := os.Stat(claimPath); !os.IsNotExist(err) {
			t.Fatalf("apply=%v recreated missing claim: %v", apply, err)
		}
		if _, err := os.Stat(filepath.Join(lifecycleBacklogDirectory(fixture.home), "missing-history")); !os.IsNotExist(err) {
			t.Fatalf("apply=%v wrote unsafe retirement audit: %v", apply, err)
		}
	}
}
