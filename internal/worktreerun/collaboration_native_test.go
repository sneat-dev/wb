package worktreerun

import (
	"context"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestCollaborationNativeOwnerStatusSeparatesReusedPIDFromUnknownRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		started time.Time
		status  string
	}{
		{name: "current", started: time.Now().UTC(), status: "live"},
		{name: "historical semantic time", started: time.Unix(1, 0).UTC(), status: "live"},
		{name: "retired kernel instance", started: time.Unix(1, 0).UTC(), status: "inactive"},
		{name: "unknown legacy timestamp", status: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			directory := filepath.Join(root, ".wb", session.DirName)
			record, err := session.Register(directory, session.Record{PID: os.Getpid(), WBSessionID: "wbs-instance", Runtime: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			record.StartedAt = tc.started
			if tc.status == "unknown" {
				record.ProcessStartedAt = time.Time{}
			}
			if tc.status == "inactive" {
				record.ProcessStartedAt = record.ProcessStartedAt.Add(-time.Hour)
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, strconv.Itoa(record.PID)+".json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			service, err := NewCollaborationService(root)
			if err != nil {
				t.Fatal(err)
			}
			if status, err := service.Ports.OwnerStatus(record.WBSessionID); err != nil || status != tc.status {
				t.Fatalf("owner status=%q,%v; want %q", status, err, tc.status)
			}
			if live, err := service.Ports.Live(record.WBSessionID); err != nil || live != (tc.status == "live") {
				t.Fatalf("peer live=%t,%v", live, err)
			}
			caller, err := service.Ports.Caller()
			if tc.status == "live" {
				if err != nil || caller != record.WBSessionID {
					t.Fatalf("current caller=%q,%v", caller, err)
				}
			} else if err == nil {
				t.Fatalf("retired or unknown caller accepted as %q", caller)
			}
		})
	}
}
