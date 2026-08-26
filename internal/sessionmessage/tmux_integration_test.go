//go:build darwin || linux

package sessionmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

const (
	tmuxPTYHelperEnv        = "WB_TEST_TMUX_PTY_HELPER"
	tmuxPTYCaptureEnv       = "WB_TEST_TMUX_PTY_CAPTURE"
	tmuxPTYDoneEnv          = "WB_TEST_TMUX_PTY_DONE"
	tmuxPTYErrorEnv         = "WB_TEST_TMUX_PTY_ERROR"
	tmuxPTYExpectedBytesEnv = "WB_TEST_TMUX_PTY_EXPECTED_BYTES"
	tmuxPTYReadyMarker      = "WB_TMUX_PTY_READY"
)

// TestOSTmuxRealPTYPreservesBracketedPayloadAndSubmitsOnce exercises tmux
// itself rather than a scripted command runner. The helper pane enables
// bracketed paste and puts its PTY in raw mode, making the captured bytes the
// exact stream tmux delivered to the application.
func TestOSTmuxRealPTYPreservesBracketedPayloadAndSubmitsOnce(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is unavailable")
	}
	tmuxPath, err = filepath.Abs(tmuxPath)
	if err != nil {
		t.Fatal(err)
	}

	tempDir, err := os.MkdirTemp(os.TempDir(), "wbtmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	socketPath := filepath.Join(tempDir, "s")
	capturePath := filepath.Join(tempDir, "capture")
	donePath := filepath.Join(tempDir, "done")
	errorPath := filepath.Join(tempDir, "error")

	message := sessionmove.Message{
		SchemaVersion:        sessionmove.MessageSchemaVersion,
		MessageID:            "message-tmux-pty",
		HandoffID:            "handoff-tmux-pty",
		SenderWBSessionID:    "wbs-source",
		RecipientWBSessionID: "wbs-successor",
		ReplyToWBSessionID:   "wbs-source",
		Kind:                 sessionmove.MessageKindText,
		Body:                 "first line\nsecond line; $(touch /tmp/not-executed)",
		SentAt:               time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC),
	}
	raw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("\x1b[200~"), raw...)
	want = append(want, []byte("\x1b[201~\r")...)

	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	sessionName := "wb-tmux-pty-integration"
	start := exec.Command(tmuxPath, "-S", socketPath, "-f", "/dev/null", "new-session", "-d",
		"-x", "80", "-y", "24", "-s", sessionName,
		testBinary, "-test.run=^TestTmuxPTYCaptureHelper$", "-test.v=false")
	start.Env = tmuxIntegrationEnv(
		tmuxPTYHelperEnv+"=1",
		tmuxPTYCaptureEnv+"="+capturePath,
		tmuxPTYDoneEnv+"="+donePath,
		tmuxPTYErrorEnv+"="+errorPath,
		tmuxPTYExpectedBytesEnv+"="+strconv.Itoa(len(want)),
	)
	output, startErr := start.CombinedOutput()
	if startErr != nil {
		t.Fatalf("start isolated tmux helper: %v: %s", startErr, strings.TrimSpace(string(output)))
	}
	if _, statErr := os.Lstat(socketPath); statErr != nil {
		diagnostic := strings.TrimSpace(string(output))
		if errors.Is(statErr, os.ErrNotExist) &&
			(strings.Contains(strings.ToLower(diagnostic), "operation not permitted") ||
				strings.Contains(strings.ToLower(diagnostic), "permission denied") ||
				strings.Contains(strings.ToLower(diagnostic), "error creating")) {
			t.Skipf("isolated tmux server is unavailable: %s", diagnostic)
		}
		t.Fatalf("isolated tmux server did not publish socket %s: %v: %s", socketPath, statErr, diagnostic)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, tmuxPath, "-S", socketPath, "kill-server")
		command.Env = tmuxIntegrationEnv()
		_ = command.Run()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &osTmux{executable: tmuxPath, runner: isolatedTmuxRunner{socketPath: socketPath}}
	pane, err := waitForOSTmuxPane(ctx, client, sessionName, errorPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForTmuxPaneMarker(ctx, tmuxPath, socketPath, pane.ID, tmuxPTYReadyMarker, errorPath); err != nil {
		t.Fatal(err)
	}
	const bufferName = "wb-message-tmux-pty"
	if err := client.LoadBuffer(ctx, bufferName, raw); err != nil {
		t.Fatal(err)
	}
	verified, err := client.SaveBuffer(ctx, bufferName)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verified, raw) {
		t.Fatalf("saved tmux buffer changed canonical message:\n got %q\nwant %q", verified, raw)
	}
	if err := client.PasteBuffer(ctx, bufferName, pane.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteBuffer(ctx, bufferName); err != nil {
		t.Fatal(err)
	}
	if err := client.Submit(ctx, pane.ID); err != nil {
		t.Fatal(err)
	}

	if err := waitForTmuxPTYHelper(ctx, donePath, errorPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tmux PTY stream changed exact bracketed payload or submit key:\n got %q\nwant %q", got, want)
	}
}

