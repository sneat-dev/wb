package sessionlaunch

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

type supervisorDependencies struct {
	executable func() (string, error)
	identity   func() herdr.Identity
	child      func(launchPlan, *launchAttempt, sessionmove.Digest) (herdrExit, error)
}

func defaultSupervisorDependencies() supervisorDependencies {
	return supervisorDependencies{os.Executable, func() herdr.Identity { return herdr.ReadIdentity(os.LookupEnv) }, superviseOwnedChild}
}

func matchHerdrEnvironment(expected sessionterminal.Identity, actual herdr.Identity) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if actual.SocketPath != expected.Socket || actual.WorkspaceID != expected.WorkspaceID || actual.PaneID != expected.PaneID {
		return fmt.Errorf("private launcher is not in its exact owned Herdr socket/workspace/pane")
	}
	return nil
}

func runHerdrSupervisor(args []string, deps supervisorDependencies) (int, error) {
	if len(args) != 4 {
		return 0, fmt.Errorf("invalid private Herdr supervisor arguments")
	}
	if !filepath.IsAbs(args[0]) || filepath.Clean(args[0]) != args[0] {
		return 0, fmt.Errorf("private Herdr supervisor requires exact absolute store")
	}
	state, err := openLaunchState(args[0], args[1], false)
	if err != nil {
		return 0, err
	}
	defer func() { _ = state.Close() }()
	attempt, err := state.openAttempt(args[2])
	if err != nil {
		return 0, err
	}
	defer func() { _ = attempt.Close() }()
	plan, digest, err := state.loadPlan()
	if err != nil {
		return 0, err
	}
	if digest != sessionmove.Digest(args[3]) || plan.Transport != "herdr" || plan.StoreRoot != args[0] || plan.TmuxName != "" {
		return 0, fmt.Errorf("private Herdr supervisor plan identity mismatch")
	}
	target, found, err := attempt.loadHerdrTarget(digest)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("private Herdr supervisor has no owned target receipt")
	}
	if err := matchHerdrEnvironment(target.Terminal, deps.identity()); err != nil {
		return 0, err
	}
	running, err := deps.executable()
	if err != nil {
		return 0, err
	}
	a, aErr := os.Stat(running)
	b, bErr := os.Stat(plan.WBExecutable)
	if aErr != nil || bErr != nil || !os.SameFile(a, b) {
		return 0, fmt.Errorf("private Herdr supervisor executable differs from planned WB")
	}
	// Exclusive attempt ownership precedes child creation; re-entering this
	// private command can never spawn a second wrapper for the same attempt.
	raw, _ := encodeLaunchJSON(target)
	created, err := attempt.publish("", "herdr-supervisor.json", raw)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, fmt.Errorf("Herdr supervisor attempt already has an owner")
	}
	exit, err := deps.child(plan, attempt, digest)
	if err != nil {
		return 0, err
	}
	raw, err = encodeLaunchJSON(exit)
	if err != nil {
		return 0, err
	}
	if _, err := attempt.publish("", "herdr-exit.json", raw); err != nil {
		return 0, err
	}
	return exit.ExitStatus, nil
}

// terminalDiagnostic retains only a bounded private diagnostic while forwarding
// stderr to the owned terminal. No amount of output grows this capture further.
type terminalDiagnostic struct {
	mu    sync.Mutex
	bytes []byte
}

func (d *terminalDiagnostic) Write(raw []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	remaining := 4096 - len(d.bytes)
	if remaining > len(raw) {
		remaining = len(raw)
	}
	if remaining > 0 {
		d.bytes = append(d.bytes, raw[:remaining]...)
	}
	return len(raw), nil
}

func (d *terminalDiagnostic) snapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.bytes)
}

func superviseOwnedChild(plan launchPlan, attempt *launchAttempt, digest sessionmove.Digest) (herdrExit, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return superviseChildContext(ctx, plan, attempt, digest)
}

func superviseChildContext(ctx context.Context, plan launchPlan, attempt *launchAttempt, digest sessionmove.Digest) (result herdrExit, resultErr error) {
	args := []string{PrivateLauncherArgument, plan.StoreRoot, plan.HandoffID, attempt.id, string(digest)}
	command := process.CommandContext(ctx, plan.WBExecutable, args...)
	command.Dir = plan.WorktreeDir
	command.Env = console.Env()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	diagnostic := &terminalDiagnostic{}
	command.Stderr = io.MultiWriter(os.Stderr, diagnostic)
	restoreTerminal, err := prepareOwnedTerminal(command, os.Stdin)
	if err != nil {
		return herdrExit{}, err
	}
	defer func() {
		if err := restoreTerminal(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()

	if err := command.Start(); err != nil {
		return herdrExit{}, fmt.Errorf("start owned private WB child: %w", err)
	}
	// Cleanup owns only the group created for this child. It also runs after a
	// normal leader exit: descendants retaining stderr cannot outlive custody.
	defer func() {
		if err := cleanupOwnedChild(command); err != nil && resultErr == nil {
			resultErr = err
		}
	}()

	started, known := session.ObserveProcessGeneration(command.Process.Pid)
	child := herdrChild{SchemaVersion: launchSchemaVersion, AttemptID: attempt.id, PlanDigest: digest, PID: command.Process.Pid, ProcessStartedAt: started}
	if !known {
		_ = command.Process.Kill()
		_ = command.Wait()
		return herdrExit{}, fmt.Errorf("owned private WB child has no native generation proof")
	}
	raw, _ := encodeLaunchJSON(child)
	if _, err := attempt.publish("", "herdr-child.json", raw); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return herdrExit{}, err
	}
	waitErr := command.Wait()
	status := command.ProcessState.ExitCode()
	if status < 0 {
		status = 128
		if waitErr != nil {
			_, _ = diagnostic.Write([]byte("owned child terminated by signal"))
		}
	}
	return herdrExit{Child: child, ExitStatus: status, Diagnostic: diagnostic.snapshot()}, nil
}
