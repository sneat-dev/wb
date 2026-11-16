package cmdmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

func TestBindingCommandsPassExactLocalTargetAndPreserveServiceErrors(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"bind", "unbind"} {
		for _, format := range []string{"json", "text"} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/fail=%v", verb, format, fail), func(t *testing.T) {
					t.Parallel()
					called := false
					failure := errors.New("receiver refused target")
					check := func(root, agent string) error {
						called = true
						if root != "/work" || agent != "mac" {
							t.Fatalf("wrong receiver: %q/%q", root, agent)
						}
						if fail {
							return failure
						}
						return nil
					}
					deps := BindingDependencies{
						Bind: func(_ context.Context, root, agent, socket, thread string) error {
							if socket != "/private/codex.sock" || thread != "thread-one" {
								t.Fatalf("wrong target: %q/%q", socket, thread)
							}
							return check(root, agent)
						},
						Unbind: check,
					}
					command := NewBind(bindingRuntime(), deps)
					args := []string{"--agent", "mac", "--format", format}
					if verb == "bind" {
						args = append(args, "--codex-socket", "/private/codex.sock", "--thread", "thread-one")
					} else {
						command = NewUnbind(bindingRuntime(), deps)
					}
					var out bytes.Buffer
					command.SetOut(&out)
					command.SetErr(&out)
					command.SilenceErrors = true
					command.SilenceUsage = true
					command.SetArgs(args)
					err := command.Execute()
					if !called {
						t.Fatal("service not called")
					}
					if fail {
						if !errors.Is(err, failure) || out.Len() != 0 {
							t.Fatalf("failure lost or success printed: %v %q", err, out.String())
						}
						return
					}
					if err != nil || !strings.Contains(out.String(), "mac") || !strings.Contains(out.String(), verb) {
						t.Fatalf("result: %v %q", err, out.String())
					}
				})
			}
		}
	}
}

func TestBindingValidationRejectsInvalidRequestBeforeAccessingReceiver(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{}, {"--agent", "../mac"}, {"--agent", "mac"},
		{"--agent", "mac", "--codex-socket", "/private/s"},
		{"--agent", "mac", "--thread", "t"},
		{"--agent", "mac", "--format", "yaml"},
		{"unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			command := NewBind(bindingRuntime(), BindingDependencies{})
			command.SilenceErrors = true
			command.SilenceUsage = true
			command.SetArgs(args)
			if command.Execute() == nil {
				t.Fatal("invalid request succeeded")
			}
		})
	}
}

func TestBindingOutputFailureIsReportedAfterSuccessfulMutation(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			called := false
			command := NewUnbind(bindingRuntime(), BindingDependencies{Unbind: func(string, string) error { called = true; return nil }})
			command.SetOut(bindingFailWriter{})
			command.SilenceErrors = true
			command.SilenceUsage = true
			command.SetArgs([]string{"--agent", "mac", "--format", format})
			if err := command.Execute(); err == nil || !called {
				t.Fatalf("write failure lost or mutation not run: %v", err)
			}
		})
	}
}

type bindingFailWriter struct{}

func (bindingFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func bindingRuntime() shared.Runtime {
	return shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{ProjectsRoot: "/work"} },
		ExitError: func(code int, message string) error { return fmt.Errorf("exit %d: %s", code, message) },
	}
}