// TestTmuxPTYCaptureHelper is executed as the sole pane process by the
// integration test above. A normal package test run skips it.
func TestTmuxPTYCaptureHelper(t *testing.T) {
	if os.Getenv(tmuxPTYHelperEnv) != "1" {
		t.Skip("tmux PTY helper")
	}
	capturePath := os.Getenv(tmuxPTYCaptureEnv)
	donePath := os.Getenv(tmuxPTYDoneEnv)
	errorPath := os.Getenv(tmuxPTYErrorEnv)
	expected, err := strconv.Atoi(os.Getenv(tmuxPTYExpectedBytesEnv))
	if err != nil || expected <= 0 {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("invalid expected byte count"))
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("helper stdin is not a PTY"))
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("put helper PTY in raw mode: %w", err))
	}
	defer func() { _ = term.Restore(fd, state) }()

	// capture-pane observing the marker proves tmux parsed the preceding mode
	// escape from the same ordered PTY output stream.
	if _, err := io.WriteString(os.Stdout, "\x1b[?2004h"+tmuxPTYReadyMarker); err != nil {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("enable bracketed paste: %w", err))
	}
	captured, err := readPTYUntilQuiet(fd, expected, 5*time.Second, 200*time.Millisecond)
	if err != nil {
		tmuxPTYHelperFatal(t, errorPath, err)
	}
	if err := os.WriteFile(capturePath, captured, 0o600); err != nil {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("write PTY capture: %w", err))
	}
	if err := os.WriteFile(donePath, []byte("done\n"), 0o600); err != nil {
		tmuxPTYHelperFatal(t, errorPath, fmt.Errorf("write PTY completion: %w", err))
	}
}

type isolatedTmuxRunner struct{ socketPath string }

func (runner isolatedTmuxRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	isolatedArgs := append([]string{"-S", runner.socketPath}, args...)
	command := exec.CommandContext(ctx, executable, isolatedArgs...)
	command.Env = tmuxIntegrationEnv()
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func waitForOSTmuxPane(ctx context.Context, client *osTmux, sessionName, errorPath string) (Pane, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if diagnostic, err := os.ReadFile(errorPath); err == nil {
			return Pane{}, fmt.Errorf("tmux PTY helper failed before pane discovery: %s", strings.TrimSpace(string(diagnostic)))
		}
		if pane, err := client.Inspect(ctx, sessionName); err == nil {
			return pane, nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return Pane{}, fmt.Errorf("wait for isolated tmux pane: %w; last error %v", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func waitForTmuxPaneMarker(ctx context.Context, tmuxPath, socketPath, paneID, marker, errorPath string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastOutput []byte
	var lastErr error
	for {
		if diagnostic, err := os.ReadFile(errorPath); err == nil {
			return fmt.Errorf("tmux PTY helper failed before readiness: %s", strings.TrimSpace(string(diagnostic)))
		}
		command := exec.CommandContext(ctx, tmuxPath, "-S", socketPath, "capture-pane", "-p", "-t", paneID)
		command.Env = tmuxIntegrationEnv()
		output, err := command.CombinedOutput()
		if err == nil {
			lastOutput = output
			if bytes.Contains(output, []byte(marker)) {
				return nil
			}
		} else {
			lastErr = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for isolated tmux helper readiness: %w; last pane output %q; last capture error %v", ctx.Err(), lastOutput, lastErr)
		case <-ticker.C:
		}
	}
}

func waitForTmuxPTYHelper(ctx context.Context, donePath, errorPath string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if diagnostic, err := os.ReadFile(errorPath); err == nil {
			return fmt.Errorf("tmux PTY helper failed: %s", strings.TrimSpace(string(diagnostic)))
		}
		if _, err := os.Stat(donePath); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for tmux PTY helper capture: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func readPTYUntilQuiet(fd, expected int, overall, quiet time.Duration) ([]byte, error) {
	deadline := time.Now().Add(overall)
	captured := make([]byte, 0, expected+1)
	buffer := make([]byte, 4096)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return captured, fmt.Errorf("timed out reading PTY input: got %d bytes, want at least %d", len(captured), expected)
		}
		wait := 100 * time.Millisecond
		if len(captured) >= expected {
			wait = quiet
		} else if remaining < wait {
			wait = remaining
		}
		milliseconds := int((wait + time.Millisecond - 1) / time.Millisecond)
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(poll, milliseconds)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return captured, fmt.Errorf("poll helper PTY: %w", err)
		}
		if ready == 0 {
			if len(captured) >= expected {
				return captured, nil
			}
			continue
		}
		count, readErr := unix.Read(fd, buffer)
		if count > 0 {
			captured = append(captured, buffer[:count]...)
		}
		if readErr != nil && !errors.Is(readErr, unix.EINTR) && !errors.Is(readErr, unix.EAGAIN) {
			return captured, fmt.Errorf("read helper PTY: %w", readErr)
		}
		if poll[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 && len(captured) < expected {
			return captured, fmt.Errorf("helper PTY closed after %d bytes, want at least %d", len(captured), expected)
		}
	}
}

func tmuxPTYHelperFatal(t *testing.T, errorPath string, err error) {
	t.Helper()
	if errorPath != "" {
		_ = os.WriteFile(errorPath, []byte(err.Error()+"\n"), 0o600)
	}
	t.Fatal(err)
}

func tmuxIntegrationEnv(extra ...string) []string {
	environment := make([]string, 0, len(os.Environ())+len(extra)+1)
	hasTerm := false
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE":
			continue
		case "TERM":
			hasTerm = true
		}
		environment = append(environment, entry)
	}
	if !hasTerm {
		environment = append(environment, "TERM=xterm-256color")
	}
	return append(environment, extra...)
}
