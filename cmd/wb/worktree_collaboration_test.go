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

func TestWorktreeRebindCommandRequiresExpectedRootAndRecordsAudit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(root, "retired-worktree-root")
	oldCheckout := worktreecollab.Checkout{ID: "rebind-checkout", Root: oldRoot, GitDir: filepath.Join(root, "gitdir"), CommonDir: filepath.Join(root, "common")}
	currentCheckout := oldCheckout
	currentCheckout.Root = root
	store := worktreecollab.NewStore(filepath.Join(root, ".wb"))
	if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *worktreecollab.State, found bool) error {
		if found {
			t.Fatal("unexpected fixture state")
		}
		return state.Take(worktreecollab.TakeRequest{Caller: "owner", ExpectedOwner: worktreecollab.NoOwner, At: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	service := worktreecollab.Service{Store: store, Ports: worktreecollab.ServicePorts{
		Resolve:     func(context.Context, string) (worktreecollab.Checkout, error) { return currentCheckout, nil },
		Caller:      func() (string, error) { return "owner", nil },
		Live:        func(string) (bool, error) { return true, nil },
		OwnerStatus: func(string) (string, error) { return "live", nil },
		ObserveLegacy: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		ObserveLegacyForInspection: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		Now: time.Now, NewMessageID: func() (string, error) { return "message", nil },
	}}
	factory := func() (worktreecollab.Service, error) { return service, nil }
	if out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(factory), root); err == nil || out != "" || !strings.Contains(err.Error(), "--expected-root is required") {
		t.Fatalf("missing expected root = %q, %v", out, err)
	}
	if out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(factory), root, "--expected-root", filepath.Join(root, "wrong-old-root")); err == nil || out != "" {
		t.Fatalf("stale-root mismatch = %q, %v; want an error and no success output", out, err)
	}
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(factory), root, "--expected-root", oldRoot)
	if err != nil || !strings.Contains(out, oldRoot+" -> "+root) {
		t.Fatalf("explicit rebind = %q, %v", out, err)
	}
	loaded, found, err := store.Load(currentCheckout)
	if err != nil || !found || len(loaded.CheckoutRebinds) != 1 || loaded.CheckoutRebinds[0].Actor != "owner" {
		t.Fatalf("rebind audit snapshot = %+v, %t, %v", loaded, found, err)
	}
}

func TestWorktreeRebindCommandPropagatesFactoryFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("collaboration registry unavailable")
	calls := 0
	factory := func() (worktreecollab.Service, error) {
		calls++
		return worktreecollab.Service{}, failure
	}
	command := newWorktreeRebindCmd(factory)
	output, err := executeCollaborationCommand(t, command, "/checkout", "--expected-root", "/retired")
	if !errors.Is(err, failure) || output != "" || calls != 1 {
		t.Fatalf("factory failure: output=%q err=%v calls=%d", output, err, calls)
	}
}
