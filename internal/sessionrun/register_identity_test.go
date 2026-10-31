package sessionrun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func TestRegistrationCorroboratesNativeChatAndTerminalBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"observed", "terminal-free", "legacy-native-alias", "legacy-terminal-refused", "terminal-error", "foreign-terminal", "own-pid", "parent-shell", "native-error", "foreign-native", "foreign-alias", "unobserved-process", "empty-observation", "register-error"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			terminal := sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(t.TempDir(), "owned.sock"), WorkspaceID: "w1", PaneID: "w1:p1"}
			record := session.Record{PID: 42, Runtime: "codex", Model: session.Unknown}
			failure := errors.New("owned observation failed")
			writes, nativeCalls := 0, 0
			deps := RegisterDependencies{
				CurrentPID: func() int { return 44 }, ParentPID: func() int { return 43 },
				RuntimeProcess: func(int, string) bool { return scenario != "parent-shell" && scenario != "unobserved-process" },
				TerminalIdentity: func() (sessionterminal.Identity, error) {
					if scenario == "terminal-error" {
						return sessionterminal.Identity{}, failure
					}
					if scenario == "terminal-free" {
						return sessionterminal.Identity{}, nil
					}
					return terminal, nil
				},
				NativeID: func(string) (string, error) {
					nativeCalls++
					if scenario == "native-error" {
						return "", failure
					}
					if scenario == "empty-observation" {
						return "", nil
					}
					return "native-current", nil
				},
				Directory: func(string) (string, error) { return "owned-registry", nil },
				Register: func(dir string, got session.Record) (session.Record, error) {
					writes++
					if dir != "owned-registry" {
						t.Fatal("wrong registry selected")
					}
					if scenario == "register-error" {
						return session.Record{}, failure
					}
					return got, nil
				},
			}
			switch scenario {
			case "legacy-native-alias":
				record.AgentID = "native-current"
			case "legacy-terminal-refused":
				record.TmuxName = "wb-session-old"
			case "foreign-terminal":
				record.Terminal = terminal
				record.Terminal.PaneID = "foreign-pane"
			case "own-pid":
				record.PID = 44
			case "parent-shell":
				record.PID = 43
			case "foreign-native":
				record.NativeHarnessID = "native-other"
			case "foreign-alias":
				record.AgentID = "native-other"
			case "unobserved-process", "empty-observation":
				record.NativeHarnessID = "explicit-native"
			}
			got, err := NewRegister(deps).Register(context.Background(), RegisterRequest{ProjectsRoot: "owned-root", Record: record})
			wantSuccess := scenario == "observed" || scenario == "terminal-free" || scenario == "legacy-native-alias" || scenario == "unobserved-process" || scenario == "empty-observation"
			if wantSuccess {
				if err != nil || writes != 1 {
					t.Fatalf("legitimate registration refused: %#v writes=%d err=%v", got, writes, err)
				}
				wantNative := "native-current"
				if scenario == "unobserved-process" || scenario == "empty-observation" {
					wantNative = "explicit-native"
				}
				if got.NativeHarnessID != wantNative || scenario != "terminal-free" && got.Terminal != terminal || scenario == "terminal-free" && !got.Terminal.IsZero() {
					t.Fatalf("incorrect corroborated identity: %#v", got)
				}
				if scenario == "unobserved-process" && nativeCalls != 0 {
					t.Fatal("unobserved process adopted current chat environment")
				}
			} else {
				if err == nil {
					t.Fatal("uncorroborated identity accepted")
				}
				wantWrites := 0
				if scenario == "register-error" {
					wantWrites = 1
				}
				if writes != wantWrites {
					t.Fatalf("refusal wrote %d registrations", writes)
				}
				if strings.HasSuffix(scenario, "-error") && !errors.Is(err, failure) {
					t.Fatalf("observation failure lost: %v", err)
				}
			}
		})
	}
}

func TestRegistrationTerminalEnvironmentRequiresAllExactCoordinates(t *testing.T) {
	for _, scenario := range []string{"complete", "socket-only", "pane-only", "missing-socket", "invalid-socket"} {
		t.Run(scenario, func(t *testing.T) {
			for _, key := range []string{"HERDR_SOCKET_PATH", "HERDR_WORKSPACE_ID", "HERDR_PANE_ID"} {
				t.Setenv(key, "")
			}
			socket := filepath.Join(t.TempDir(), "owned.sock")
			t.Setenv("HERDR_SOCKET_PATH", socket)
			if scenario != "socket-only" {
				t.Setenv("HERDR_PANE_ID", "w1:p1")
			}
			if scenario != "socket-only" && scenario != "pane-only" {
				t.Setenv("HERDR_WORKSPACE_ID", "w1")
			}
			if scenario == "missing-socket" {
				t.Setenv("HERDR_SOCKET_PATH", "")
			}
			if scenario == "invalid-socket" {
				t.Setenv("HERDR_SOCKET_PATH", "relative.sock")
			}
			got, err := observedRegisterTerminal()
			if scenario == "invalid-socket" {
				if err == nil {
					t.Fatal("relative server identity accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "complete" {
				if got.Socket != socket || got.WorkspaceID != "w1" || got.PaneID != "w1:p1" || got.Kind != "herdr" {
					t.Fatalf("complete environment changed: %#v", got)
				}
			} else if !got.IsZero() {
				t.Fatalf("partial environment invented endpoint: %#v", got)
			}
		})
	}
}
