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

// dqCovSecondSyncReport returns a report that shares the fixture's report ID
// but targets a different repository, so its record path is new and its
// contents differ from anything already published.
func dqCovSecondSyncReport(t *testing.T) syncreport.Report {
	t.Helper()
	report := syncReportFixture(t)
	report.Records[0].Repository = "acme/other"
	report.Records[0].Raw = []byte("second record body\n")
	return report
}

func dqCovNoopValidate(context.Context, string) error { return nil }

func TestDQCovPublishSyncReportReportsLockFailure(t *testing.T) {
	t.Parallel()
	p := New(Options{ClonePath: dqCovBlockedLockClonePath(t), CloneURL: "file:///nowhere"})

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "create wb-state lock directory") {
		t.Fatalf("PublishSyncReport = %v, want the lock-directory cause", err)
	}
}

func TestDQCovPublishSyncReportReportsFetchFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such-origin")
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: missing})

	if _, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate); err == nil {
		t.Fatal("PublishSyncReport against an unusable clone succeeded, want an error")
	}
}

func TestDQCovPublishSyncReportReportsStatusFailure(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate); err == nil {
		t.Fatal("PublishSyncReport with an unreadable index succeeded, want an error")
	}
}

func TestDQCovPublishSyncReportRefusesDirtyCheckout(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "unrelated.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "not safe to update") {
		t.Fatalf("PublishSyncReport = %v, want the dirty-checkout refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(p.opts.ClonePath, syncreport.SettingsPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refused publish wrote schema files: stat err = %v", statErr)
	}
}

func TestDQCovPublishSyncReportRestoresOverwrittenFiles(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, syncreport.RootCollectionsPath)); err != nil {
		t.Fatalf("first publish did not create root collections: %v", err)
	}

	sentinel := errors.New("dqCov validation refused")
	_, err := p.PublishSyncReport(context.Background(), dqCovSecondSyncReport(t), func(context.Context, string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("second PublishSyncReport = %v, want the validation error", err)
	}
	// Rollback must restore every pre-existing file byte-for-byte and remove
	// the new record.
	settings, readErr := os.ReadFile(filepath.Join(p.opts.ClonePath, syncreport.SettingsPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(settings) != syncreport.SettingsYAML {
		t.Fatalf("settings after rollback = %q, want %q", settings, syncreport.SettingsYAML)
	}
	roots, readErr := os.ReadFile(filepath.Join(p.opts.ClonePath, syncreport.RootCollectionsPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(roots), "sync_reports: sync-reports") {
		t.Fatalf("root collections after rollback = %q, want the published mapping", roots)
	}
	newRecord := filepath.Join(p.opts.ClonePath, filepath.FromSlash(syncreport.CollectionRecords+"/"+syncreport.RecordName("sync-20260908T145950Z", "acme/other")))
	if _, statErr := os.Stat(newRecord); !os.IsNotExist(statErr) {
		t.Fatalf("rollback left the new record behind: stat err = %v", statErr)
	}
	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("rollback left the checkout dirty: %q", status)
	}
}

// TestDQCovPublishSyncReportMergesExistingRootCollections proves an existing
// root-collections file that does not yet register this collection is merged
// with it rather than replaced.
func TestDQCovPublishSyncReportMergesExistingRootCollections(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushFileToRef(t, origin, syncreport.RootCollectionsPath, []byte("other_collection: other\n"), "refs/heads/main", "pre-existing collections")

	if _, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate); err != nil {
		t.Fatal(err)
	}
	merged := gitIn(t, origin, "show", "main:"+syncreport.RootCollectionsPath)
	if !strings.Contains(merged, "other_collection: other") {
		t.Fatalf("merged root collections = %q, want the pre-existing collection preserved", merged)
	}
	if !strings.Contains(merged, "sync_reports: sync-reports") {
		t.Fatalf("merged root collections = %q, want the sync-reports mapping added", merged)
	}
}

func TestDQCovPublishSyncReportReportsRootCollectionsMergeError(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushFileToRef(t, origin, syncreport.RootCollectionsPath, []byte("{{{ not yaml\n"), "refs/heads/main", "broken root collections")

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), syncreport.RootCollectionsPath) {
		t.Fatalf("PublishSyncReport = %v, want a root-collections decode error", err)
	}
}

