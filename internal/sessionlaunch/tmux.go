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
	PaneFailure(context.Context, string) (tmuxFailure, bool, error)
}

type osTmux struct{ executable string }

type tmuxFailure struct {
	ExitStatus int
	Diagnostic string
}

func (t osTmux) StartDetached(ctx context.Context, name, cwd, executable string, arguments []string) error {
	if _, dead, err := t.PaneFailure(ctx, name); err != nil {
		return err
	} else if dead {
		command := exec.CommandContext(ctx, t.executable, "kill-session", "-t", "="+name)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("remove terminal tmux successor %s: %w: %s", name, err, boundedTmuxDetail(output))
		}
	}
	args := []string{"new-session", "-d", "-s", name, "-c", cwd, executable}
	args = append(args, arguments...)
	args = append(args, ";", "set-option", "-t", "="+name, "remain-on-exit", "on")
	command := exec.CommandContext(ctx, t.executable, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start detached tmux successor: %w: %s", err, boundedTmuxDetail(output))
	}
	return nil
}

func (t osTmux) PanePID(ctx context.Context, name string) (int, bool, error) {
	command := exec.CommandContext(ctx, t.executable, "list-panes", "-s", "-t", "="+name, "-F", "#{pane_pid}\t#{pane_dead}")
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		detail := strings.ToLower(string(output))
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			(strings.Contains(detail, "can't find session:") || strings.Contains(detail, "no server running on")) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("inspect tmux successor %s: %w: %s", name, err, boundedTmuxDetail(output))
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return 0, false, fmt.Errorf("tmux successor %s returned %d pane fields, want PID and dead state", name, len(fields))
	}
	if fields[1] == "1" {
		return 0, false, nil
	}
	if fields[1] != "0" {
		return 0, false, fmt.Errorf("tmux successor %s reported invalid dead state %q", name, fields[1])
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, false, fmt.Errorf("tmux successor %s reported invalid pane PID %q", name, fields[0])
	}
	return pid, true, nil
}

func (t osTmux) PaneFailure(ctx context.Context, name string) (tmuxFailure, bool, error) {
	command := exec.CommandContext(ctx, t.executable, "list-panes", "-s", "-t", "="+name, "-F", "#{pane_dead}\t#{pane_dead_status}")
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		detail := strings.ToLower(string(output))
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			(strings.Contains(detail, "can't find session:") || strings.Contains(detail, "no server running on")) {
			return tmuxFailure{}, false, nil
		}
		return tmuxFailure{}, false, fmt.Errorf("inspect tmux successor %s failure: %w: %s", name, err, boundedTmuxDetail(output))
	}
	dead, status, parseErr := parseTmuxPaneFailureOutput(output)
	if parseErr != nil {
		return tmuxFailure{}, false, fmt.Errorf("tmux successor %s %w", name, parseErr)
	}
	if dead == 0 {
		return tmuxFailure{}, false, nil
	}
	capture := exec.CommandContext(ctx, t.executable, "capture-pane", "-p", "-S", "-200", "-t", "="+name)
	diagnostic, captureErr := capture.CombinedOutput()
	if captureErr != nil {
		return tmuxFailure{}, false, fmt.Errorf("capture terminal tmux successor %s: %w: %s", name, captureErr, boundedTmuxDetail(diagnostic))
	}
	return tmuxFailure{ExitStatus: status, Diagnostic: boundedTmuxDetail(diagnostic)}, true, nil
}

func parseTmuxPaneFailureOutput(output []byte) (int, int, error) {
	line := string(output)
	if !strings.HasSuffix(line, "\n") {
		return 0, 0, fmt.Errorf("returned malformed failure fields, want dead state and exit status")
	}
	line = strings.TrimSuffix(line, "\n")
	if strings.Contains(line, "\n") {
		return 0, 0, fmt.Errorf("returned malformed failure fields, want one pane")
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("returned %d failure fields, want dead state and exit status", len(fields))
	}
	if fields[0] == "0" {
		if fields[1] != "" {
			return 0, 0, fmt.Errorf("reported exit status %q for live pane", fields[1])
		}
		return 0, 0, nil
	}
	if fields[0] != "1" {
		return 0, 0, fmt.Errorf("reported invalid dead state %q", fields[0])
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 0 {
		return 0, 0, fmt.Errorf("reported invalid exit status %q", fields[1])
	}
	return 1, status, nil
}

func boundedTmuxDetail(raw []byte) string {
	const maximum = 1024
	if len(raw) > maximum {
		raw = raw[:maximum]
	}
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(string(raw)))
}
