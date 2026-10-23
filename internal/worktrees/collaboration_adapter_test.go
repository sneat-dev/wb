package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func TestCollaborationCheckoutFaultBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(root, "gitdir")
	common := filepath.Join(root, "common")
	for _, path := range []string{gitDir, common} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	baseline := defaultCollaborationCheckoutPorts()
	baseline.repositoryRoot = func(context.Context, string) (string, error) { return root, nil }
	baseline.gitDirs = func(context.Context, string) (string, string, error) { return gitDir, common, nil }
	checkout, err := baseline.resolvePathCheckout(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	baseline.home = func(string) (string, error) { return root, nil }
	baseline.openDirectory = func(string, bool) (*os.File, error) { return os.Open(root) }
	baseline.readJSON = func(_ *os.File, _ string, target any) error {
		state, err := worktreecollab.New(checkout)
		if err != nil {
			return err
		}
		if err := state.Take(worktreecollab.TakeRequest{Caller: "owner", ExpectedOwner: worktreecollab.NoOwner, At: time.Now()}); err != nil {
			return err
		}
		*target.(*worktreecollab.State) = state
		return nil
	}
	if got, err := baseline.resolve(ctx, "", checkout.ID); err != nil || got != checkout {
		t.Fatalf("ID fixture = %+v, %v", got, err)
	}
	baseline.registered = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	missingDirectory := baseline
	missingDirectory.openDirectory = func(path string, _ bool) (*os.File, error) {
		if path == filepath.Join(root, "worktree-collaboration") {
			return nil, os.ErrNotExist
		}
		return os.Open(path)
	}
	if got, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err != nil || got != checkout {
		t.Fatalf("first ID lookup without snapshot = %+v, %v", got, err)
	}
	missingSnapshot := baseline
	missingSnapshot.readJSON = func(*os.File, string, any) error { return os.ErrNotExist }
	if got, err := missingSnapshot.resolve(ctx, "projects", checkout.ID); err != nil || got != checkout {
		t.Fatalf("first ID lookup with empty directory = %+v, %v", got, err)
	}
	missingDirectory.registered = func(context.Context, string) ([]string, error) {
		return nil, errors.New("registration inventory unavailable")
	}
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "registration inventory unavailable") {
		t.Fatalf("inventory failure hidden: %v", err)
	}
	missingDirectory.registered = func(context.Context, string) ([]string, error) { return nil, nil }
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "not a registered") {
		t.Fatalf("unknown ID accepted: %v", err)
	}
	missingDirectory.registered = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	missingDirectory.gitDirs = func(context.Context, string) (string, string, error) { return common, common, nil }
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "not a registered") {
		t.Fatalf("canonical checkout accepted under linked ID: %v", err)
	}
	missingDirectory.gitDirs = baseline.gitDirs
	missingDirectory.repositoryRoot = func(context.Context, string) (string, error) { return "", os.ErrNotExist }
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "not a registered") {
		t.Fatalf("missing registered path accepted: %v", err)
	}
	missingDirectory.repositoryRoot = func(context.Context, string) (string, error) { return "", errors.New("registered checkout unreadable") }
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "registered checkout unreadable") {
		t.Fatalf("unreadable registered path ignored: %v", err)
	}
	second := t.TempDir()
	second, err = filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	missingDirectory.repositoryRoot = func(_ context.Context, path string) (string, error) { return path, nil }
	missingDirectory.openDirectory = func(path string, _ bool) (*os.File, error) {
		if path == filepath.Join(root, "worktree-collaboration") {
			return nil, os.ErrNotExist
		}
		return os.Open(path)
	}
	missingDirectory.registered = func(context.Context, string) ([]string, error) { return []string{root, second}, nil }
	if _, err := missingDirectory.resolve(ctx, "projects", checkout.ID); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous live ID accepted: %v", err)
	}
	boom := errors.New("boundary")
	for _, tc := range []struct {
		name, input string
		mutate      func(*collaborationCheckoutPorts)
	}{
		{"home", checkout.ID, func(p *collaborationCheckoutPorts) { p.home = func(string) (string, error) { return "", boom } }},
		{"ID directory", checkout.ID, func(p *collaborationCheckoutPorts) {
			p.openDirectory = func(string, bool) (*os.File, error) { return nil, boom }
		}},
		{"ID read", checkout.ID, func(p *collaborationCheckoutPorts) { p.readJSON = func(*os.File, string, any) error { return boom } }},
		{"ID malformed", checkout.ID, func(p *collaborationCheckoutPorts) {
			p.readJSON = func(_ *os.File, _ string, target any) error {
				target.(*worktreecollab.State).Checkout.ID = "bad"
				return nil
			}
		}},
		{"ID source missing", checkout.ID, func(p *collaborationCheckoutPorts) {
			p.repositoryRoot = func(context.Context, string) (string, error) { return "", boom }
		}},
		{"ID rebound", checkout.ID, func(p *collaborationCheckoutPorts) {
			p.gitDirs = func(context.Context, string) (string, string, error) { return common, gitDir, nil }
		}},
		{"repository root", root, func(p *collaborationCheckoutPorts) {
			p.repositoryRoot = func(context.Context, string) (string, error) { return "", boom }
		}},
		{"resolve root", root, func(p *collaborationCheckoutPorts) { p.resolvePath = func(string) (string, error) { return "", boom } }},
		{"open held root", root, func(p *collaborationCheckoutPorts) {
			p.openDirectory = func(string, bool) (*os.File, error) { return nil, boom }
		}},
		{"inspect held root", root, func(p *collaborationCheckoutPorts) {
			p.openDirectory = func(string, bool) (*os.File, error) { file, _ := os.Open(root); _ = file.Close(); return file, nil }
		}},
		{"Git dirs", root, func(p *collaborationCheckoutPorts) {
			p.gitDirs = func(context.Context, string) (string, string, error) { return "", "", boom }
		}},
		{"resolve git dir", root, func(p *collaborationCheckoutPorts) {
			p.gitDirs = func(context.Context, string) (string, string, error) { return "/missing-gitdir", common, nil }
		}},
		{"resolve common dir", root, func(p *collaborationCheckoutPorts) {
			p.gitDirs = func(context.Context, string) (string, string, error) { return gitDir, "/missing-common", nil }
		}},
		{"canonical clone", root, func(p *collaborationCheckoutPorts) {
			p.gitDirs = func(context.Context, string) (string, string, error) { return common, common, nil }
		}},
		{"reinspect path", root, func(p *collaborationCheckoutPorts) { p.stat = func(string) (os.FileInfo, error) { return nil, boom } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports := baseline
			tc.mutate(&ports)
			if _, err := ports.resolve(ctx, "", tc.input); err == nil {
				t.Fatal("faulty identity accepted")
			}
		})
	}
}

