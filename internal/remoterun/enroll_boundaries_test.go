package remoterun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRestartStagePreservesExecutableContextFlagsAndChildErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, stage := range []string{"executable", "child", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			calls := 0
			err := restartDaemonAfterEnroll(ctx, "private-root", func() (string, error) {
				if stage == "executable" {
					return "", errObservedProvider
				}
				return "private-executable", nil
			}, func(got context.Context, path string, args []string) ([]byte, error) {
				calls++
				if got != ctx || path != "private-executable" || !reflect.DeepEqual(args, []string{"--projects-root", "private-root", "--non-interactive", "daemon", "restart", "--if-running", "--format=json"}) {
					t.Fatalf("child custody: %v %q %v", got, path, args)
				}
				if stage == "child" {
					return []byte("  child diagnostic\n"), errObservedProvider
				}
				return nil, nil
			})
			if stage == "success" {
				if err != nil || calls != 1 {
					t.Fatalf("success=%v calls=%d", err, calls)
				}
				return
			}
			if !errors.Is(err, errObservedProvider) {
				t.Fatalf("error identity: %v", err)
			}
			if stage == "executable" && calls != 0 {
				t.Fatal("child ran after executable refusal")
			}
			if stage == "child" && err.Error() != errObservedProvider.Error()+": child diagnostic" {
				t.Fatalf("child diagnostic: %v", err)
			}
		})
	}
	// Canceled command observes the genuine current executable and child runner;
	// the controlled success above is not a native daemon restart proof.
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := RestartDaemonAfterEnroll(canceled, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("actual canceled child: %v", err)
	}
}

func TestEnrollmentPathResolutionRefusalStopsBeforePersistence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	token := filepath.Join(root, "credential")
	config := filepath.Join(root, "wb.yaml")
	var stages []string
	s := NewEnroll(EnrollDependencies{ConfigPath: func() string { stages = append(stages, "config"); return config }, Verify: func(context.Context, string, string, string) error { stages = append(stages, "verify"); return nil }, Restart: func(context.Context, string) error { t.Fatal("restart after resolution refusal"); return nil }}, boundaryExit)
	// Simulated resolver error only; successful original cases retain the real
	// filepath.Abs/default credential writer and native configuration authority.
	s.abs = func(path string) (string, error) {
		stages = append(stages, "abs")
		if path != token {
			t.Fatal(path)
		}
		return "", errObservedProvider
	}
	_, err := s.Enroll(context.Background(), EnrollRequest{ProjectsRoot: root, Machine: "box", TokenFile: token, TokenStdin: true, RestartDaemon: true, Input: strings.NewReader("credential")})
	if !errors.Is(err, errObservedProvider) || !strings.Contains(err.Error(), "resolve token file:") {
		t.Fatalf("resolution error identity: %v", err)
	}
	if !reflect.DeepEqual(stages, []string{"verify", "config", "abs"}) {
		t.Fatalf("stages=%v", stages)
	}
	for _, path := range []string{token, config} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("persisted after resolution refusal: %s %v", path, err)
		}
	}
}
