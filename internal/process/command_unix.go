//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// cancellationGrace gives a cooperative child a brief chance to stop before
// escalation. It is deliberately bounded: Command.Wait cannot return while a
// descendant that inherited CombinedOutput's pipes remains alive.
var cancellationGrace = 250 * time.Millisecond

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return commandContextInteractive(ctx, false, name, args...)
}

func commandContextInteractive(ctx context.Context, interactive bool, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	if interactive {
		return command
	}
	// Setsid (rather than Setpgid) gives the child its own session as well as
	// its own process group: it has no controlling terminal at all, so it
	// cannot open /dev/tty to prompt and cannot be stopped by the kernel with
	// SIGTTIN/SIGTTOU for touching one. Group kill (the negative pid below)
	// and the live-group registry still work unchanged, because setsid(2)
	// also makes the child the leader of a brand-new process group whose id
	// equals its own pid -- exactly the property Setpgid gave it before.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return terminateProcessGroup(command.Process.Pid)
	}
	// A descendant that unexpectedly retains an output pipe must not leave the
	// caller blocked indefinitely after cancellation. Normal cancellation kills
	// the whole group before this delay is needed.
	command.WaitDelay = cancellationGrace
	return command
}

func terminateProcessGroup(pid int) error {
	if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
		return err
	}

	timer := time.NewTimer(cancellationGrace)
	defer timer.Stop()
	<-timer.C
	return signalProcessGroup(pid, syscall.SIGKILL)
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	// Setsid makes the direct child the leader of its own session and process
	// group. Negative PID is therefore scoped to precisely that child and
	// descendants which inherited its group; it never scans or signals
	// unrelated system processes.
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// liveGroups is the live-pgid registry: every Cmd registers its child's pgid
// (== its pid, per the Setsid comment above) between a successful Start and
// its Wait. A monotonic token, rather than the pid itself, is the map key so
// two entries never collide during the pid-reuse window SignalLiveGroups'
// doc describes.
var liveGroups = struct {
	mu   sync.Mutex
	next uint64
	pids map[uint64]int
}{pids: map[uint64]int{}}

func trackStart(pid int) uint64 {
	liveGroups.mu.Lock()
	defer liveGroups.mu.Unlock()
	liveGroups.next++
	token := liveGroups.next
	liveGroups.pids[token] = pid
	return token
}

func trackStop(token uint64) {
	liveGroups.mu.Lock()
	defer liveGroups.mu.Unlock()
	delete(liveGroups.pids, token)
}

func signalLiveGroups(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	liveGroups.mu.Lock()
	pids := make([]int, 0, len(liveGroups.pids))
	for _, pid := range liveGroups.pids {
		pids = append(pids, pid)
	}
	liveGroups.mu.Unlock()
	for _, pid := range pids {
		_ = signalProcessGroup(pid, s)
	}
}