func TestDQCovPublishSyncReportRefusesIncompatibleDefinition(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushFileToRef(t, origin, syncreport.DefinitionPath, []byte("titles:\n  en: something else\n"), "refs/heads/main", "incompatible definition")

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("PublishSyncReport = %v, want an incompatible-definition error", err)
	}
}

func TestDQCovPublishSyncReportReportsSchemaReadFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushSymlinkToRef(t, origin, syncreport.SettingsPath, ".", "refs/heads/main", "settings is a directory link")

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil {
		t.Fatal("PublishSyncReport with an unreadable settings path succeeded, want an error")
	}
}

func TestDQCovPublishSyncReportReportsMkdirFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushSymlinkToRef(t, origin, syncreport.CollectionRecords, "/nonexistent/dq-records", "refs/heads/main", "records dir is a dangling link")

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil {
		t.Fatal("PublishSyncReport with an uncreatable records directory succeeded, want an error")
	}
	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("failed publish left the checkout dirty: %q", status)
	}
}

func TestDQCovPublishSyncReportReportsWriteFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushSymlinkToRef(t, origin, syncreport.SettingsPath, "/nonexistent/dq-dir/settings.yaml", "refs/heads/main", "settings is a dangling link")

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil {
		t.Fatal("PublishSyncReport with an unwritable settings path succeeded, want an error")
	}
}

func TestDQCovPublishSyncReportReportsAddCommitFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	dqCovInstallFailingCommitHook(t, p.opts.ClonePath)

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil {
		t.Fatal("PublishSyncReport with a refusing commit hook succeeded, want an error")
	}
	// The rollback removes the files it wrote from the working tree. (It does
	// not unstage them: AddCommit's `git add` already succeeded, so the index
	// keeps the staged entries — that is existing behaviour, asserted here as
	// the working-tree removal it does perform.)
	for _, relative := range []string{syncreport.SettingsPath, syncreport.RootCollectionsPath, syncreport.DefinitionPath} {
		if _, statErr := os.Stat(filepath.Join(p.opts.ClonePath, filepath.FromSlash(relative))); !os.IsNotExist(statErr) {
			t.Fatalf("rollback left %s in the working tree: stat err = %v", relative, statErr)
		}
	}
}

func TestDQCovPublishSyncReportAbortsRebaseAfterRejectedPush(t *testing.T) {
	origin := bareOrigin(t)
	report := syncReportFixture(t)
	record := report.Records[0]
	relative := filepath.ToSlash(filepath.Join(syncreport.CollectionRecords, syncreport.RecordName(report.ID, record.Repository)))
	dqCovPushFileToRef(t, origin, relative, []byte("competing record body\n"), "refs/staging/competing", "competing record")
	competingSHA := gitIn(t, origin, "rev-parse", "refs/staging/competing")
	installRejectFirstPushHook(t, origin, competingSHA)

	p := machine(t, origin)
	_, err := p.PublishSyncReport(context.Background(), report, dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "rebase aborted") {
		t.Fatalf("PublishSyncReport = %v, want an aborted rebase error", err)
	}
}

func TestDQCovPublishSyncReportReportsRebaseFailureWithoutRebase(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	installRejectAlwaysPushHook(t, origin)
	p := machine(t, origin)

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "push rejected and rebase failed") {
		t.Fatalf("PublishSyncReport = %v, want 'push rejected and rebase failed'", err)
	}
}

func TestDQCovPublishSyncReportReportsSecondRejection(t *testing.T) {
	origin := bareOrigin(t)
	installRejectAlwaysPushHook(t, origin)
	p := machine(t, origin)

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "push rejected twice") {
		t.Fatalf("PublishSyncReport = %v, want 'push rejected twice'", err)
	}
}

func TestDQCovPublishSyncReportReportsHeadSHAFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	dqCovInstallRemoveClientBranchHook(t, origin, p.opts.ClonePath)

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil {
		t.Fatal("PublishSyncReport succeeded although HEAD was unreadable after the push")
	}
}
