package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

func collaborationCommandFixture(t *testing.T) (collaborationServiceFactory, *string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caller := "owner"
	service := worktreecollab.Service{Store: worktreecollab.NewStore(filepath.Join(root, ".wb")), Ports: worktreecollab.ServicePorts{
		Resolve: func(context.Context, string) (worktreecollab.Checkout, error) {
			return worktreecollab.Checkout{ID: "test-checkout", Root: root, GitDir: filepath.Join(root, "gitdir"), CommonDir: filepath.Join(root, "common")}, nil
		},
		Caller:      func() (string, error) { return caller, nil },
		Live:        func(string) (bool, error) { return true, nil },
		OwnerStatus: func(string) (string, error) { return "live", nil },
		ObserveLegacy: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		ObserveLegacyForInspection: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		Now: time.Now, NewMessageID: func() (string, error) { return "message-one", nil },
	}}
	return func() (worktreecollab.Service, error) { return service, nil }, &caller
}

func executeCollaborationCommand(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	err := command.Execute()
	return out.String(), err
}

type collaborationFailWriter struct{}

func (collaborationFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

type collaborationNthFailWriter struct{ writes, failAt int }

func (writer *collaborationNthFailWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("output unavailable")
	}
	return len(value), nil
}

func TestSessionRegisterJoinBoundaryReports(t *testing.T) {
	makeCommand := func(inv *invocation, join collaborationServiceFactory) *cobra.Command {
		deps := sessionrun.DefaultRegisterDependencies()
		deps.CurrentPID = func() int { return -1 }
		deps.RuntimeProcess = func(int, string) bool { return true }
		return cmdsession.NewRegister(newCLIRuntime(inv), cmdsession.Dependencies{Register: sessionrun.NewRegister(deps).Register, Join: func(ctx context.Context, path string) error {
			service, err := join()
			if err != nil {
				return err
			}
			_, err = service.Join(ctx, path)
			return err
		}})
	}
	args := []string{"--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex", "--native-harness-id", "collab-registration", "--wb-session-id", "wbs-registration"}
	inv := &invocation{projectsRoot: t.TempDir()}
	if _, err := executeCollaborationCommand(t, makeCommand(inv, nil), "--pid", "0", "--runtime", "codex"); err == nil {
		t.Fatal("invalid registration accepted")
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCollaborationCommand(t, makeCommand(&invocation{projectsRoot: loop}, nil), args...); err == nil {
		t.Fatal("invalid projects root accepted")
	}
	command := makeCommand(inv, nil)
	command.SetOut(collaborationFailWriter{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("registration output failure = %v", err)
	}
	failing := func() (worktreecollab.Service, error) {
		return worktreecollab.Service{}, errors.New("join unavailable")
	}
	if out, err := executeCollaborationCommand(t, makeCommand(inv, failing), append(args, "--join", "worktree")...); err == nil || !strings.Contains(err.Error(), "registered but not joined") || !strings.Contains(out, "registered") {
		t.Fatalf("post-registration service failure = %q, %v", out, err)
	}
	factory, _ := collaborationCommandFixture(t)
	if out, err := executeCollaborationCommand(t, makeCommand(inv, factory), append(args, "--join", "worktree")...); err == nil || !strings.Contains(err.Error(), "registered but not joined") || !strings.Contains(out, "registered") {
		t.Fatalf("post-registration join failure = %q, %v", out, err)
	}
	service, _ := factory()
	if _, err := service.Take(context.Background(), "worktree", "none", false, ""); err != nil {
		t.Fatal(err)
	}
	joined := makeCommand(inv, factory)
	writer := &collaborationNthFailWriter{failAt: 2}
	joined.SetOut(writer)
	joined.SetErr(&bytes.Buffer{})
	joined.SetArgs(append(args, "--join", "worktree"))
	joined.SilenceUsage, joined.SilenceErrors = true, true
	if err := joined.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") || writer.writes != 2 {
		t.Fatalf("joined output failure = %v; writes=%d", err, writer.writes)
	}
}
