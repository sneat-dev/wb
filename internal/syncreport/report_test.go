package syncreport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateInGitDBAcceptsGeneratedCollection(t *testing.T) {
	if _, err := exec.LookPath("ingitdb"); err != nil {
		t.Skip("ingitdb is not installed")
	}
	raw := strings.Replace(strings.Replace(validRecord, "sync-20260908T145950Z", "sync-a", 1), "sneat-co/schoolus", "acme/app", 1)
	record, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	record.Raw = []byte(raw)
	directory := t.TempDir()
	if err := Install(directory, Report{ID: "sync-a", Records: []Record{record}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(context.Background(), directory); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("ingitdb", "list", "collections", "--path", directory)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != CollectionID {
		t.Fatalf("list collections = %q, %v", output, err)
	}
}

const validRecord = `---
schema_version: 1
report_id: sync-20260908T145950Z
repository: sneat-co/schoolus
finding: unpushed_commits
severity: attention
state: open
observed_at: "2026-09-08T14:59:50Z"
head_sha: 4afefc42745fe329f2ca4a32388a337096b55cdb
title: Six unpushed commits
suggested_action: Finish active worktrees before cleanup.
---
## Analysis

The commits belong to active work and must be preserved.
`

func TestLoadDirectoryReturnsSortedDedicatedRecords(t *testing.T) {
	dir := t.TempDir()
	second := strings.Replace(validRecord, "sneat-co/schoolus", "sneat-co/debtus", 1)
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte(validRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := LoadDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.ID != "sync-20260908T145950Z" || len(report.Records) != 2 || report.Records[0].Repository != "sneat-co/debtus" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestLoadDirectoryRejectsMixedRunsAndDuplicateRepositories(t *testing.T) {
	for name, second := range map[string]string{
		"mixed":     strings.Replace(validRecord, "sync-20260908T145950Z", "other-run", 1),
		"duplicate": validRecord,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "one.md"), []byte(validRecord), 0o600)
			_ = os.WriteFile(filepath.Join(dir, "two.md"), []byte(second), 0o600)
			if _, err := LoadDirectory(dir); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}

func TestParseRejectsUnknownFrontmatterAndMissingBody(t *testing.T) {
	if _, err := Parse([]byte(strings.Replace(validRecord, "title:", "unknown: value\ntitle:", 1))); err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
	cut := validRecord[:strings.Index(validRecord, "## Analysis")]
	if _, err := Parse([]byte(cut)); err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Fatalf("missing body error = %v", err)
	}
}

func TestRecordNameCannotCreateDirectories(t *testing.T) {
	got := RecordName("sync-1", "sneat-co/schoolus")
	if got != "sync-1--sneat-co%2Fschoolus.md" || strings.ContainsAny(got, `/\\`) {
		t.Fatalf("RecordName = %q", got)
	}
}

func TestMergeRootCollectionsPreservesExistingRegistrations(t *testing.T) {
	existing := []byte("# user collections\ntasks: data/tasks\n")
	merged, changed, err := MergeRootCollections(existing)
	if err != nil || !changed {
		t.Fatalf("MergeRootCollections = (%q, %t, %v)", merged, changed, err)
	}
	want := "# user collections\ntasks: data/tasks\nsync_reports: sync-reports\n"
	if string(merged) != want {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
	again, changed, err := MergeRootCollections(merged)
	if err != nil || changed || string(again) != want {
		t.Fatalf("idempotent merge = (%q, %t, %v)", again, changed, err)
	}
}

func TestMergeRootCollectionsRefusesAnIncompatibleRegistration(t *testing.T) {
	if _, _, err := MergeRootCollections([]byte("sync_reports: other/path\n")); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("error = %v", err)
	}
}
