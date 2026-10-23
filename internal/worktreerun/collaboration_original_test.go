package worktreerun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollaborationSessionPortsBindAncestorRecipientAndCheckout(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ports := defaultCollaborationSessionPorts()
	ports.root = func(string) (string, error) { return filepath.Join(root, ".wb"), nil }
	ports.pid = func() int { return 42 }
	ports.resolveCheckout = func(_ context.Context, _, value string) (worktreecollab.Checkout, error) {
		if value != "worktree" {
			return worktreecollab.Checkout{}, errors.New("unknown checkout")
		}
		return worktreecollab.Checkout{ID: "checkout", Root: root, GitDir: root + "/gitdir", CommonDir: root + "/common"}, nil
	}
	ports.observeLegacy = func(string, string) (worktreecollab.ObservedOwner, error) { return worktreecollab.ObservedOwner{}, nil }
	ports.resolveAncestor = func(string, int) (session.Record, bool) { return session.Record{}, false }
	ports.lookupExact = func(string, int) (session.Record, bool, error) { return session.Record{}, false, nil }
	ports.lookupRecipient = func(string, string) (session.Record, bool) { return session.Record{}, false }
	service, err := ports.service(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "registered ancestor") {
		t.Fatalf("unregistered caller = %v", err)
	}
	declared := session.Record{PID: 42, WBSessionID: "wbs-owner"}
	ports.resolveAncestor = func(string, int) (session.Record, bool) { return declared, true }
	service, _ = ports.service(root)
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "corroborated") {
		t.Fatalf("unverified caller = %v", err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) { return declared, true, nil }
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}}, nil
	}
	service, _ = ports.service(root)
	if id, err := service.Ports.Caller(); err != nil || id != declared.WBSessionID {
		t.Fatalf("exact caller = %q, %v", id, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 99, WBSessionID: declared.WBSessionID}}}, nil
	}
	service, _ = ports.service(root)
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate invoking session accepted: %v", err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return []session.View{{Record: declared}}, nil }
	service, _ = ports.service(root)
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("missing recipient = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	service, _ = ports.service(root)
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("listed recipient without exact lookup = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return nil, errors.New("listing unavailable") }
	service, _ = ports.service(root)
	if _, err := service.Ports.Caller(); err == nil || !strings.Contains(err.Error(), "listing unavailable") {
		t.Fatalf("caller ignored registration listing failure: %v", err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupRecipient = func(string, string) (session.Record, bool) { return session.Record{PID: 55, WBSessionID: "peer"}, true }
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}, {Record: session.Record{PID: 56, WBSessionID: "peer"}}}, nil
	}
	service, _ = ports.service(root)
	if live, err := service.Ports.Live("peer"); err != nil || live {
		t.Fatalf("duplicate recipient accepted as live: %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("exact read")
	}
	service, _ = ports.service(root)
	if _, err := service.Ports.Live("peer"); err == nil || !strings.Contains(err.Error(), "exact read") {
		t.Fatalf("recipient read failure = %v", err)
	}
	ports.lookupExact = func(_ string, pid int) (session.Record, bool, error) {
		return session.Record{PID: pid, WBSessionID: "peer"}, true, nil
	}
	service, _ = ports.service(root)
	if live, err := service.Ports.Live("peer"); err != nil || !live {
		t.Fatalf("live peer = %t, %v", live, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return []session.View{{Record: declared}}, nil }
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("missing owner evidence = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) { return nil, errors.New("session listing unavailable") }
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("uncertain owner listing incorrectly treated as inactive = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{}, false, errors.New("record unreadable")
	}
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("unreadable owner incorrectly dead = %q, %v", status, err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "other"}, false, nil
	}
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("mismatched owner incorrectly dead = %q, %v", status, err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "peer"}, false, nil
	}
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "inactive" {
		t.Fatalf("proven dead owner = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{
			{Record: declared},
			{Record: session.Record{PID: 55, WBSessionID: "peer"}},
			{Record: session.Record{PID: 56, WBSessionID: "peer"}},
		}, nil
	}
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "unknown" {
		t.Fatalf("duplicate owner registration incorrectly treated as dead = %q, %v", status, err)
	}
	ports.listSessions = func(string) ([]session.View, error) {
		return []session.View{{Record: declared}, {Record: session.Record{PID: 55, WBSessionID: "peer"}}}, nil
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{PID: 55, WBSessionID: "peer"}, true, nil
	}
	service, _ = ports.service(root)
	if status, err := service.Ports.OwnerStatus("peer"); err != nil || status != "live" {
		t.Fatalf("proven live owner = %q, %v", status, err)
	}
	if _, err := service.Ports.Resolve(context.Background(), "missing"); err == nil {
		t.Fatal("checkout resolution failure hidden")
	}
	if _, err := service.Ports.ObserveLegacy(worktreecollab.Checkout{Root: root}); err != nil {
		t.Fatal(err)
	}
	ports.root = func(string) (string, error) { return "", errors.New("home unavailable") }
	if _, err := ports.service(""); err == nil || !strings.Contains(err.Error(), "home unavailable") {
		t.Fatalf("home resolution = %v", err)
	}
	if _, err := NewCollaborationService(root); err != nil {
		t.Fatal(err)
	}
	// The lazy root factory assertion remains in the root binding gate.
}
