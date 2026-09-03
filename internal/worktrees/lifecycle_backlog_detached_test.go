package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issue338Record loads the exact record sneat-dev/wb#338 found on the
// founder's machine, byte for byte: the complete cleanup journal of a detached
// pull-request review checkout. A WB that knew the detached shape (#332) wrote
// it; the installed WB, which did not, rejected it on load — and because the
// loader then failed the whole backlog on the first bad file, cleanup and
// abort of every unrelated task on the machine failed with it.
func issue338Record(t *testing.T) ([]byte, lifecycleBacklogRecord) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "issue-338-detached-backlog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record lifecycleBacklogRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	return content, record
}

// issue338RecordWithoutDetachedMarker is that same record as a WB without the
// detached shape read it: an empty branch and nothing to excuse it. It is also
// what a hand-edited or truncated record looks like — a file the validator
// rejects for the exact reason the issue quotes.
func issue338RecordWithoutDetachedMarker(t *testing.T) (string, []byte) {
	t.Helper()
	content, _ := issue338Record(t)
	var fields map[string]any
	if err := json.Unmarshal(content, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "detached")
	poisoned, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := fields["id"].(string)
	return id, append(poisoned, '\n')
}

func plantBacklogRecord(t *testing.T, home, name string, content []byte) string {
	t.Helper()
	directory := lifecycleBacklogDirectory(home)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFileUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s must be left exactly where it was: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s was rewritten:\n%s", path, got)
	}
}

func TestLifecycleBacklogAcceptsIssue338DetachedReviewRecord(t *testing.T) {
	content, record := issue338Record(t)
	if !record.Detached || record.Branch != "" || record.Stage != lifecycleStageComplete || record.Disposition != "removed" {
		t.Fatalf("fixture is not the detached, complete record the issue describes: %#v", record)
	}
	if err := validateLifecycleBacklog(record); err != nil {
		t.Fatalf("the detached review record must validate as written: %v", err)
	}

	// Planted in a fresh home, the loader must leave it exactly where it is:
	// valid, complete, nothing to resume and nothing to report.
	home := t.TempDir()
	path := plantBacklogRecord(t, home, record.ID+".json", content)
	records, quarantined, err := loadResumableLifecycleBacklog(context.Background(), home, record.ProjectsRoot,
		[]string{record.WorktreesRoot}, taskSelectionSet([]string{"unrelated-task"}), "", "removed")
	if err != nil {
		t.Fatalf("a complete detached record must not fail the loader: %v", err)
	}
	if len(records) != 0 || len(quarantined) != 0 {
		t.Fatalf("records = %#v quarantined = %#v, want nothing to resume and nothing to report", records, quarantined)
	}
	assertFileUnchanged(t, path, content)
}

func TestLifecycleBacklogRejectsIssue338RecordWithoutDetachedMarker(t *testing.T) {
	_, poisoned := issue338RecordWithoutDetachedMarker(t)
	var record lifecycleBacklogRecord
	if err := json.Unmarshal(poisoned, &record); err != nil {
		t.Fatal(err)
	}
	err := validateLifecycleBacklog(record)
	if err == nil || !strings.Contains(err.Error(), "invalid lifecycle backlog branch identity") {
		t.Fatalf("an empty branch with no detached marker must be rejected with the issue's own message, got %v", err)
	}
}

