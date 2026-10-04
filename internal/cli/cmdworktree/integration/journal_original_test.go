package integration

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwWtReadPromptBody(t *testing.T) {
	t.Parallel()
	// Actual filesystem reader through the public command; domain append is a recorder.
	readPromptBody := func(prompt, file string) ([]byte, error) {
		var body []byte
		command := cmdworktree.NewSet(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, func(_ context.Context, o worktrees.LogSteerOptions) (worktrees.LogVerbResult, error) {
			body = o.Body
			return worktrees.LogVerbResult{}, nil
		})
		command.SilenceUsage = true
		command.SilenceErrors = true
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		args := []string{}
		if prompt != "" {
			args = append(args, "--prompt", prompt)
		}
		if file != "" {
			args = append(args, "--prompt-file", file)
		}
		command.SetArgs(args)
		err := command.Execute()
		return body, err
	}
	directory := t.TempDir()
	promptFile := filepath.Join(directory, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("  from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(directory, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPromptBody("", ""); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("neither source = %v", err)
	}
	if _, err := readPromptBody("inline", promptFile); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("both sources = %v", err)
	}
	if body, err := readPromptBody("inline", ""); err != nil || string(body) != "inline" {
		t.Fatalf("inline body = (%q, %v)", body, err)
	}
	if _, err := readPromptBody("   ", ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty inline body = %v", err)
	}
	if body, err := readPromptBody("", promptFile); err != nil || string(body) != "  from a file\n" {
		t.Fatalf("file body = (%q, %v)", body, err)
	}
	if _, err := readPromptBody("", filepath.Join(directory, "missing.txt")); err == nil || !strings.Contains(err.Error(), "read prompt file") {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := readPromptBody("", emptyFile); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty file = %v", err)
	}
}

func TestJournalFinalizeActualFileReadsAndCaps(t *testing.T) {
	t.Parallel()
	// Actual private report files, simulated append; native persisted journal journeys remain root.
	directory := t.TempDir()
	file := filepath.Join(directory, "report")
	if err := os.WriteFile(file, []byte(" exact report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(directory, "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", worktrees.MaxFinalizeReportBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
		fail bool
	}{{file, " exact report\n", false}, {big, "", true}, {filepath.Join(directory, "missing"), "", true}} {
		var got []byte
		released := false
		ops := cmdworktree.JournalOperations{Finalize: func(_ context.Context, o worktrees.LogFinalizeOptions) (worktrees.LogVerbResult, error) {
			got = o.Report
			return worktrees.LogVerbResult{}, nil
		}}
		c := cmdworktree.NewWorkLog(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, ops, func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
			return worktrees.AgentIdentity{}, func() { released = true }, nil
		})
		c.SilenceUsage = true
		c.SilenceErrors = true
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		c.SetArgs([]string{"finalize", "--report", tc.path})
		err := c.Execute()
		if (err != nil) != tc.fail || !released || string(got) != tc.want {
			t.Fatalf("path=%s err=%v released=%v report=%q", tc.path, err, released, got)
		}
	}
}
