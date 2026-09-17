package sessioncourier

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSDCovNewSSHDelivererConstructorBranches(t *testing.T) {
	executable := testExecutable(t)
	missing := filepath.Join(t.TempDir(), "missing-ssh")
	plain := filepath.Join(t.TempDir(), "plain-ssh")
	if err := os.WriteFile(plain, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()

	tests := map[string]struct {
		config   sessionmove.SSHConfig
		lookPath func(string) (string, error)
		runner   commandRunner
		want     string
	}{
		"nil lookup": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: nil,
			runner:   &fakeCommandRunner{},
			want:     "executable lookup is unavailable",
		},
		"lookup failure": {
			config: sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) {
				return "", errors.New("ssh not found")
			},
			runner: &fakeCommandRunner{},
			want:   "resolve ssh executable",
		},
		"relative resolution": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) { return "ssh", nil },
			runner:   &fakeCommandRunner{},
			want:     "clean absolute",
		},
		"unstattable resolution": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) { return missing, nil },
			runner:   &fakeCommandRunner{},
			want:     "resolve ssh executable",
		},
		"non-executable file": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) { return plain, nil },
			runner:   &fakeCommandRunner{},
			want:     "regular executable",
		},
		"directory": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) { return directory, nil },
			runner:   &fakeCommandRunner{},
			want:     "regular executable",
		},
		"nil runner": {
			config:   sessionmove.SSHConfig{Host: "target"},
			lookPath: func(string) (string, error) { return executable, nil },
			runner:   nil,
			want:     "command runner is unavailable",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deliverer, err := newSSHDeliverer(test.config, test.lookPath, test.runner)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("newSSHDeliverer error = %v, want containing %q", err, test.want)
			}
			if deliverer != nil {
				t.Fatalf("newSSHDeliverer returned %#v on error", deliverer)
			}
		})
	}
}

func TestSDCovSSHDelivererFailureDiagnosticBranches(t *testing.T) {
	_, raw := courierTestRequest(t)

	t.Run("cancelled delivery context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &fakeCommandRunner{err: errors.New("signal: killed")}
		deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.Deliver(ctx, raw)
		if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "ssh session delivery to target") {
			t.Fatalf("Deliver error = %v, want wrapped cancellation", err)
		}
		if runner.calls != 1 {
			t.Fatalf("ssh calls = %d", runner.calls)
		}
	})
	t.Run("silent failure", func(t *testing.T) {
		runner := &fakeCommandRunner{err: errors.New("exit status 255")}
		deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.Deliver(context.Background(), raw)
		if err == nil || err.Error() != "ssh session delivery to target: exit status 255" {
			t.Fatalf("Deliver error = %v, want undecorated failure", err)
		}
	})
	t.Run("stderr failure", func(t *testing.T) {
		runner := &fakeCommandRunner{err: errors.New("exit status 255"), stderr: []byte("Host key verification failed.\n")}
		deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
		_, err := deliverer.Deliver(context.Background(), raw)
		if err == nil || err.Error() != "ssh session delivery to target: exit status 255: Host key verification failed." {
			t.Fatalf("Deliver error = %v, want sanitized diagnostic suffix", err)
		}
	})
}

func TestSDCovBoundedBufferHonoursLimit(t *testing.T) {
	var buffer boundedBuffer
	buffer.limit = 4

	written, err := buffer.Write([]byte("abc"))
	if written != 3 || err != nil || buffer.exceeded || string(buffer.Bytes()) != "abc" {
		t.Fatalf("first write: n=%d err=%v exceeded=%v bytes=%q", written, err, buffer.exceeded, buffer.Bytes())
	}
	written, err = buffer.Write([]byte("de"))
	if written != 2 || err != nil || !buffer.exceeded || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("overflowing write: n=%d err=%v exceeded=%v bytes=%q", written, err, buffer.exceeded, buffer.Bytes())
	}
	buffer.exceeded = false
	written, err = buffer.Write([]byte("x"))
	if written != 1 || err != nil || !buffer.exceeded || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("write past limit: n=%d err=%v exceeded=%v bytes=%q", written, err, buffer.exceeded, buffer.Bytes())
	}
	buffer.exceeded = false
	written, err = buffer.Write(nil)
	if written != 0 || err != nil || buffer.exceeded {
		t.Fatalf("empty write past limit: n=%d err=%v exceeded=%v", written, err, buffer.exceeded)
	}
}

func TestSDCovTruncateUTF8DropsPartialRune(t *testing.T) {
	value := "aaa\u00e9" // five bytes: 61 61 61 c3 a9
	if got := truncateUTF8(value, 100); got != value {
		t.Fatalf("truncateUTF8 short value = %q, want %q", got, value)
	}
	if got := truncateUTF8(value, 4); got != "aaa" {
		t.Fatalf("truncateUTF8 splitting rune = %q, want %q", got, "aaa")
	}
	if got := truncateUTF8(value, 5); got != value {
		t.Fatalf("truncateUTF8 exact length = %q, want %q", got, value)
	}
}

// TestSDCovSanitizeDiagnosticBoundsMultibyteTail proves the truncation path
// keeps UTF-8 validity when a multi-byte rune straddles the byte cap.
func TestSDCovSanitizeDiagnosticBoundsMultibyteTail(t *testing.T) {
	raw := []byte(strings.Repeat("\u00e9", maxSSHDiagnosticBytes))
	diagnostic := sanitizeDiagnostic(raw, false)
	if len(diagnostic) > maxSSHDiagnosticBytes {
		t.Fatalf("diagnostic length = %d", len(diagnostic))
	}
	for _, value := range diagnostic {
		if value == '\uFFFD' {
			t.Fatalf("diagnostic contains a replacement rune: %q", diagnostic)
		}
	}
	if !strings.HasSuffix(diagnostic, "...") {
		t.Fatalf("truncated diagnostic = %q, want ellipsis suffix", diagnostic)
	}
}

func TestSDCovTruncatedBoundedBufferReportsExceededDiagnostic(t *testing.T) {
	_, raw := courierTestRequest(t)
	stderr := bytes.Repeat([]byte("y"), maxSSHStderrBytes+1)
	runner := &fakeCommandRunner{stderr: stderr, err: errors.New("exit status 255")}
	deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
	_, err := deliverer.Deliver(context.Background(), raw)
	if err == nil || !strings.HasSuffix(err.Error(), "...") {
		t.Fatalf("Deliver error = %v, want truncated diagnostic", err)
	}
}
