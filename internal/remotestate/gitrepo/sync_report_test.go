package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/syncreport"
)

func syncReportFixture(t *testing.T) syncreport.Report {
	t.Helper()
	raw := []byte(`---
schema_version: 1
report_id: sync-20260908T145950Z
repository: acme/app
finding: unpushed_commits
severity: attention
state: open
observed_at: 2026-09-08T14:59:50Z
head_sha: 0123456789abcdef0123456789abcdef01234567
title: Local commits need review
suggested_action: Inspect and publish or retire them.
---
## Evidence

The local branch has two commits not present upstream.
`)
	record, err := syncreport.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	record.Raw = raw
	return syncreport.Report{ID: record.ReportID, Records: []syncreport.Record{record}}
}

func TestPublishSyncReportRestoresFilesWhenRepositoryValidationFails(t *testing.T) {
	origin := bareOrigin(t)
	provider := machine(t, origin)
	_, err := provider.PublishSyncReport(context.Background(), syncReportFixture(t), func(context.Context, string) error {
		return errors.New("invalid collection")
	})
	if err == nil || !strings.Contains(err.Error(), "invalid collection") {
		t.Fatalf("error = %v", err)
	}
	if status := gitIn(t, provider.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("validation failure left checkout dirty: %q", status)
	}
	if files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "sync-reports") {
		t.Fatalf("validation failure changed origin: %q", files)
	}
}

func TestPublishSyncReportRefusesToRewriteAnImmutableRecord(t *testing.T) {
	origin := bareOrigin(t)
	provider := machine(t, origin)
	report := syncReportFixture(t)
	if _, err := provider.PublishSyncReport(context.Background(), report, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	report.Records[0].Raw = append(append([]byte(nil), report.Records[0].Raw...), []byte("changed\n")...)
	if _, err := provider.PublishSyncReport(context.Background(), report, func(context.Context, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublishSyncReportValidatesCommitsAndPushesRecords(t *testing.T) {
	origin := bareOrigin(t)
	provider := machine(t, origin)
	validated := false
	result, err := provider.PublishSyncReport(context.Background(), syncReportFixture(t), func(_ context.Context, root string) error {
		validated = true
		for _, path := range []string{syncreport.SettingsPath, syncreport.RootCollectionsPath, syncreport.DefinitionPath, "sync-reports/$records/sync-20260908T145950Z--acme%2Fapp.md"} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				t.Errorf("assembled checkout lacks %s: %v", path, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !validated {
		t.Fatal("assembled checkout was not validated")
	}
	if len(result.CommitSHA) != 40 {
		t.Fatalf("commit SHA = %q", result.CommitSHA)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "sync-reports/$records/sync-20260908T145950Z--acme%2Fapp.md") {
		t.Fatalf("origin files = %q", files)
	}
	if message := gitIn(t, origin, "log", "-1", "--format=%s", "main"); message != "wb: publish sync report sync-20260908T145950Z (1 repositories)" {
		t.Fatalf("commit message = %q", message)
	}
}
