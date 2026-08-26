package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type tmux interface {
	StartDetached(context.Context, string, string, string, []string) error
	PanePID(context.Context, string) (int, bool, error)
}

type osTmux struct{ executable string }

func (t osTmux) StartDetached(ctx context.Context, name, cwd, executable string, arguments []string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", cwd, executable}
	args = append(args, arguments...)
	command := exec.CommandContext(ctx, t.executable, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start detached tmux successor: %w: %s", err, boundedTmuxDetail(output))
	}
	return nil
}

func (t osTmux) PanePID(ctx context.Context, name string) (int, bool, error) {
	command := exec.CommandContext(ctx, t.executable, "list-panes", "-s", "-t", "="+name, "-F", "#{pane_pid}")
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		detail := strings.ToLower(string(output))
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			(strings.Contains(detail, "can't find session:") || strings.Contains(detail, "can't find window:") ||
				strings.Contains(detail, "no server running on")) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("inspect tmux successor %s: %w: %s", name, err, boundedTmuxDetail(output))
	}
	lines := strings.Fields(string(output))
	if len(lines) != 1 {
		return 0, false, fmt.Errorf("tmux successor %s has %d panes, want exactly one", name, len(lines))
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil || pid <= 0 {
		return 0, false, fmt.Errorf("tmux successor %s reported invalid pane PID %q", name, lines[0])
	}
	return pid, true, nil
}

func boundedTmuxDetail(raw []byte) string {
	const maximum = 1024
	if len(raw) > maximum {
		raw = raw[:maximum]
	}
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(string(raw)))
}
