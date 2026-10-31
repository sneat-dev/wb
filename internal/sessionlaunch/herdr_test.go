package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

type fakeLaunchHerdr struct {
	workspace                              herdr.CreatedWorkspace
	pane                                   herdr.Pane
	processes                              herdr.PaneProcesses
	agentGet                               func(context.Context, string) (herdr.Agent, error)
	createErr, getErr, processErr, execErr error
	creates, execs                         int
	executable                             string
	args                                   []string
}

func (f *fakeLaunchHerdr) WorkspaceCreate(context.Context, string, string) (herdr.CreatedWorkspace, error) {
	f.creates++
	return f.workspace, f.createErr
}
func (f *fakeLaunchHerdr) PaneGet(context.Context, string) (herdr.Pane, error) {
	return f.pane, f.getErr
}
func (f *fakeLaunchHerdr) AgentGet(ctx context.Context, pane string) (herdr.Agent, error) {
	if f.agentGet != nil {
		return f.agentGet(ctx, pane)
	}
	return herdr.Agent{}, herdr.ErrUnknownTarget
}
func (f *fakeLaunchHerdr) PaneProcessInfo(context.Context, string) (herdr.PaneProcesses, error) {
	return f.processes, f.processErr
}
func (f *fakeLaunchHerdr) PaneExec(_ context.Context, _ string, executable string, args []string) error {
	f.execs++
	f.executable, f.args = executable, append([]string(nil), args...)
	return f.execErr
}

func newHerdrLaunchFixture(t *testing.T) (*launchState, *launchAttempt, launchPlan, sessionmove.Digest, *fakeLaunchHerdr, osHerdr) {
	t.Helper()
	state, root := slCovOpenState(t)
	plan := slCovPlan(state.handoffID)
	plan.Transport, plan.TmuxName = "herdr", ""
	plan.StoreRoot = root
	plan.WorktreeDir = t.TempDir()
	plan.WBExecutable, _ = os.Executable()
	plan.HarnessExecutable = plan.WBExecutable
	plan.HarnessArgs = []string{"-test.run=^TestHerdrFixtureHarness$"}
	_, digest, _, err := state.savePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	pane := herdr.Pane{PaneID: "w1:p1", WorkspaceID: "w1"}
	client := &fakeLaunchHerdr{workspace: herdr.CreatedWorkspace{WorkspaceID: "w1", Pane: pane}, pane: pane, processes: herdr.PaneProcesses{PaneID: pane.PaneID}}
	transport := osHerdr{socket: filepath.Join(t.TempDir(), "herdr.sock"), client: func(string) (launchHerdrClient, error) { return client, nil }}
	return state, attempt, plan, digest, client, transport
}