func TestCollaborationLegacyObservationFaultsAndFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ports := defaultCollaborationLegacyPorts()
	ports.openLocal = func(string, bool) (*os.File, error) { return os.Open(root) }
	ports.lock = func(*os.File) (func(), error) { return func() {}, nil }
	ports.readEvents = func(*os.File) ([]worktreejournal.LocalWorkLogEvent, bool, error) { return nil, false, nil }
	ports.readManifest = func(string) (Manifest, error) { return Manifest{}, errManifestNotFound }
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.ID != "" {
		t.Fatalf("no legacy owner = %+v, %v", observed, err)
	}
	boom := errors.New("boundary")
	for _, tc := range []struct {
		name   string
		mutate func(*collaborationLegacyPorts)
	}{
		{"open", func(p *collaborationLegacyPorts) {
			p.openLocal = func(string, bool) (*os.File, error) { return nil, boom }
		}},
		{"lock", func(p *collaborationLegacyPorts) { p.lock = func(*os.File) (func(), error) { return nil, boom } }},
		{"read", func(p *collaborationLegacyPorts) {
			p.readEvents = func(*os.File) ([]worktreejournal.LocalWorkLogEvent, bool, error) { return nil, false, boom }
		}},
		{"torn", func(p *collaborationLegacyPorts) {
			p.readEvents = func(*os.File) ([]worktreejournal.LocalWorkLogEvent, bool, error) { return nil, true, nil }
		}},
		{"manifest", func(p *collaborationLegacyPorts) {
			p.readManifest = func(string) (Manifest, error) { return Manifest{}, boom }
		}},
	} {
		copy := ports
		tc.mutate(&copy)
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := copy.observe(root, "sessions", true); err == nil {
				t.Fatal("legacy observation fault accepted")
			}
		})
	}
	ports.readManifest = func(string) (Manifest, error) { return Manifest{EffortID: "task", RunID: "run", AgentID: "agent"}, nil }
	if observed, err := ports.observe(root, "sessions", true); err != nil || !strings.HasPrefix(observed.ID, "legacy-") || observed.Status != "unknown" {
		t.Fatalf("immutable manifest owner = %+v, %v", observed, err)
	}
	ports.readManifest = func(string) (Manifest, error) { return Manifest{}, nil }
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.ID != "" {
		t.Fatalf("manifest without declared owner = %+v, %v", observed, err)
	}
	event := worktreejournal.LocalWorkLogEvent{ID: "owner-event", Owner: &worktreejournal.OwnerRegistration{Agent: "codex/test", PID: 55, At: time.Now()}}
	ports.readEvents = func(*os.File) ([]worktreejournal.LocalWorkLogEvent, bool, error) {
		return []worktreejournal.LocalWorkLogEvent{event, {}}, false, nil
	}
	ports.processAlive = func(int) bool { return false }
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.Status != "inactive" {
		t.Fatalf("inactive journal owner = %+v, %v", observed, err)
	}
	event.Owner.PID = 0
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.Status != "unknown" || observed.ID == "" {
		t.Fatalf("owner without PID must remain uncertain = %+v, %v", observed, err)
	}
	event.Owner.PID = 55
	ports.processAlive = func(int) bool { return true }
	ports.lookupExact = func(string, int) (session.Record, bool, error) { return session.Record{}, false, boom }
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.Status != "unknown" {
		t.Fatalf("unmapped journal owner = %+v, %v", observed, err)
	}
	ports.lookupExact = func(string, int) (session.Record, bool, error) {
		return session.Record{Runtime: "codex", NativeHarnessID: "test", WBSessionID: "registered", StartedAt: event.Owner.At.Add(-time.Second)}, true, nil
	}
	if observed, err := ports.observe(root, "sessions", true); err != nil || observed.SessionID != "registered" || observed.Status != "live" {
		t.Fatalf("registered journal owner = %+v, %v", observed, err)
	}
}

func TestCollaborationRegistrationScanRejectsUnreadableRoots(t *testing.T) {
	t.Parallel()
	if _, err := registeredCollaborationWorktrees(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing projects root accepted")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme", "app", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := registeredCollaborationWorktrees(context.Background(), root); err == nil || !strings.Contains(err.Error(), "list registered") {
		t.Fatalf("unreadable canonical registry accepted: %v", err)
	}
}
