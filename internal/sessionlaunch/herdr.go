package sessionlaunch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

type terminal interface {
	StartDetached(context.Context, string, string, string, []string) error
	PanePID(context.Context, string) (int, bool, error)
	PaneFailure(context.Context, string) (terminalFailure, bool, error)
}
type terminalFailure struct {
	ExitStatus int
	Diagnostic string
}

type launchHerdrClient interface {
	WorkspaceCreate(context.Context, string, string) (herdr.CreatedWorkspace, error)
	PaneGet(context.Context, string) (herdr.Pane, error)
	AgentGet(context.Context, string) (herdr.Agent, error)
	PaneProcessInfo(context.Context, string) (herdr.PaneProcesses, error)
	PaneExec(context.Context, string, string, []string) error
}
type osHerdr struct {
	executable string
	client     func(string) (launchHerdrClient, error)
	socket     string
	clients    *sync.Map
}

type terminalKey struct {
	Root    string `json:"root"`
	Handoff string `json:"handoff"`
}

func terminalAddress(plan launchPlan) string {
	if plan.Transport != "herdr" {
		return plan.TmuxName
	} // immutable legacy inspection only
	raw, _ := json.Marshal(terminalKey{plan.StoreRoot, plan.HandoffID})
	return "herdr-v1:" + base64.RawURLEncoding.EncodeToString(raw)
}
func validPlanTerminal(plan launchPlan, successorID string) bool {
	return plan.Transport == "herdr" && plan.TmuxName == "" || plan.Transport == "" && plan.TmuxName == "wb-session-"+successorID
}

type herdrTarget struct {
	SchemaVersion int                      `json:"schema_version"`
	AttemptID     string                   `json:"attempt_id"`
	PlanDigest    sessionmove.Digest       `json:"plan_digest"`
	Terminal      sessionterminal.Identity `json:"terminal"`
}
type herdrChild struct {
	SchemaVersion    int                `json:"schema_version"`
	AttemptID        string             `json:"attempt_id"`
	PlanDigest       sessionmove.Digest `json:"plan_digest"`
	PID              int                `json:"pid"`
	ProcessStartedAt time.Time          `json:"process_started_at"`
}
type herdrExit struct {
	Child      herdrChild `json:"child"`
	ExitStatus int        `json:"exit_status"`
	Diagnostic string     `json:"diagnostic"`
}

func openHerdrAttempt(address string) (*launchState, *launchAttempt, launchPlan, sessionmove.Digest, error) {
	if len(address) > 32<<10 || !strings.HasPrefix(address, "herdr-v1:") {
		return nil, nil, launchPlan{}, "", fmt.Errorf("legacy terminal identities are read-only; launch requires Herdr")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(address, "herdr-v1:"))
	if err != nil {
		return nil, nil, launchPlan{}, "", err
	}
	var key terminalKey
	if err := decodeLaunchJSON(raw, &key); err != nil {
		return nil, nil, launchPlan{}, "", err
	}
	if !filepath.IsAbs(key.Root) || filepath.Clean(key.Root) != key.Root {
		return nil, nil, launchPlan{}, "", fmt.Errorf("Herdr launch address has an invalid private root")
	}
	state, err := openLaunchState(key.Root, key.Handoff, false)
	if err != nil {
		return nil, nil, launchPlan{}, "", err
	}
	plan, digest, err := state.loadPlan()
	if err != nil {
		_ = state.Close()
		return nil, nil, launchPlan{}, "", err
	}
	if plan.Transport != "herdr" || plan.TmuxName != "" || terminalAddress(plan) != address {
		_ = state.Close()
		return nil, nil, launchPlan{}, "", fmt.Errorf("Herdr launch address contradicts immutable plan")
	}
	attempt, err := latestAttempt(state)
	if err != nil {
		_ = state.Close()
		return nil, nil, launchPlan{}, "", err
	}
	return state, attempt, plan, digest, nil
}