func TestHerdrStartsOnlyFixedGatedSupervisorAndNeverResubmits(t *testing.T) {
	t.Parallel()
	_, attempt, plan, digest, client, transport := newHerdrLaunchFixture(t)
	args := []string{PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
	if err := transport.StartDetached(context.Background(), terminalAddress(plan), plan.WorktreeDir, plan.WBExecutable, args); err != nil {
		t.Fatal(err)
	}
	want := append([]string{PrivateLauncherArgument, "--supervise"}, args[1:]...)
	if client.creates != 1 || client.execs != 1 || client.executable != plan.WBExecutable || !reflect.DeepEqual(client.args, want) {
		t.Fatalf("unexpected bootstrap: %#v", client)
	}
	if err := transport.StartDetached(context.Background(), terminalAddress(plan), plan.WorktreeDir, plan.WBExecutable, args); err == nil {
		t.Fatal("ambiguous retry submitted twice")
	}
	if client.creates != 1 || client.execs != 1 {
		t.Fatal("retry created or submitted another pane")
	}
}

func TestHerdrLaunchRefusesForeignOrAmbiguousTargets(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"argv", "create-error", "foreign-pane", "existing-agent", "get-error", "lost-submit", "creation-intent"} {
		t.Run(stage, func(t *testing.T) {
			_, attempt, plan, digest, client, transport := newHerdrLaunchFixture(t)
			args := []string{PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
			switch stage {
			case "argv":
				args[0] = "arbitrary"
			case "create-error":
				client.createErr = errors.New("server stopped")
			case "foreign-pane":
				client.pane.WorkspaceID = "foreign"
			case "existing-agent":
				client.pane.AgentSession = &herdr.AgentSession{Kind: "id", Value: "unowned"}
			case "get-error":
				client.getErr = errors.New("lost pane")
			case "lost-submit":
				client.execErr = errors.New("lost reply")
			case "creation-intent":
				if _, err := attempt.publish("", "herdr-create-intent.json", []byte("{}")); err != nil {
					t.Fatal(err)
				}
			}
			if err := transport.StartDetached(context.Background(), terminalAddress(plan), plan.WorktreeDir, plan.WBExecutable, args); err == nil {
				t.Fatal("unsafe launch accepted")
			}
			if stage != "lost-submit" && client.execs != 0 {
				t.Fatal("unsafe target received input")
			}
		})
	}
}

func TestHerdrLivePIDRequiresExactPaneArgvAndNativeGeneration(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"owned-wrapper", "owned-harness", "foreign-pane", "foreign-argv", "wrong-generation", "duplicate", "unknown-target", "process-error", "no-target", "ambiguous-create"} {
		t.Run(stage, func(t *testing.T) {
			_, attempt, plan, digest, client, transport := newHerdrLaunchFixture(t)
			generation, known := session.ObserveProcessGeneration(os.Getpid())
			if !known {
				t.Fatal("missing native generation")
			}
			target := herdrTarget{launchSchemaVersion, attempt.id, digest, sessionterminal.Identity{Kind: "herdr", Socket: transport.socket, WorkspaceID: "w1", PaneID: "w1:p1"}}
			if stage != "no-target" && stage != "ambiguous-create" {
				raw, _ := encodeLaunchJSON(target)
				if _, err := attempt.publish("", "herdr-target.json", raw); err != nil {
					t.Fatal(err)
				}
			}
			child := herdrChild{launchSchemaVersion, attempt.id, digest, os.Getpid(), generation}
			if stage == "wrong-generation" {
				child.ProcessStartedAt = child.ProcessStartedAt.Add(time.Second)
			}
			raw, _ := encodeLaunchJSON(child)
			if _, err := attempt.publish("", "herdr-child.json", raw); err != nil {
				t.Fatal(err)
			}
			argv := []string{plan.WBExecutable, PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
			if stage == "owned-harness" {
				argv = append([]string{plan.HarnessExecutable}, plan.HarnessArgs...)
			}
			if stage == "foreign-argv" {
				argv = []string{"/bin/sh", "unowned"}
			}
			client.processes.Foreground = []herdr.ForegroundProcess{{PID: os.Getpid(), Name: "wb", Argv: argv}}
			switch stage {
			case "foreign-pane":
				client.pane.WorkspaceID = "other"
			case "duplicate":
				client.processes.Foreground = append(client.processes.Foreground, client.processes.Foreground[0])
			case "unknown-target":
				client.getErr = herdr.ErrUnknownTarget
			case "process-error":
				client.processErr = errors.New("unreadable process evidence")
			case "ambiguous-create":
				if _, err := attempt.publish("", "herdr-create-intent.json", []byte("{}")); err != nil {
					t.Fatal(err)
				}
			}
			pid, live, err := transport.PanePID(context.Background(), terminalAddress(plan))
			wantLive := stage == "owned-wrapper" || stage == "owned-harness"
			wantError := stage == "foreign-pane" || stage == "wrong-generation" || stage == "duplicate" || stage == "process-error" || stage == "ambiguous-create"
			if live != wantLive || (err != nil) != wantError || live && pid != os.Getpid() {
				t.Fatalf("PID evidence %s: %d %t %v", stage, pid, live, err)
			}
		})
	}
}

func TestOwnedSupervisorRetainsExactExitAndRejectsSecondOwner(t *testing.T) {
	t.Parallel()
	_, attempt, plan, digest, _, transport := newHerdrLaunchFixture(t)
	target := herdrTarget{launchSchemaVersion, attempt.id, digest, sessionterminal.Identity{Kind: "herdr", Socket: transport.socket, WorkspaceID: "w1", PaneID: "w1:p1"}}
	raw, _ := encodeLaunchJSON(target)
	if _, err := attempt.publish("", "herdr-target.json", raw); err != nil {
		t.Fatal(err)
	}
	child := herdrChild{launchSchemaVersion, attempt.id, digest, 1000000000, time.Unix(10, 0).UTC()}
	exit := herdrExit{child, 17, "bounded failure"}
	deps := supervisorDependencies{executable: func() (string, error) { return plan.WBExecutable, nil }, identity: func() herdr.Identity {
		return herdr.Identity{SocketPath: transport.socket, WorkspaceID: "w1", PaneID: "w1:p1"}
	}, child: func(launchPlan, *launchAttempt, sessionmove.Digest) (herdrExit, error) {
		raw, _ := encodeLaunchJSON(child)
		_, err := attempt.publish("", "herdr-child.json", raw)
		return exit, err
	}}
	args := []string{plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
	status, err := runHerdrSupervisor(args, deps)
	if err != nil || status != 17 {
		t.Fatalf("supervisor: %d %v", status, err)
	}
	failure, found, err := transport.PaneFailure(context.Background(), terminalAddress(plan))
	if err != nil || !found || failure.ExitStatus != 17 || failure.Diagnostic != exit.Diagnostic {
		t.Fatalf("exit evidence: %#v %t %v", failure, found, err)
	}
	if _, err := runHerdrSupervisor(args, deps); err == nil {
		t.Fatal("second supervisor owner admitted")
	}
}

func writePreflightHerdr(t *testing.T, bin string) {
	t.Helper()
	path := filepath.Join(bin, "herdr")
	body := "#!/bin/sh\ncase \"$*\" in\n--version) printf 'herdr 0.9.1\\n';;\n'pane list') printf '%s\\n' '{\"result\":{\"type\":\"pane_list\",\"panes\":[]}}';;\n*) exit 2;;\nesac\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "owned-test.sock"))
}

func TestUnknownModelMetadataNeverBecomesAHarnessArgument(t *testing.T) {
	t.Parallel()
	for _, runtime := range []string{RuntimeCodex, RuntimeClaudeCode} {
		t.Run(runtime, func(t *testing.T) {
			spec, err := harnessSpecForAuthority(sessionauthority.Launch{SourceRuntime: runtime, SourceModel: session.Unknown}, "/tmp/owned")
			if err != nil {
				t.Fatal(err)
			}
			if spec.Model != session.Unknown {
				t.Fatal("unknown metadata was discarded")
			}
			for _, arg := range spec.Args {
				if arg == "-m" || arg == "--model" || arg == session.Unknown {
					t.Fatalf("unknown model became executable argv: %#v", spec.Args)
				}
			}
		})
	}
}

func TestHerdrBindingWaitsForActiveClassificationAndRejectsContradictoryIdentity(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"delayed-active", "partial-active", "unknown-until-cancel", "pane-runtime", "active-runtime", "active-native", "active-session-runtime", "active-pane", "active-workspace", "agent-error", "foreign-owned-pid", "process-error", "native-bind-conflict"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			_, attempt, plan, digest, client, transport := newHerdrLaunchFixture(t)
			dir := t.TempDir()
			target := herdrTarget{launchSchemaVersion, attempt.id, digest, sessionterminal.Identity{Kind: "herdr", Socket: transport.socket, WorkspaceID: "w1", PaneID: "w1:p1"}}
			raw, _ := encodeLaunchJSON(target)
			if _, err := attempt.publish("", "herdr-target.json", raw); err != nil {
				t.Fatal(err)
			}
			record, err := session.Register(dir, session.Record{PID: os.Getpid(), WBSessionID: plan.SuccessorWBSessionID, Runtime: plan.Runtime, Model: plan.Model, Terminal: target.Terminal})
			if err != nil {
				t.Fatal(err)
			}
			child := herdrChild{launchSchemaVersion, attempt.id, digest, record.PID, record.ProcessStartedAt}
			raw, _ = encodeLaunchJSON(child)
			if _, err := attempt.publish("", "herdr-child.json", raw); err != nil {
				t.Fatal(err)
			}
			client.processes.Foreground = []herdr.ForegroundProcess{{PID: record.PID, Argv: append([]string{plan.HarnessExecutable}, plan.HarnessArgs...)}}
			client.pane.AgentSession = &herdr.AgentSession{Agent: plan.Runtime, Kind: "id", Source: "herdr:codex", Value: "native-owned-binding"}
			active := herdr.Agent{PaneID: target.Terminal.PaneID, WorkspaceID: target.Terminal.WorkspaceID, Kind: plan.Runtime, Session: &herdr.AgentSession{Agent: plan.Runtime, Kind: "id", Value: "native-owned-binding"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			failure := errors.New("active observation failed")
			client.agentGet = func(context.Context, string) (herdr.Agent, error) {
				calls++
				switch stage {
				case "delayed-active":
					if calls == 1 {
						return herdr.Agent{}, herdr.ErrUnknownTarget
					}
				case "partial-active":
					if calls == 1 {
						partial := active
						partial.Kind = ""
						partial.Session = nil
						return partial, nil
					}
				case "unknown-until-cancel":
					cancel()
					return herdr.Agent{}, herdr.ErrUnknownTarget
				case "agent-error":
					return herdr.Agent{}, failure
				}
				return active, nil
			}
			switch stage {
			case "pane-runtime":
				client.pane.AgentSession.Agent = "claude"
			case "active-runtime":
				active.Kind = "claude"
			case "active-native":
				active.Session.Value = "foreign-native"
			case "active-session-runtime":
				active.Session.Agent = "claude"
			case "active-pane":
				active.PaneID = "other-pane"
			case "active-workspace":
				active.WorkspaceID = "other-workspace"
			case "foreign-owned-pid":
				client.processes.Foreground[0].PID++
			case "process-error":
				client.processErr = failure
			case "native-bind-conflict":
				changed := record
				changed.Model = "different-requested-model"
				if _, err := session.Register(dir, changed); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := transport.BindSession(ctx, terminalAddress(plan), dir, record)
			wantSuccess := stage == "delayed-active" || stage == "partial-active"
			if wantSuccess {
				if err != nil || bound.NativeHarnessID != "native-owned-binding" || calls != 2 {
					t.Fatalf("did not wait for active exact session: %#v calls=%d err=%v", bound, calls, err)
				}
			} else {
				if err == nil {
					t.Fatal("unready or contradictory native target bound")
				}
				if stage == "unknown-until-cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("bounded wait did not preserve cancellation: %v", err)
				}
				if (stage == "agent-error" || stage == "process-error") && !errors.Is(err, failure) {
					t.Fatalf("observation error lost: %v", err)
				}
				if strings.Contains(stage, "active-") && !strings.Contains(err.Error(), "contradicts") {
					t.Fatalf("active mismatch error: %v", err)
				}
				unchanged, live, lookupErr := session.LookupRecordExact(dir, record)
				if lookupErr != nil || !live || unchanged.NativeHarnessID != "" {
					t.Fatalf("refusal mutated native binding: %#v %v", unchanged, lookupErr)
				}
			}
		})
	}
}
