package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func prCommandForTest(inv *invocation, name string) *cobra.Command {
	parent := newPRCmd(inv)
	command, _, err := parent.Find([]string{name})
	if err != nil {
		panic(err)
	}
	parent.RemoveCommand(command)
	return command
}
func TestPRRootRegistersCompleteFamily(t *testing.T) {
	t.Parallel()
	command := newPRCmd(&invocation{})
	for _, name := range []string{"create", "update", "land"} {
		leaf, _, err := command.Find([]string{name})
		if err != nil || !leaf.Runnable() {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestPRRootBindsParsedFlagsAndPreservesExactRefusalIdentity(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	state := filepath.Join(root, ".wb", "streams", "broken")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "stream.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{projectsRoot: t.TempDir()}
	command := newRootCmdFor(inv)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetArgs([]string{"--projects-root", root, "pr", "land", "acme/app#1", "--non-interactive", "--format", "json"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(exit.message, "cannot tell whether acme/app holds a live local link") || !strings.Contains(exit.message, "broken (") {
		t.Fatalf("refusal %T %v", err, err)
	}
	if inv.projectsRoot != root || out.Len() != 0 {
		t.Fatal(inv.projectsRoot, out.String())
	}
	// Construct the actual post-checkout callback via create --land before the native missing-task refusal.
	create := prCommandForTest(inv, "create")
	create.SilenceUsage = true
	create.SilenceErrors = true
	create.SetOut(&out)
	create.SetErr(&errOut)
	create.SetArgs([]string{"definitely-absent-task", "--land", "--format", "json"})
	if err := create.Execute(); err == nil {
		t.Fatal("missing task accepted")
	}
	if !strings.Contains(out.String(), "outcome") {
		t.Fatal(out.String())
	}
}
func TestLandingGuardTranslationPreservesOrdinaryErrorsAndExactMessage(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read failed")
	if landingGuardError(nil) != nil || landingGuardError(sentinel) != sentinel {
		t.Fatal("ordinary error identity changed")
	}
	refusal := &landingcontext.Refusal{Message: "exact refusal; clear it with: undo"}
	err := landingGuardError(fmt.Errorf("wrapped: %w", refusal))
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || exit.message != refusal.Message {
		t.Fatalf("refusal %T %v", err, err)
	}
}
func TestWorktreeLandingSharedBindingsRetainNativePolicy(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}
	lane := landingLaneGuardRequest(inv, "wb worktree merge land", "because", true)
	if lane.Owner.WBSessionID != "" || !lane.TakeOver || lane.TakeoverReason != "because" {
		t.Fatal(lane)
	}
	releaseWorktreeMergeLane(inv, orchestrate.WorktreeMergeReceipt{})
	var errOut bytes.Buffer
	lifecycleCheckoutUpdated(&errOut)(t.Context(), orchestrate.CheckoutUpdate{Checkout: filepath.Join(root, "missing")})
	if !strings.Contains(errOut.String(), "warning: lifecycle hooks were not dispatched:") {
		t.Fatal(errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".wb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only ownership created state", err)
	}
}
func TestPRNativeDependencyBindingsUseActualOperationsAndConsole(t *testing.T) {
	t.Parallel()
	deps := prDependencies()
	var out bytes.Buffer
	if deps.IsTerminal(&out) || deps.Interactive(&out, true) {
		t.Fatal("buffer/noninteractive must not be a terminal")
	}
	for _, pair := range [][2]any{{deps.Create, orchestrate.CreatePullRequest}, {deps.Update, orchestrate.UpdatePullRequest}, {deps.Land, orchestrate.LandPullRequest}} {
		if reflect.ValueOf(pair[0]).Pointer() != reflect.ValueOf(pair[1]).Pointer() {
			t.Fatal("native operation binding changed")
		}
	}
	root := t.TempDir()
	if err := deps.CheckRepository(root, "acme/app"); err != nil {
		t.Fatal(err)
	}
	lane := deps.Lane(root, "wb pr land", "because", true)
	if !lane.TakeOver || lane.TakeoverReason != "because" || lane.Owner.WBSessionID != "" {
		t.Fatal(lane)
	}
	deps.CheckoutUpdated(&out)(t.Context(), orchestrate.CheckoutUpdate{Checkout: filepath.Join(root, "absent")})
	if !strings.Contains(out.String(), "warning: lifecycle hooks were not dispatched:") {
		t.Fatal(out.String())
	}
}
