package sessionrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestFreshMoveRefusalsAndActualEffectsStopBeforeCheckpoint(t *testing.T) {
	t.Parallel()
	want := errors.New("move effect")
	for _, stage := range []string{"machine", "no-machine", "load", "target", "courier", "deliverer", "source", "read", "override", "checkpoint", "store", "nil-loopback"} {
		t.Run(stage, func(t *testing.T) {
			checkpointCalls := 0
			source := session.Record{PID: 41, WBSessionID: "source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
			deps := MoveDependencies{
				DefaultConfigPath: func() string { return "private" }, LocalMachine: func() (string, error) { return "source", nil },
				LoadConfig: func(string) (sessionmove.Config, error) { return cwDepsMoveConfig(cwDepsSSHTarget()), nil },
				NewDeliverer: func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
					return nil, want
				},
				ResolveSource: func(string) (session.Record, bool, error) { return source, true, nil }, LoadScanner: privateScanner,
				Checkpoint: func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
					checkpointCalls++
					return worktrees.SessionCheckpointResult{}, want
				},
			}
			request := MoveRequest{Target: "hetzner-vm1", HandoverFile: "-", Input: strings.NewReader("handover")}
			switch stage {
			case "machine":
				request.Target = ""
				deps.LocalMachine = func() (string, error) { return "", want }
			case "no-machine":
				request.Target = ""
				deps.LocalMachine = nil
			case "load":
				deps.LoadConfig = func(string) (sessionmove.Config, error) { return sessionmove.Config{}, want }
			case "target":
				deps.LoadConfig = func(string) (sessionmove.Config, error) { return sessionmove.Config{}, nil }
			case "courier":
				deps.LoadConfig = func(string) (sessionmove.Config, error) {
					return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: "target"}), nil
				}
			case "deliverer": // Actual factory failure is retained before source lookup.
			default:
				request.Target = "source"
				switch stage {
				case "source":
					deps.ResolveSource = func(string) (session.Record, bool, error) { return session.Record{}, false, want }
				case "read":
					request.HandoverFile = "/absent-private-handover"
				case "override":
					request.OverrideSecrets = []string{"invalid"}
				case "store", "nil-loopback":
					deps.Checkpoint = func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error) {
						checkpointCalls++
						return worktrees.SessionCheckpointResult{}, nil
					}
					deps.Store = func(string) (sessionmove.Store, error) {
						if stage == "store" {
							return sessionmove.Store{}, want
						}
						return sessionmove.NewStore(t.TempDir()), nil
					}
					deps.LoopbackDeliverer = func(sessionmove.Store) sessioncourier.Deliverer { return nil }
				}
			}
			_, err := NewMove(deps).Move(context.Background(), request, nil)
			if err == nil {
				t.Fatal("refusal accepted")
			}
			if stage != "checkpoint" && stage != "store" && stage != "nil-loopback" && checkpointCalls != 0 {
				t.Fatalf("later checkpoint=%d", checkpointCalls)
			}
			switch stage {
			case "machine", "load", "deliverer", "source", "checkpoint", "store":
				if !errors.Is(err, want) {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestMoveResumeRefusesInvalidDurableRouteAndDispatchWithoutMutating(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"route-read", "route-target", "route-courier", "route-write", "state-read", "dispatch-read"} {
		t.Run(stage, func(t *testing.T) {
			store := sessionmove.NewStore(t.TempDir())
			source, request, _, digest := cwDepsMoveFixture(t, store)
			deps := MoveDependencies{ResolveSource: func(string) (session.Record, bool, error) { return source, true, nil }, Store: func(string) (sessionmove.Store, error) { return store, nil }, DefaultConfigPath: func() string { return "private" }, LoadConfig: func(string) (sessionmove.Config, error) { return cwDepsMoveConfig(cwDepsSSHTarget()), nil }}
			route := moveRoute(request, digest, sessionmove.CourierSSH, cwDepsSSHTarget())
			switch stage {
			case "route-read":
				if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, "route.json"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "route-target":
				deps.LoadConfig = func(string) (sessionmove.Config, error) { return sessionmove.Config{}, nil }
			case "route-courier":
				deps.LoadConfig = func(string) (sessionmove.Config, error) {
					return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: request.TargetMachine}), nil
				}
			case "route-write":
				if err := os.Mkdir(filepath.Join(store.Root, request.HandoffID, "route.json"), 0700); err != nil {
					t.Fatal(err)
				}
			case "state-read":
				if _, _, err := store.SaveRoute(route); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, "receipt.json"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dispatch-read":
				target := cwDepsSSHTarget()
				target.Synchestra = &sessionmove.SynchestraConfig{Runner: "synchestra"}
				route = moveRoute(request, digest, sessionmove.CourierSynchestra, target)
				if _, _, err := store.SaveRoute(route); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, "synchestra-dispatch.json"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := executeResume(deps, request.HandoffID, ""); err == nil {
				t.Fatal("invalid durable state accepted")
			}
		})
	}
}