func (t osHerdr) newClient(socket string) (launchHerdrClient, error) {
	if t.client != nil {
		return t.client(socket)
	}
	if t.clients != nil {
		if client, found := t.clients.Load(socket); found {
			return client.(launchHerdrClient), nil
		}
	}
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, fmt.Errorf("Herdr launch requires a clean absolute HERDR_SOCKET_PATH; start Herdr TUI and select its socket explicitly")
	}
	lookup := func(key string) (string, bool) {
		if key == "HERDR_BIN_PATH" {
			return t.executable, true
		}
		return os.LookupEnv(key)
	}
	client, err := herdr.NewClient(lookup, herdr.WithSocketPath(socket))
	if err != nil {
		return nil, err
	}
	if err := client.EnsureTUIProtocol(context.Background()); err != nil {
		return nil, err
	}
	if t.clients != nil {
		t.clients.Store(socket, client)
	}
	return client, nil
}

func (t osHerdr) Preflight(ctx context.Context) error {
	socket := t.socket
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	client, err := t.newClient(socket)
	if err != nil {
		return err
	}
	if enumerator, ok := client.(interface {
		PaneList(context.Context, string) ([]herdr.Pane, error)
	}); ok {
		_, err := enumerator.PaneList(ctx, "")
		return err
	}
	return nil
}

func (attempt *launchAttempt) loadHerdrTarget(digest sessionmove.Digest) (herdrTarget, bool, error) {
	raw, err := attempt.read("", "herdr-target.json")
	if errors.Is(err, os.ErrNotExist) {
		return herdrTarget{}, false, nil
	}
	if err != nil {
		return herdrTarget{}, false, err
	}
	var target herdrTarget
	if err := decodeLaunchJSON(raw, &target); err != nil {
		return herdrTarget{}, false, err
	}
	if target.SchemaVersion != launchSchemaVersion || target.AttemptID != attempt.id || target.PlanDigest != digest {
		return herdrTarget{}, false, fmt.Errorf("Herdr target receipt contradicts attempt")
	}
	if err := target.Terminal.Validate(); err != nil {
		return herdrTarget{}, false, err
	}
	return target, true, nil
}

func (t osHerdr) StartDetached(ctx context.Context, address, cwd, executable string, args []string) error {
	state, attempt, plan, digest, err := openHerdrAttempt(address)
	if err != nil {
		return err
	}
	defer func() { _ = attempt.Close(); _ = state.Close() }()
	expected := []string{PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
	if cwd != plan.WorktreeDir || executable != plan.WBExecutable || !reflect.DeepEqual(args, expected) {
		return fmt.Errorf("Herdr launch requires fixed private bootstrap argv")
	}
	target, found, err := attempt.loadHerdrTarget(digest)
	if err != nil {
		return err
	}
	socket := t.socket
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if found {
		socket = target.Terminal.Socket
	}
	client, err := t.newClient(socket)
	if err != nil {
		return err
	}
	if !found {
		intent, _ := encodeLaunchJSON(struct {
			AttemptID  string             `json:"attempt_id"`
			PlanDigest sessionmove.Digest `json:"plan_digest"`
			Socket     string             `json:"socket"`
		}{attempt.id, digest, socket})
		created, err := attempt.publish("", "herdr-create-intent.json", intent)
		if err != nil {
			return err
		}
		if !created {
			return fmt.Errorf("Herdr workspace creation outcome is ambiguous; inspect retained attempt before recovery")
		}
		workspace, err := client.WorkspaceCreate(ctx, plan.WorktreeDir, "wb-"+plan.SuccessorWBSessionID)
		if err != nil {
			return err
		}
		pane, err := client.PaneGet(ctx, workspace.Pane.PaneID)
		if err != nil {
			return err
		}
		if pane.PaneID != workspace.Pane.PaneID || pane.WorkspaceID != workspace.WorkspaceID || pane.AgentSession != nil {
			return fmt.Errorf("new Herdr workspace root is not the exact empty owned pane")
		}
		target = herdrTarget{SchemaVersion: launchSchemaVersion, AttemptID: attempt.id, PlanDigest: digest, Terminal: sessionterminal.Identity{Kind: "herdr", Socket: socket, WorkspaceID: workspace.WorkspaceID, PaneID: pane.PaneID}}
		if err := target.Terminal.Validate(); err != nil {
			return err
		}
		raw, _ := encodeLaunchJSON(target)
		if _, err := attempt.publish("", "herdr-target.json", raw); err != nil {
			return err
		}
	}
	// Submission is at most once even if the client loses its reply. Only exact
	// live child/readiness evidence may adopt an already submitted attempt.
	intent, _ := encodeLaunchJSON(target)
	created, err := attempt.publish("", "herdr-submit-intent.json", intent)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("Herdr bootstrap submission outcome is ambiguous; automatic resubmission refused")
	}
	return client.PaneExec(ctx, target.Terminal.PaneID, plan.WBExecutable, append([]string{PrivateLauncherArgument, "--supervise"}, expected[1:]...))
}