// TestLoaderReadsPastUnreadableBacklogRecords is the loader half of #338 at
// unit level: the issue's record as the installed WB read it, plain garbage,
// and a record the writer itself produced for a detached checkout share one
// directory, and none of them may fail the load. A complete record — valid or
// not — is nobody's business and is passed over in silence; a file that does
// not even decode is named. Nothing is moved or deleted.
func TestLoaderReadsPastUnreadableBacklogRecords(t *testing.T) {
	home := t.TempDir()
	// The valid neighbour is written by the writer itself, in the detached
	// shape the issue's record has, so this also pins what cleanup persists
	// for a detached checkout.
	valid := newLifecycleBacklogRecord("/home/ai/projects", ListResult{
		Task: "other-review", Repository: "sneat-dev/wb", CanonicalDir: "/home/ai/projects/sneat-dev/wb",
		WorktreesRoot: "/home/ai/.wb/worktrees", WorktreeDir: "/home/ai/.wb/worktrees/other-review/sneat-dev/wb",
		Base: "main", HeadSHA: strings.Repeat("1", 40), Detached: true,
	}, "removed")
	if err := persistLifecycleBacklog(home, &valid, lifecycleStageComplete); err != nil {
		t.Fatalf("the writer must persist a detached record: %v", err)
	}
	validContent, err := os.ReadFile(lifecycleBacklogPath(home, valid.ID))
	if err != nil {
		t.Fatal(err)
	}
	poisonedID, poisoned := issue338RecordWithoutDetachedMarker(t)
	poisonedPath := plantBacklogRecord(t, home, poisonedID+".json", poisoned)
	garbageName := strings.Repeat("f", 64) + ".json"
	garbage := []byte("{not json\n")
	garbagePath := plantBacklogRecord(t, home, garbageName, garbage)

	records, quarantined, err := loadResumableLifecycleBacklog(context.Background(), home, valid.ProjectsRoot,
		[]string{valid.WorktreesRoot}, taskSelectionSet([]string{"unrelated-task"}), "", "removed")
	if err != nil {
		t.Fatalf("unreadable records must not fail the loader: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("nothing was resumable, got %#v", records)
	}
	if len(quarantined) != 1 || quarantined[0].Path != garbagePath || quarantined[0].Task != "" ||
		!strings.HasPrefix(quarantined[0].Reason, "decode: ") {
		t.Fatalf("quarantined = %#v, want only the undecodable file named", quarantined)
	}
	assertFileUnchanged(t, lifecycleBacklogPath(home, valid.ID), validContent)
	assertFileUnchanged(t, poisonedPath, poisoned)
	assertFileUnchanged(t, garbagePath, garbage)
}

// TestCleanupOfDetachedReviewCheckoutDoesNotPoisonLaterCleanup is the
// regression #338 asked for: retire a detached pull-request review checkout,
// then retire a second, unrelated task. The first leaves a complete backlog
// record that names no branch; the second must not even notice it.
func TestCleanupOfDetachedReviewCheckoutDoesNotPoisonLaterCleanup(t *testing.T) {
	fixture, _, reviewHead, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "review-source")
	reviewTask := "pr-review-checkout"
	reviewDir := filepath.Join(fixture.home, "worktrees", reviewTask, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", reviewDir, reviewHead)
	installPerCommitPullRequestFixture(t, map[string]string{
		reviewHead: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), squashSHA, mergedAt),
	})

	reviewed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: reviewTask,
		Apply: true, IncludeDetached: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("detached review cleanup: %v", err)
	}
	if len(reviewed.Results) != 1 || !reviewed.Results[0].Applied || !reviewed.Results[0].Detached {
		t.Fatalf("detached review cleanup = %#v", reviewed.Results)
	}
	if _, statErr := os.Stat(reviewDir); !os.IsNotExist(statErr) {
		t.Fatalf("detached review checkout survived: %v", statErr)
	}
	// The writer's half of #338: the record it leaves behind is the detached
	// shape the validator accepts, and it is closed.
	record := backlogRecordForTask(t, fixture.home, reviewTask)
	if !record.Detached || record.Branch != "" || record.RemoteHeadSHA != "" || record.Stage != lifecycleStageComplete {
		t.Fatalf("detached review record = %#v, want detached, branchless and complete", record)
	}
	if err := validateLifecycleBacklog(record); err != nil {
		t.Fatalf("the record cleanup just wrote must validate: %v", err)
	}

	second, secondHead, _ := prepareMergedTaskInFixture(t, fixture, "unrelated-task")
	installMergedPullRequestFixture(t, secondHead, mergedAt)
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unrelated-task",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatalf("cleanup of an unrelated task after a detached review cleanup: %v", err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied || !outcome.Results[0].WorktreeGone {
		t.Fatalf("unrelated cleanup = %#v", outcome.Results)
	}
	if len(outcome.Quarantined) != 0 || len(outcome.Diagnostics) != 0 {
		t.Fatalf("a valid detached record is not a finding: quarantined=%#v diagnostics=%#v", outcome.Quarantined, outcome.Diagnostics)
	}
	if _, statErr := os.Stat(second.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("unrelated worktree survived: %v", statErr)
	}
}

