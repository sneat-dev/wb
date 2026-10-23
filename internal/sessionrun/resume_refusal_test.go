package sessionrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type parkedResumeRefusalFixture struct {
	root, config string
	store        sessionpark.Store
	lock         *sessionpark.SourceLock
	state        sessionpark.State
}

func newParkedResumeRefusalFixture(t *testing.T) parkedResumeRefusalFixture {
	t.Helper()
	root := t.TempDir()
	store := sessionpark.NewStore(filepath.Join(root, ".wb", sessionpark.SourceDirName))
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-refusal", Source: session.Record{
		PID: 41, WBSessionID: "wbs-source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC(),
	}, Continuation: "private context", Worktrees: []sessionpark.Worktree{cleanParkedWorktree(filepath.Join(root, "checkout"), "topic")}, ParkedAt: time.Unix(11, 0).UTC()}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	state, err := store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "wb.yaml")
	if err := os.WriteFile(config, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: target.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return parkedResumeRefusalFixture{root: root, store: store, lock: lock, state: state, config: config}
}

func TestSessionResumeRemoteRefusesInvalidCourierAndCompetingWinner(t *testing.T) {
	t.Parallel()
	fixture := newParkedResumeRefusalFixture(t)
	state := fixture.state
	state.Status = sessionpark.StatusResumed
	state.ResumeRoute = &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteLocal}
	for _, tc := range []struct {
		name, via, want string
		state           sessionpark.State
	}{
		{"unsupported courier", "http", "unsupported resume courier", fixture.state},
		{"local winner", "ssh", "different local or remote winner", state},
	} {
		//nolint:paralleltest // Rows reuse the same acquired SourceLock and durable parked-session aggregate.
		t.Run(tc.name, func(t *testing.T) {
			_, err := resumeParkedRemote(fixture.root, context.Background(), ResumeDependencies{}, fixture.store, fixture.lock, tc.state, "target", tc.via, fixture.config, time.Unix(20, 0), io.Discard, filepath.Join(fixture.root, ".wb"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSessionResumeRemoteRefusesUnreconstructableBundleAndMissingTransport(t *testing.T) {
	t.Parallel()
	fixture := newParkedResumeRefusalFixture(t)
	state := fixture.state
	state.Bundle.Worktrees = []sessionpark.Worktree{{WorktreeDir: "/tmp/dirty-member", Dirty: true}}
	_, err := resumeParkedRemote(fixture.root, context.Background(), ResumeDependencies{}, fixture.store, fixture.lock, state, "target", "ssh", fixture.config, time.Unix(20, 0), io.Discard, filepath.Join(fixture.root, ".wb"))
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty bundle error = %v", err)
	}
	_, err = resumeParkedRemote(fixture.root, context.Background(), ResumeDependencies{}, fixture.store, fixture.lock, fixture.state, "target", "ssh", fixture.config, time.Unix(20, 0), io.Discard, filepath.Join(fixture.root, ".wb"))
	if err == nil || !strings.Contains(err.Error(), "dependencies are unavailable") {
		t.Fatalf("missing transport error = %v", err)
	}
	state, err = fixture.store.LoadUnderLock(fixture.lock)
	if err != nil {
		t.Fatal(err)
	}
	if state.ResumeRoute != nil {
		t.Fatalf("refusal claimed remote route: %+v", state.ResumeRoute)
	}
}

func TestSessionResumeRemotePropagatesCustodyAndDeliveryFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"custody", "delivery"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newParkedResumeRefusalFixture(t)
			want := errors.New("remote " + stage + " unavailable")
			deps := ResumeDependencies{
				WithRemoteCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error {
					if stage == "custody" {
						return want
					}
					return proceed()
				},
				DeliverSSH: func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error) {
					return sessionparkcourier.Result{}, want
				},
			}
			_, err := resumeParkedRemote(fixture.root, context.Background(), deps, fixture.store, fixture.lock, fixture.state, "target", "ssh", fixture.config, time.Unix(20, 0), io.Discard, filepath.Join(fixture.root, ".wb"))
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			state, err := fixture.store.LoadUnderLock(fixture.lock)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "custody" && state.ResumeRoute != nil {
				t.Fatalf("custody refusal claimed route: %+v", state.ResumeRoute)
			}
		})
	}
}

func TestSessionResumeLocalRefusesRemoteWinnerAndMissingLauncher(t *testing.T) {
	t.Parallel()
	fixture := newParkedResumeRefusalFixture(t)
	state := fixture.state
	state.Status = sessionpark.StatusResumed
	state.ResumeRoute = &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteRemote, TargetMachine: "target"}
	_, err := resumeParkedLocal(fixture.root, context.Background(), ResumeDependencies{}, fixture.store, fixture.lock, state, time.Unix(20, 0))
	if err == nil || !strings.Contains(err.Error(), "remote winner") {
		t.Fatalf("remote winner error = %v", err)
	}
	_, err = resumeParkedLocal(fixture.root, context.Background(), ResumeDependencies{}, fixture.store, fixture.lock, fixture.state, time.Unix(20, 0))
	if err == nil || !strings.Contains(err.Error(), "dependencies are unavailable") {
		t.Fatalf("missing launcher error = %v", err)
	}
}

func TestSessionResumeLocalPropagatesCustodyFailureWithoutClaimingRoute(t *testing.T) {
	t.Parallel()
	fixture := newParkedResumeRefusalFixture(t)
	want := errors.New("local custody unavailable")
	deps := ResumeDependencies{
		WithLocalCustody: func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error {
			return want
		},
		AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error { return nil },
		StartLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			return sessionlaunch.Result{}, nil
		},
		InspectLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			return sessionlaunch.Result{}, nil
		},
		InspectPrepared: func(context.Context, sessionlaunch.Options) (string, error) { return "", nil },
	}
	_, err := resumeParkedLocal(fixture.root, context.Background(), deps, fixture.store, fixture.lock, fixture.state, time.Unix(20, 0))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	state, err := fixture.store.LoadUnderLock(fixture.lock)
	if err != nil {
		t.Fatal(err)
	}
	if state.ResumeRoute != nil {
		t.Fatalf("custody refusal claimed local route: %+v", state.ResumeRoute)
	}
}

func TestParkedRemoteSSHConfigRetainsRouteAndRejectsChangedConfiguration(t *testing.T) {
	t.Parallel()
	fixture := newParkedResumeRefusalFixture(t)
	route := &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteRemote, TargetMachine: "target", Courier: "ssh", SSHHost: "target.example"}
	retained, err := parkedRemoteSSHConfig(route, "target", "ssh", "")
	if err != nil || retained.Host != "target.example" {
		t.Fatalf("retained = %+v, error = %v", retained, err)
	}
	matched, err := parkedRemoteSSHConfig(route, "target", "ssh", fixture.config)
	if err != nil || matched != retained {
		t.Fatalf("matched = %+v, error = %v", matched, err)
	}
	drifted := filepath.Join(fixture.root, "drifted.yaml")
	if err := os.WriteFile(drifted, []byte("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: other.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parkedRemoteSSHConfig(route, "target", "ssh", drifted); err == nil || !strings.Contains(err.Error(), "differs from the retained route") {
		t.Fatalf("drift error = %v", err)
	}
}

func cleanParkedWorktree(path, branch string) sessionpark.Worktree {
	head := strings.Repeat("a", 40)
	return sessionpark.Worktree{
		Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
		WorktreeDir: path, Branch: branch, Head: head, RemoteHead: head,
		WorkLogReference: "worklog:parked/remote/" + strings.Repeat("b", 64),
		OwnerEventID:     strings.Repeat("c", 64),
	}
}