func (t osHerdr) PanePID(ctx context.Context, address string) (int, bool, error) {
	state, attempt, plan, digest, err := openHerdrAttempt(address)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = attempt.Close(); _ = state.Close() }()
	target, found, err := attempt.loadHerdrTarget(digest)
	if err != nil {
		return 0, false, err
	}
	if !found {
		if _, err := attempt.read("", "herdr-create-intent.json"); err == nil {
			return 0, false, fmt.Errorf("Herdr creation has no durable target receipt; refusing ambiguous retry")
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, false, err
		}
		return 0, false, nil
	}
	client, err := t.newClient(target.Terminal.Socket)
	if err != nil {
		return 0, false, err
	}
	pane, err := client.PaneGet(ctx, target.Terminal.PaneID)
	if errors.Is(err, herdr.ErrUnknownTarget) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if pane.PaneID != target.Terminal.PaneID || pane.WorkspaceID != target.Terminal.WorkspaceID {
		return 0, false, fmt.Errorf("Herdr pane changed workspace identity")
	}
	processes, err := client.PaneProcessInfo(ctx, target.Terminal.PaneID)
	if err != nil {
		return 0, false, err
	}
	var child herdrChild
	childRaw, childErr := attempt.read("", "herdr-child.json")
	if childErr == nil {
		if err := decodeLaunchJSON(childRaw, &child); err != nil {
			return 0, false, err
		}
		if child.AttemptID != attempt.id || child.PlanDigest != digest || child.PID <= 0 {
			return 0, false, fmt.Errorf("Herdr child receipt contradicts attempt")
		}
	} else if !errors.Is(childErr, os.ErrNotExist) {
		return 0, false, childErr
	}
	wrapper := append([]string{plan.WBExecutable, PrivateLauncherArgument}, []string{plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}...)
	harness := append([]string{plan.HarnessExecutable}, plan.HarnessArgs...)
	pid := 0
	for _, process := range processes.Foreground {
		if childErr == nil && process.PID != child.PID {
			continue
		}
		if !reflect.DeepEqual(process.Argv, wrapper) && !(childErr == nil && reflect.DeepEqual(process.Argv, harness)) {
			continue
		}
		if pid != 0 {
			return 0, false, fmt.Errorf("Herdr pane has multiple matching private launch processes")
		}
		if childErr == nil && !child.ProcessStartedAt.IsZero() {
			observed, known := session.ObserveProcessGeneration(process.PID)
			if !known || !observed.Equal(child.ProcessStartedAt) {
				return 0, false, fmt.Errorf("Herdr child PID generation changed")
			}
		}
		pid = process.PID
	}
	return pid, pid > 0, nil
}

