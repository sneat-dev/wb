package main

import (
	"bytes"
	"errors"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeCreateGuardRootBindingsUseActualPolicyAndLazyProjectsRoot(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	prompt := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(prompt, []byte("private original request\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{projectsRoot: "before-construction"}
	create := newWorktreeCreateCmd(inv)
	inv.projectsRoot = projects
	var out, stderr bytes.Buffer
	create.SetOut(&out)
	create.SetErr(&stderr)
	create.SilenceUsage = true
	create.SilenceErrors = true
	create.SetArgs([]string{"wiring", "acme/absent", "--mode", "manual", "--initiator", "human", "--model", "unknown", "--original-prompt-file", prompt})
	err := create.Execute()
	if err == nil || !strings.Contains(err.Error(), projects) || strings.Contains(err.Error(), "before-construction") || out.Len() != 0 {
		t.Fatalf("actual lazy create=%v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
	var coded *exitError
	if errors.As(err, &coded) {
		t.Fatalf("native hook failure must retain ordinary identity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projects, ".worktrees")); !os.IsNotExist(err) {
		t.Fatalf("preflight mutated worktree store: %v", err)
	}
	// The registered root-level alias and nested constructor share actual flags.
	for _, c := range []*cobra.Command{newCreateCmd(inv), newWorktreeCreateCmd(inv)} {
		if c.Flags().Lookup("no-claim") == nil || c.Flags().Lookup("model") == nil || c.Flags().Lookup("base").DefValue != "main" || !strings.Contains(c.Annotations[discoveryTermsAnnotation], "quiet") {
			t.Fatalf("actual create registration/flags/quiet annotations=%+v", c)
		}
	}
	guard := newWorktreeGuardCmd(inv)
	guard.SetOut(&out)
	guard.SetErr(&stderr)
	guard.SilenceUsage = true
	guard.SilenceErrors = true
	guard.SetArgs([]string{"--admission", "invalid"})
	if err := guard.Execute(); err == nil || err.Error() != "unsupported admission mode \"invalid\"; use off, warn, or enforce" || errors.As(err, &coded) {
		t.Fatalf("actual guard ordinary error=%v", err)
	}
}
