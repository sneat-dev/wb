package worktreerun

import (
	"context"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"os"
	"path/filepath"
	"testing"
)

func TestCollaborationNativeDefaultsBindPrivateRegisteredSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, ".wb", session.DirName)
	record, err := session.Register(directory, session.Record{PID: os.Getpid(), Runtime: "codex", WBSessionID: "wbs-collaboration"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborationService(root)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := service.Ports.Caller(); err != nil || id != record.WBSessionID {
		t.Fatalf("caller=%s err=%v", id, err)
	}
	if live, err := service.Ports.Live(record.WBSessionID); err != nil || !live {
		t.Fatalf("live=%t err=%v", live, err)
	}
	if status, err := service.Ports.OwnerStatus(record.WBSessionID); err != nil || status != "live" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if _, err := service.Ports.Resolve(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing native checkout accepted")
	}
	_, _ = service.Ports.ObserveLegacyForInspection(worktreecollab.Checkout{Root: root})
	if _, err := service.Ports.NewMessageID(); err != nil {
		t.Fatal(err)
	}
	if service.Ports.Now().IsZero() {
		t.Fatal("native clock missing")
	}
}
