package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// rebindRecoveryFixture binds one stale snapshot whose recorded owner is proven
// inactive to a separately registered live caller, so the command-level proofs
// exercise the same service contract the CLI ships.
func rebindRecoveryFixture(t *testing.T) (collaborationServiceFactory, worktreecollab.Checkout, worktreecollab.Checkout) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldCheckout := worktreecollab.Checkout{ID: "recovery-checkout", Root: filepath.Join(base, "retired"),
		GitDir: filepath.Join(base, "repo", ".git", "worktrees", "checkout1"), CommonDir: filepath.Join(base, "repo", ".git")}
	currentCheckout := oldCheckout
	currentCheckout.Root = filepath.Join(base, "current")
	if err := os.Mkdir(currentCheckout.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	store := worktreecollab.NewStore(filepath.Join(base, ".wb"))
	if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *worktreecollab.State, found bool) error {
		if found {
			t.Fatal("unexpected fixture state")
		}
		return state.Take(worktreecollab.TakeRequest{Caller: "gone", ExpectedOwner: worktreecollab.NoOwner, At: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	service := worktreecollab.Service{Store: store, Ports: worktreecollab.ServicePorts{
		Resolve: func(context.Context, string) (worktreecollab.Checkout, error) { return currentCheckout, nil },
		Caller:  func() (string, error) { return "successor", nil },
		Live:    func(string) (bool, error) { return true, nil },
		OwnerStatus: func(id string) (string, error) {
			if id == "gone" {
				return "inactive", nil
			}
			return "live", nil
		},
		ObserveLegacy: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		ObserveLegacyForInspection: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		},
		Now: time.Now, NewMessageID: func() (string, error) { return "message", nil },
	}}
	return func() (worktreecollab.Service, error) { return service, nil }, oldCheckout, currentCheckout
}

func TestWorktreeRebindCommandRefusesTakeoverFieldsWithoutRecoveryMode(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"--expected-owner=gone", "--expected-owner-epoch=1", "--expected-revision=1", "--reason=retired"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			calls := 0
			factory := func() (worktreecollab.Service, error) {
				calls++
				return worktreecollab.Service{}, errors.New("factory must not run")
			}
			out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(factory), "/checkout", "--expected-root=/retired", field)
			if err == nil || !strings.Contains(err.Error(), "--take-inactive-ownership") || out != "" || calls != 0 {
				t.Fatalf("takeover field without mode: out=%q err=%v factoryCalls=%d", out, err, calls)
			}
		})
	}
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(func() (worktreecollab.Service, error) {
		return worktreecollab.Service{}, errors.New("factory must not run")
	}), "/checkout", "--take-inactive-ownership=false", "--expected-owner=gone")
	if err == nil || !strings.Contains(err.Error(), "--take-inactive-ownership") || out != "" {
		t.Fatalf("explicitly disabled recovery mode accepted takeover fields: out=%q err=%v", out, err)
	}
}

func TestWorktreeRebindCommandRequiresEveryExactRecoveryObservation(t *testing.T) {
	t.Parallel()
	all := map[string]string{"--expected-root": "/retired", "--expected-owner": "gone", "--expected-owner-epoch": "1",
		"--expected-revision": "1", "--reason": "retired"}
	for missing := range all {
		t.Run("missing "+missing, func(t *testing.T) {
			t.Parallel()
			args := []string{"/checkout", "--take-inactive-ownership"}
			for field, value := range all {
				if field != missing {
					args = append(args, field+"="+value)
				}
			}
			out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(func() (worktreecollab.Service, error) {
				return worktreecollab.Service{}, errors.New("factory must not run")
			}), args...)
			if err == nil || !strings.Contains(err.Error(), missing) || out != "" {
				t.Fatalf("missing %s: out=%q err=%v", missing, out, err)
			}
		})
	}
	for _, zero := range []struct{ field, value string }{{"--expected-owner-epoch", "0"}, {"--expected-revision", "0"}} {
		args := []string{"/checkout", "--take-inactive-ownership"}
		for field, value := range all {
			if field == zero.field {
				value = zero.value
			}
			args = append(args, field+"="+value)
		}
		out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(func() (worktreecollab.Service, error) {
			return worktreecollab.Service{}, errors.New("factory must not run")
		}), args...)
		if err == nil || !strings.Contains(err.Error(), "never zero") || out != "" {
			t.Fatalf("%s=0: out=%q err=%v", zero.field, out, err)
		}
	}
}

