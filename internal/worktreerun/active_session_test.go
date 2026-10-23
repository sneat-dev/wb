package worktreerun

import (
	"github.com/sneat-dev/wb/internal/session"
	"os"
	"path/filepath"
	"testing"
)

func TestActiveSessionInventoryUsesNativeProjectStateAndReportsBadRoot(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	views, err := listActiveSessions(projects)
	if err != nil || len(views) != 0 {
		t.Fatalf("empty sessions=%v err=%v", views, err)
	}
	record, err := session.Register(filepath.Join(projects, ".wb", session.DirName), session.Record{PID: os.Getpid(), Runtime: "codex", WBSessionID: "wbs-active-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	views, err = listActiveSessions(projects)
	if err != nil || len(views) != 1 || views[0].WBSessionID != record.WBSessionID {
		t.Fatalf("native sessions=%v err=%v", views, err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listActiveSessions(filepath.Join(blocker, "projects")); err == nil {
		t.Fatal("unresolvable projects root accepted")
	}
	deps := DefaultActiveDependencies(ActiveRemoteDependencies{})
	if deps.Claims == nil || deps.Sessions == nil {
		t.Fatal("missing native inventories")
	}
}