func (t osHerdr) PaneFailure(_ context.Context, address string) (terminalFailure, bool, error) {
	state, attempt, _, digest, err := openHerdrAttempt(address)
	if errors.Is(err, os.ErrNotExist) {
		return terminalFailure{}, false, nil
	}
	if err != nil {
		return terminalFailure{}, false, err
	}
	defer func() { _ = attempt.Close(); _ = state.Close() }()
	raw, err := attempt.read("", "herdr-exit.json")
	if errors.Is(err, os.ErrNotExist) {
		return terminalFailure{}, false, nil
	}
	if err != nil {
		return terminalFailure{}, false, err
	}
	var exit herdrExit
	if err := decodeLaunchJSON(raw, &exit); err != nil {
		return terminalFailure{}, false, err
	}
	if exit.Child.SchemaVersion != launchSchemaVersion || exit.Child.AttemptID != attempt.id || exit.Child.PlanDigest != digest || exit.Child.PID <= 0 || exit.Child.ProcessStartedAt.IsZero() || exit.ExitStatus < 0 || len(exit.Diagnostic) > 4096 {
		return terminalFailure{}, false, fmt.Errorf("Herdr exit receipt contradicts exact child attempt")
	}
	childRaw, err := attempt.read("", "herdr-child.json")
	if err != nil {
		return terminalFailure{}, false, err
	}
	var child herdrChild
	if err := decodeLaunchJSON(childRaw, &child); err != nil {
		return terminalFailure{}, false, err
	}
	if child != exit.Child {
		return terminalFailure{}, false, fmt.Errorf("Herdr exit receipt belongs to another child")
	}
	if !child.ProcessStartedAt.IsZero() {
		if err := session.ProveGenerationGone(session.Record{PID: child.PID, ProcessStartedAt: child.ProcessStartedAt}); err != nil {
			return terminalFailure{}, false, err
		}
	}
	return terminalFailure{ExitStatus: exit.ExitStatus, Diagnostic: exit.Diagnostic}, true, nil
}

func (t osHerdr) BindSession(ctx context.Context, address, dir string, record session.Record) (session.Record, error) {
	state, attempt, plan, digest, err := openHerdrAttempt(address)
	if err != nil {
		return session.Record{}, err
	}
	defer func() { _ = attempt.Close(); _ = state.Close() }()
	target, found, err := attempt.loadHerdrTarget(digest)
	if err != nil {
		return session.Record{}, err
	}
	if !found || target.Terminal != record.Terminal {
		return session.Record{}, fmt.Errorf("registered child has different Herdr target")
	}
	client, err := t.newClient(target.Terminal.Socket)
	if err != nil {
		return session.Record{}, err
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		pane, err := client.PaneGet(wait, target.Terminal.PaneID)
		if err != nil {
			return session.Record{}, err
		}
		if pane.PaneID != target.Terminal.PaneID || pane.WorkspaceID != target.Terminal.WorkspaceID {
			return session.Record{}, fmt.Errorf("Herdr native observation belongs to another pane")
		}
		if native := pane.AgentSession.HarnessSessionID(); native != "" {
			agent := pane.AgentSession.Agent
			if agent == "claude" {
				agent = RuntimeClaudeCode
			}
			if agent != plan.Runtime {
				return session.Record{}, fmt.Errorf("Herdr reports a different native harness runtime")
			}
			// Official native-session reports precede Herdr's asynchronous
			// foreground-agent classification. Session metadata alone does not
			// make this pane an actionable AgentPrompt target.
			active, err := client.AgentGet(wait, target.Terminal.PaneID)
			if err != nil && !errors.Is(err, herdr.ErrUnknownTarget) {
				return session.Record{}, err
			}
			if err == nil {
				kind := active.Kind
				if kind == "claude" {
					kind = RuntimeClaudeCode
				}
				sessionRuntime := ""
				if active.Session != nil {
					sessionRuntime = active.Session.Agent
					if sessionRuntime == "claude" {
						sessionRuntime = RuntimeClaudeCode
					}
				}
				if sessionRuntime != "" && sessionRuntime != plan.Runtime || active.PaneID != pane.PaneID || active.WorkspaceID != pane.WorkspaceID || kind != "" && kind != plan.Runtime || active.HarnessSessionID() != "" && active.HarnessSessionID() != native {
					return session.Record{}, fmt.Errorf("Herdr active agent contradicts the owned native session")
				}
				if kind != "" && active.HarnessSessionID() != "" {
					pid, live, err := t.PanePID(wait, address)
					if err != nil {
						return session.Record{}, err
					}
					if !live || pid != record.PID {
						return session.Record{}, fmt.Errorf("Herdr native session has no matching owned child PID")
					}
					return session.BindNativeHarness(dir, record, native)
				}
			}
		}
		select {
		case <-wait.Done():
			return session.Record{}, fmt.Errorf("wait for Herdr native harness identity: %w", wait.Err())
		case <-ticker.C:
		}
	}
}
