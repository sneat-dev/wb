package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/syncreport"
)

func writeSyncReportCommandFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	record := `---
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
`
	if err := os.WriteFile(filepath.Join(directory, "app.md"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestSyncReportValidateLoadsAndValidatesTheWholeBatch(t *testing.T) {
	directory := writeSyncReportCommandFixture(t)
	validated := false
	deps := syncReportCommandDeps{
		validate: func(_ context.Context, report syncreport.Report) error {
			validated = report.ID == "sync-20260908T145950Z" && len(report.Records) == 1
			return nil
		},
	}
	command := newSyncReportValidateCmd(deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{directory})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !validated {
		t.Fatal("validator did not receive the complete report")
	}
	if !strings.Contains(output.String(), "Validated sync report sync-20260908T145950Z (1 repository records)") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSyncReportPublishReturnsImmutableViewerURL(t *testing.T) {
	directory := writeSyncReportCommandFixture(t)
	oldRoot := projectsRoot
	projectsRoot = "/fleet"
	t.Cleanup(func() { projectsRoot = oldRoot })
	deps := syncReportCommandDeps{
		validate: func(context.Context, syncreport.Report) error { return nil },
		publish: func(_ context.Context, repository, root string, report syncreport.Report) (gitrepo.SyncReportPublishResult, error) {
			if repository != "alice/workbench" || root != "/fleet" || len(report.Records) != 1 {
				t.Fatalf("unexpected publish input: %s %s %#v", repository, root, report)
			}
			return gitrepo.SyncReportPublishResult{CommitSHA: strings.Repeat("a", 40), Paths: []string{"sync-reports/$records/x.md"}}, nil
		},
	}
	command := newSyncReportPublishCmd(deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{directory, "--repo", "alice/workbench", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var got syncReportPublishOutput
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ReportID != "sync-20260908T145950Z" || got.Repository != "alice/workbench" {
		t.Fatalf("output = %+v", got)
	}
	wantURL := "https://sneat.work/bench/app/sync-report?ref=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&repo=alice%2Fworkbench&report=sync-20260908T145950Z"
	if got.URL != wantURL {
		t.Fatalf("URL = %q, want %q", got.URL, wantURL)
	}
}
