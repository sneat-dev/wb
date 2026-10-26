//go:build e2e

package gitrepo

import (
	"context"
	"github.com/sneat-dev/wb/internal/syncreport"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A sync report commits into the user's own repository, so its pre-commit
// hook (secret scanning and the like) runs and can refuse: the refusal fails
// the publish, the rollback removes the files it wrote, and nothing is left
// staged in the shared index (wb#899).
//
//nolint:paralleltest // Git fixtures override process-wide author, committer, and maintenance environment.
func TestE2EPublishSyncReportRunsPreCommitAndRollsBackARefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts rely on a POSIX shell")
	}
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	hooks := filepath.Join(p.opts.ClonePath, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho 'secret scan: refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := p.PublishSyncReport(context.Background(), syncReportFixture(t), dqCovNoopValidate)
	if err == nil || !strings.Contains(err.Error(), "secret scan: refused") {
		t.Fatalf("PublishSyncReport with a refusing pre-commit hook = %v, want the hook's refusal", err)
	}
	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("clone status after a failed commit = %q, want clean", status)
	}
	for _, relative := range []string{syncreport.SettingsPath, syncreport.RootCollectionsPath, syncreport.DefinitionPath} {
		if _, statErr := os.Stat(filepath.Join(p.opts.ClonePath, filepath.FromSlash(relative))); !os.IsNotExist(statErr) {
			t.Fatalf("rollback left %s in the working tree: stat err = %v", relative, statErr)
		}
	}
}
