package sessionmessage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var canonicalPaneID = regexp.MustCompile(`^%[0-9]+$`)

const (
	tmuxOperationTimeout = 10 * time.Second
	maxTmuxOutputBytes   = 128 << 10
	maxTmuxErrorBytes    = 4 << 10
)

type tmuxCommandRunner interface {
	Run(context.Context, string, []string, []byte, io.Writer, io.Writer) error
}

type execTmuxCommandRunner struct{}

func (execTmuxCommandRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type osTmux struct {
	executable string
	runner     tmuxCommandRunner
}

func newOSTmux() (*osTmux, error) {
	executable, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("resolve fixed tmux executable for session message: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("resolved tmux executable %q is not one clean absolute path", executable)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		if err != nil {
			return nil, fmt.Errorf("inspect fixed tmux executable: %w", err)
		}
		return nil, fmt.Errorf("fixed tmux executable is not a regular executable file")
	}
	return &osTmux{executable: executable, runner: execTmuxCommandRunner{}}, nil
}

func (client *osTmux) Inspect(ctx context.Context, name string) (Pane, error) {
	stdout, err := client.run(ctx, []string{
		"list-panes", "-s", "-t", "=" + name, "-F", "#{session_name}\t#{pane_id}\t#{pane_pid}",
	}, nil, maxTmuxOutputBytes)
	if err != nil {
		return Pane{}, fmt.Errorf("inspect exact tmux successor: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return Pane{}, fmt.Errorf("tmux successor contains %d panes; want exactly one", len(lines))
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) != 3 || fields[0] != name || !canonicalPaneID.MatchString(fields[1]) {
		return Pane{}, fmt.Errorf("tmux list-panes returned an invalid exact-pane identity")
	}
	pid, parseErr := strconv.Atoi(fields[2])
	if parseErr != nil || pid <= 0 {
		return Pane{}, fmt.Errorf("tmux list-panes returned an invalid pane PID")
	}
	return Pane{SessionName: fields[0], ID: fields[1], PID: pid, Count: 1}, nil
}

func (client *osTmux) LoadBuffer(ctx context.Context, name string, raw []byte) error {
	_, err := client.run(ctx, []string{"load-buffer", "-b", name, "-"}, raw, maxTmuxErrorBytes)
	return err
}

func (client *osTmux) SaveBuffer(ctx context.Context, name string) ([]byte, error) {
	return client.run(ctx, []string{"save-buffer", "-b", name, "-"}, nil, maxTmuxOutputBytes)
}

func (client *osTmux) PasteBuffer(ctx context.Context, name, paneID string) error {
	// -p asks tmux to wrap the payload in bracketed-paste markers. -r preserves
	// every LF byte instead of converting it to CR before it reaches the pane.
	_, err := client.run(ctx, []string{"paste-buffer", "-p", "-r", "-b", name, "-t", paneID}, nil, maxTmuxErrorBytes)
	return err
}

func (client *osTmux) DeleteBuffer(ctx context.Context, name string) error {
	_, err := client.run(ctx, []string{"delete-buffer", "-b", name}, nil, maxTmuxErrorBytes)
	return err
}

func (client *osTmux) Submit(ctx context.Context, paneID string) error {
	if !canonicalPaneID.MatchString(paneID) {
		return fmt.Errorf("refuse to submit message to non-canonical tmux pane identity")
	}
	_, err := client.run(ctx, []string{"send-keys", "-t", paneID, "Enter"}, nil, maxTmuxErrorBytes)
	return err
}

func (client *osTmux) run(ctx context.Context, args []string, stdin []byte, stdoutLimit int) ([]byte, error) {
	if client == nil || client.runner == nil || client.executable == "" {
		return nil, fmt.Errorf("tmux command adapter is unavailable")
	}
	operationContext, cancel := context.WithTimeout(ctx, tmuxOperationTimeout)
	defer cancel()
	var stdout, stderr limitedBuffer
	stdout.limit = stdoutLimit
	stderr.limit = maxTmuxErrorBytes
	if err := client.runner.Run(operationContext, client.executable, args, stdin, &stdout, &stderr); err != nil {
		if operationContext.Err() != nil {
			return nil, operationContext.Err()
		}
		// Tmux diagnostics contain only fixed target/buffer identifiers. The
		// message body is stdin-only and is never included here.
		detail := strings.Join(strings.Fields(stderr.buffer.String()), " ")
		if detail == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, detail)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("tmux output exceeds %d bytes", stdoutLimit)
	}
	return append([]byte(nil), stdout.buffer.Bytes()...), nil
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *limitedBuffer) Write(raw []byte) (int, error) {
	written := len(raw)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = buffer.exceeded || len(raw) > 0
		return written, nil
	}
	if len(raw) > remaining {
		_, _ = buffer.buffer.Write(raw[:remaining])
		buffer.exceeded = true
		return written, nil
	}
	_, _ = buffer.buffer.Write(raw)
	return written, nil
}
