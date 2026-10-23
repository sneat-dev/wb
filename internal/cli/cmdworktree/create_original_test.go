package cmdworktree

import (
	"encoding/json"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestWorktreeCreateJSONDisabledShapeIsPlainArray(t *testing.T) {
	t.Parallel()
	results := []worktrees.CreateResult{{Repository: "acme/app"}}
	got := worktreeCreateJSON(worktreerun.RemoteClaimOutcome{Outcome: "disabled"}, results)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var arr []worktrees.CreateResult
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("disabled shape must be the plain array exactly as before: %v: %s", err, data)
	}
	if len(arr) != 1 || arr[0].Repository != "acme/app" {
		t.Fatalf("arr = %+v", arr)
	}
}

func TestWorktreeCreateJSONAttemptedShapeWrapsResult(t *testing.T) {
	t.Parallel()
	results := []worktrees.CreateResult{{Repository: "acme/app"}}
	got := worktreeCreateJSON(worktreerun.RemoteClaimOutcome{Outcome: "acquired"}, results)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct {
		RemoteClaim worktreerun.RemoteClaimOutcome `json:"remote_claim"`
		Worktrees   []worktrees.CreateResult       `json:"worktrees"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		t.Fatalf("attempted shape must wrap remote_claim + worktrees: %v: %s", err, data)
	}
	if wrapped.RemoteClaim.Outcome != "acquired" || len(wrapped.Worktrees) != 1 || wrapped.Worktrees[0].Repository != "acme/app" {
		t.Fatalf("wrapped = %+v", wrapped)
	}
}

func TestCwWtValidateWorktreeBranchFlags(t *testing.T) {
	t.Parallel()
	validate := func(command *cobra.Command, branch string) error {
		return shared.ValidateBranchChoice(branch, command.Flags().Changed("branch"), command.Flags().Changed("branch-prefix"))
	}
	build := func(args ...string) *cobra.Command {
		command := &cobra.Command{Use: "x", Args: cobra.ExactArgs(2)}
		var branch string
		command.Flags().StringVar(&branch, "branch", "", "")
		command.Flags().StringVar(new(string), "branch-prefix", "", "")
		command.SetArgs(args)
		_ = command.ParseFlags(args)
		return command
	}
	if err := validate(build(), ""); err != nil {
		t.Fatalf("no flags = %v", err)
	}
	if err := validate(build("--branch", "b"), "b"); err != nil {
		t.Fatalf("branch only = %v", err)
	}
	if err := validate(build("--branch", "b", "--branch-prefix", "p"), "b"); err == nil {
		t.Fatal("both flags must be refused")
	}
	command := build("--branch=")
	if err := validate(command, ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("explicitly empty branch = %v", err)
	}
}
