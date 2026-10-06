package daemonhost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryReceiverReturnsActualQueueAndConfigurationErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"queue directory", "config syntax", "git provider", "no remote", "long machine", "hub provider"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			config := filepath.Join(root, "wb.yaml")
			body := "{}\n"
			want := ""
			switch stage {
			case "queue directory":
				write(t, filepath.Join(root, ".wb"), "blocked")
				want = "create repository event queue"
			case "config syntax":
				body = "remote: [broken]\n"
				want = ""
			case "git provider":
				body = "remote:\n  provider: git\n  repo: acme/private\n  machine: private-machine\n"
			case "long machine":
				body = "remote:\n  provider: hub\n  url: http://127.0.0.1:8796\n  token_file: " + filepath.Join(root, "token") + "\n  machine: " + strings.Repeat("m", 129) + "\n"
				want = "machine is invalid"
			case "hub provider":
				body = "remote:\n  provider: hub\n  url: http://127.0.0.1:8796\n  token_file: " + filepath.Join(root, "token") + "\n  machine: private-machine\n"
			}
			write(t, config, body)
			// Already-cancelled context prevents any poll/network/work after config checks.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := startRepositoryEventReceiver(ctx, root, config, io.Discard)
			failed := stage == "queue directory" || stage == "config syntax" || stage == "long machine"
			if (err != nil) != failed {
				t.Fatalf("receiver=%v failure=%v", err, failed)
			}
			if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("receiver=%v want %q", err, want)
			}
			if !failed {
				if _, err = os.Stat(filepath.Join(root, ".wb", "runtime", "daemon", "repository-events", "jobs")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

//nolint:paralleltest // Process-wide environment changes in TestRepositoryReceiverReturnsActualNonDirectoryHomeResolutionError; these rows share their parent environment and remain sequential.
func TestRepositoryReceiverReturnsActualNonDirectoryHomeResolutionError(t *testing.T) {
	// Actual HOME/USERPROFILE layout observation is process-global; this case is serial.
	root := t.TempDir()
	config := filepath.Join(root, "wb.yaml")
	write(t, config, "remote:\n  provider: hub\n  url: http://127.0.0.1:8796\n  token_file: "+filepath.Join(root, "token")+"\n  machine: private-machine\n")
	blockedHome := filepath.Join(t.TempDir(), "home-file")
	write(t, blockedHome, "private regular file")
	t.Setenv("HOME", blockedHome)
	t.Setenv("USERPROFILE", blockedHome)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := startRepositoryEventReceiver(ctx, root, config, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "resolve WB home") {
		t.Fatalf("actual non-directory home refusal=%v", err)
	}
	if _, err = os.Stat(filepath.Join(root, ".wb", "runtime", "daemon", "repository-events", "jobs")); err != nil {
		t.Fatalf("prior absolute-root queue creation missing: %v", err)
	}
}