// TestCleanupAndAbortSurviveAPoisonedBacklogRecord reproduces the founder's
// machine after #338 with the issue's own bytes: the record as the installed
// WB read it — foreign paths and all — plus an undecodable file sit in the
// backlog while unrelated tasks are retired. Cleanup and abort both succeed,
// pass the finished foreign record over in silence, name the undecodable one,
// and leave both files exactly where they were.
func TestCleanupAndAbortSurviveAPoisonedBacklogRecord(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "unrelated-finished-task")
	installMergedPullRequestFixture(t, head, mergedAt)
	poisonedID, poisoned := issue338RecordWithoutDetachedMarker(t)
	poisonedPath := plantBacklogRecord(t, fixture.home, poisonedID+".json", poisoned)
	garbageName := strings.Repeat("f", 64) + ".json"
	garbage := []byte("{not json\n")
	garbagePath := plantBacklogRecord(t, fixture.home, garbageName, garbage)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unrelated-finished-task",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("one poisoned backlog record failed an unrelated cleanup: %v", err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied || !outcome.Results[0].WorktreeGone {
		t.Fatalf("cleanup = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(created.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("unrelated worktree survived: %v", statErr)
	}
	if len(outcome.Quarantined) != 1 || outcome.Quarantined[0].Path != garbagePath || !strings.HasPrefix(outcome.Quarantined[0].Reason, "decode: ") {
		t.Fatalf("cleanup quarantine = %#v, want only the undecodable file named", outcome.Quarantined)
	}
	assertFileUnchanged(t, poisonedPath, poisoned)
	assertFileUnchanged(t, garbagePath, garbage)

	// Abort reads the same backlog for its discarded disposition and used to
	// fail on the same file.
	fixture = newGitFixture(t)
	aborted, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "abort-beside-poisoned-record", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	poisonedPath = plantBacklogRecord(t, fixture.home, poisonedID+".json", poisoned)
	garbagePath = plantBacklogRecord(t, fixture.home, garbageName, garbage)
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "abort-beside-poisoned-record", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatalf("one poisoned backlog record failed an unrelated abort: %v", err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone {
		t.Fatalf("abort = %#v", results)
	}
	if _, statErr := os.Stat(aborted[0].WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("aborted worktree survived: %v", statErr)
	}
	if len(results[0].Quarantined) != 1 || results[0].Quarantined[0].Path != garbagePath || !strings.HasPrefix(results[0].Quarantined[0].Reason, "decode: ") {
		t.Fatalf("abort quarantine = %#v, want only the undecodable file named", results[0].Quarantined)
	}
	assertFileUnchanged(t, poisonedPath, poisoned)
	assertFileUnchanged(t, garbagePath, garbage)
}

func backlogRecordForTask(t *testing.T, home, task string) lifecycleBacklogRecord {
	t.Helper()
	entries, err := os.ReadDir(lifecycleBacklogDirectory(home))
	if err != nil {
		t.Fatal(err)
	}
	var found []lifecycleBacklogRecord
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(lifecycleBacklogDirectory(home), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var record lifecycleBacklogRecord
		if err := json.Unmarshal(content, &record); err != nil {
			t.Fatal(err)
		}
		if record.Task == task {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one backlog record for %s, got %#v", task, found)
	}
	return found[0]
}