func TestWorktreeRebindCommandRecoversInactiveOwnershipOnce(t *testing.T) {
	t.Parallel()
	factory, oldCheckout, currentCheckout := rebindRecoveryFixture(t)
	args := []string{currentCheckout.Root, "--take-inactive-ownership", "--expected-root", oldCheckout.Root,
		"--expected-owner", "gone", "--expected-owner-epoch", "1", "--expected-revision", "1",
		"--reason", "recorded owner exited; Git reused the linked registration"}
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(factory), args...)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"recovered " + currentCheckout.ID, oldCheckout.Root + " -> " + currentCheckout.Root,
		"owner gone -> successor", "(epoch 2, revision 2)"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("recovery output %q missing %q", out, fragment)
		}
	}
	service, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	state, found, err := service.Store.Load(currentCheckout)
	if err != nil || !found {
		t.Fatalf("recovered snapshot = %t, %v", found, err)
	}
	if state.Owner != "successor" || state.OwnerEpoch != 2 || len(state.OwnerChanges) != 2 || len(state.CheckoutRebinds) != 1 {
		t.Fatalf("recovered snapshot = %+v", state)
	}
	var audited struct {
		Reason   string `json:"reason"`
		Expected struct {
			Root     string `json:"root"`
			Owner    string `json:"owner"`
			Epoch    uint64 `json:"epoch"`
			Revision uint64 `json:"revision"`
		} `json:"expected"`
		Resulting struct {
			Root     string `json:"root"`
			Owner    string `json:"owner"`
			Epoch    uint64 `json:"epoch"`
			Revision uint64 `json:"revision"`
		} `json:"resulting"`
	}
	if err := json.Unmarshal([]byte(state.OwnerChanges[1].Reason), &audited); err != nil {
		t.Fatalf("recovery audit is not reconstructible: %v (%q)", err, state.OwnerChanges[1].Reason)
	}
	if audited.Reason != "recorded owner exited; Git reused the linked registration" || state.OwnerChanges[1].At.IsZero() ||
		audited.Expected.Root != oldCheckout.Root || audited.Expected.Owner != "gone" ||
		audited.Expected.Epoch != 1 || audited.Expected.Revision != 1 ||
		audited.Resulting.Root != currentCheckout.Root || audited.Resulting.Owner != "successor" ||
		audited.Resulting.Epoch != 2 || audited.Resulting.Revision != 2 {
		t.Fatalf("recovery audit = %+v", state.OwnerChanges[1])
	}
	out, err = executeCollaborationCommand(t, newWorktreeRebindCmd(factory), args...)
	if err == nil || !strings.Contains(err.Error(), "already completed") || out != "" {
		t.Fatalf("repeated recovery: out=%q err=%v", out, err)
	}
	repeated, found, err := service.Store.Load(currentCheckout)
	if err != nil || !found || repeated.OwnerEpoch != 2 || repeated.Revision != 2 {
		t.Fatalf("repeated recovery advanced the snapshot: %+v, %t, %v", repeated, found, err)
	}
}

func TestWorktreeRebindCommandRefusesLiveOwnerRecovery(t *testing.T) {
	t.Parallel()
	factory, oldCheckout, currentCheckout := rebindRecoveryFixture(t)
	service, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	service.Ports.OwnerStatus = func(string) (string, error) { return "live", nil }
	live := func() (worktreecollab.Service, error) { return service, nil }
	out, err := executeCollaborationCommand(t, newWorktreeRebindCmd(live), currentCheckout.Root, "--take-inactive-ownership",
		"--expected-root", oldCheckout.Root, "--expected-owner", "gone", "--expected-owner-epoch", "1",
		"--expected-revision", "1", "--reason", "retired")
	if err == nil || !strings.Contains(err.Error(), "proven inactive") || out != "" {
		t.Fatalf("live owner recovery: out=%q err=%v", out, err)
	}
	state, found, err := service.Store.Load(oldCheckout)
	if err != nil || !found || state.Owner != "gone" || state.Revision != 1 {
		t.Fatalf("refused live-owner recovery changed the snapshot: %+v, %t, %v", state, found, err)
	}
}
