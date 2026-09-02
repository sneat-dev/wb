package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
)

// syncReportHome pins WB_HOME at a temporary directory. Every test here must
// use it: without it the writer targets the developer's real ~/.wb.
func syncReportHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	return home
}

func syncReportMetaForTest() fleetsync.RunMeta {
	return fleetsync.RunMeta{
		StartedAt:    time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC),
		ProjectsRoot: "/home/ai/projects",
		Scanned:      3,
	}
}

func TestWriteSyncIssuesReportWritesToWBHome(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	path := filepath.Join(home, "last-sync-issues.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(contents), "# WB sync issues") {
		t.Errorf("unexpected contents:\n%s", contents)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("path not announced on stdout: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
}

func TestWriteSyncIssuesReportOverwritesRatherThanAppends(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer
	path := filepath.Join(home, "last-sync-issues.md")

	results := []fleetsync.Result{{
		Repo:   discover.Repo{Org: "o", Name: "r", Path: "/p/o/r"},
		Status: fleetsync.Failed,
		Err:    errors.New("boom"),
	}}
	writeSyncIssuesReport(syncReportMetaForTest(), results, "/home/ai/projects", &out, &errOut)
	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if strings.Contains(string(contents), "boom") {
		t.Errorf("second run must replace the first, not append:\n%s", contents)
	}
	if strings.Count(string(contents), "# WB sync issues") != 1 {
		t.Errorf("report written more than once:\n%s", contents)
	}
}

func TestWriteSyncIssuesReportLeavesNoTemporaryFileAfterASuccessfulWrite(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-sync-issues-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
	// The failure path is covered by TestWriteSyncIssuesReportRemovesItsTemporaryFileWhenTheRenameFails.
}

func TestWriteSyncIssuesReportRemovesItsTemporaryFileWhenTheRenameFails(t *testing.T) {
	home := syncReportHome(t)
	// A directory sitting at the report's own path makes os.Rename fail
	// *after* the temporary file exists, which is the only situation in which
	// the deferred cleanup is what removes it.
	if err := os.MkdirAll(filepath.Join(home, "last-sync-issues.md"), 0o755); err != nil {
		t.Fatalf("stage the blocked destination: %v", err)
	}
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	if errOut.Len() == 0 {
		t.Fatal("renaming onto a directory should have failed and been reported")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-sync-issues-") {
			t.Errorf("temporary file survived a failed rename: %s", entry.Name())
		}
	}
}

func TestWriteSyncIssuesReportWarnsWithoutFailingWhenHomeIsUnwritable(t *testing.T) {
	home := syncReportHome(t)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	var out, errOut bytes.Buffer

	writeSyncIssuesReport(syncReportMetaForTest(), nil, "/home/ai/projects", &out, &errOut)

	if errOut.Len() == 0 {
		t.Skip("this filesystem allowed the write; ordering is asserted by the happy path instead")
	}
	if !strings.Contains(errOut.String(), "sync issues report not written") {
		t.Errorf("failure not warned about: %q", errOut.String())
	}
}

func TestFinishSyncWritesReportEvenWhenARepositoryFailed(t *testing.T) {
	home := syncReportHome(t)
	var out, errOut bytes.Buffer

	results := []fleetsync.Result{{
		Repo:   discover.Repo{Org: "o", Name: "broken", Path: "/p/o/broken"},
		Status: fleetsync.Failed,
		Err:    errors.New("git pull: transport failure"),
	}}
	code := finishSync(syncReportMetaForTest(), results, false, false, remoteDeps{},
		t.TempDir(), "", 1, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	contents, err := os.ReadFile(filepath.Join(home, "last-sync-issues.md"))
	if err != nil {
		t.Fatalf("a run with errors must still produce a report: %v", err)
	}
	if !strings.Contains(string(contents), "transport failure") {
		t.Errorf("error not reported:\n%s", contents)
	}
}

func TestFinishSyncReportFailureDoesNotChangeExitCode(t *testing.T) {
	home := syncReportHome(t)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	var out, errOut bytes.Buffer

	code := finishSync(syncReportMetaForTest(), nil, false, false, remoteDeps{},
		t.TempDir(), "", 1, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: an unwritable report must not fail a clean sync", code)
	}
	if !strings.Contains(errOut.String(), "sync issues report not written") {
		t.Errorf("failure not warned about: %q", errOut.String())
	}
}
