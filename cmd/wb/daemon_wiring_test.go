package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"

	"github.com/sneat-dev/wb/internal/daemonruntime"
)

func TestDaemonServeRepeatedExecutionKeepsCurrentRootAndNoImplicitPin(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	deps := daemonTestDependencies(t, first)
	tokens := 0
	deps.Token = func() (string, error) { tokens++; return "token", nil }
	sentinel := errors.New("private listen refusal")
	calls := 0
	deps.listen = func(network, address string) (net.Listener, error) {
		calls++
		if network != "tcp" || address != daemonruntime.DefaultListen {
			t.Fatalf("listen=%s/%s", network, address)
		}
		return nil, sentinel
	}
	statePath, err := daemonruntime.StatePath(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	state := daemon.NewStartingAt(nil, daemonruntime.DefaultListen, daemon.Provenance{}, "second-token", "", "", deps.Now())
	if err := (daemon.Store{Path: statePath}).Save(state); err != nil {
		t.Fatal(err)
	}
	inv := testInvocation(t, first)
	command := daemonCommandForTest("serve", inv, deps)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetContext(t.Context())
	command.SilenceErrors = true
	command.SilenceUsage = true
	for _, root := range []string{first, second} {
		inv.projectsRoot = root
		if err := command.Execute(); !errors.Is(err, sentinel) {
			t.Fatalf("execution root=%s err=%v", root, err)
		}
		if strings.Contains(errOut.String(), "pinned") {
			t.Fatalf("derived path became explicit pin: %s", errOut.String())
		}
	}
	if calls != 2 || tokens != 1 || out.Len() != 0 {
		t.Fatalf("calls=%d tokens=%d stdout=%s", calls, tokens, out.String())
	}
}

// These cases exercise the actual command-to-native preparation binding; all
// effects and inputs stay private to the case, before listener construction.
func TestDaemonServePreparationFailureBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"state-path", "state-load", "token"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			inv := testInvocation(t, root)
			sentinel := errors.New("private token effect refusal")
			tokenCalls := 0
			deps := daemonDependencies{}
			deps.Token = func() (string, error) { tokenCalls++; return "", sentinel }
			deps.listen = func(string, string) (net.Listener, error) {
				t.Fatal("listener reached after preparation failure")
				return nil, nil
			}
			command := daemonCommandForTest("serve", inv, deps)
			var out, errOut bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&errOut)
			command.SetContext(t.Context())
			command.SilenceErrors, command.SilenceUsage = true, true
			var statePath string
			switch stage {
			case "state-path":
				blocker := filepath.Join(root, "blocker")
				if err := os.WriteFile(blocker, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				inv.projectsRoot = filepath.Join(blocker, "projects")
			case "state-load", "token":
				var err error
				statePath, err = daemonruntime.StatePath(root)
				if err != nil {
					t.Fatal(err)
				}
				if stage == "state-load" {
					if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(statePath, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := command.Execute()
			switch stage {
			case "state-path":
				if !errors.Is(err, syscall.ENOTDIR) {
					t.Fatalf("native path refusal=%v", err)
				}
				data, readErr := os.ReadFile(filepath.Join(root, "blocker"))
				if readErr != nil || string(data) != "preserve" {
					t.Fatalf("ancestor changed: %q,%v", data, readErr)
				}
			case "state-load":
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) || !strings.Contains(err.Error(), "decode daemon state") {
					t.Fatalf("native record refusal=%v", err)
				}
				data, readErr := os.ReadFile(statePath)
				if readErr != nil || string(data) != "{" {
					t.Fatalf("record changed: %q,%v", data, readErr)
				}
			case "token":
				// The per-instance Token error contract is simulated; production random
				// generation does not fail. It must still precede persistence/listening.
				if !errors.Is(err, sentinel) {
					t.Fatalf("token identity=%v", err)
				}
				if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("state persisted after token refusal: %v", statErr)
				}
			}
			expectedTokens := 0
			if stage == "token" {
				expectedTokens = 1
			}
			if tokenCalls != expectedTokens || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("token calls=%d stdout=%q stderr=%q", tokenCalls, out.String(), errOut.String())
			}
		})
	}
}
