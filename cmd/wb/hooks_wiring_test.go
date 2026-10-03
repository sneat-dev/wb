package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksCompositionUsesCurrentFlagsAndFreshLifecycleState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	inv := &invocation{}
	inv.projectsRoot = filepath.Join(root, "projects")
	if err := os.MkdirAll(inv.projectsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"lifecycle", "check", "--json"},
		{"lifecycle", "check", "--config", filepath.Join(root, "missing.yaml"), "--json"},
		{"lifecycle", "status", "--json"},
		{"lifecycle", "resume", "--json"},
		{"lifecycle", "retry", "absent", "--json"},
		{"lifecycle", "gc", "--json"},
		{"lifecycle", "backfill", "--json"},
		{"lifecycle", "run-pending", "--config", filepath.Join(root, "missing.yaml"), "--state-dir", filepath.Join(root, "worker-state"), "--receipt", filepath.Join(root, "worker-receipt.jsonl")},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newHooksCmd(inv)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			var out, diag bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diag)
			cmd.SetArgs(args)
			cmd.SetContext(context.Background())
			err := cmd.Execute()
			if args[1] == "retry" {
				if err == nil {
					t.Fatal("missing retry succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, diag.String())
			}
		})
	}
	// Projects root is resolved at execution, after the family was constructed.
	cmd := newHooksCmd(inv)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	inv.projectsRoot = filepath.Join(root, "not-created")
	cmd.SetArgs([]string{"lifecycle", "backfill"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("pre-parse root snapshot reused")
	}
}

func TestHooksSharedAdaptersPreserveLegacyBytesAndWriterErrors(t *testing.T) {
	t.Parallel()
	if got := shellQuote("l'été ü"); got != "'l'\\''été ü'" {
		t.Fatal(got)
	}
	if got := argumentOrCurrent([]string{""}); got != "" {
		t.Fatal(got)
	}
	if got := argumentOrCurrent([]string{"a", "b"}); got != "." {
		t.Fatal(got)
	}
	if hookExecutable() == "" {
		t.Fatal("empty hook executable")
	}
	var out bytes.Buffer
	if err := writeLine(&out, "hello", "é"); err != nil {
		t.Fatal(err)
	}
	if err := writeFormat(&out, "%s %d\n", "world", 2); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello é\nworld 2\n" {
		t.Fatal(out.String())
	}
	sentinel := errors.New("closed")
	if err := writeLine(failingWriter{err: sentinel}, "message"); err != sentinel {
		t.Fatal(err)
	}
	if err := writeFormat(failingWriter{err: sentinel}, "message"); err != sentinel {
		t.Fatal(err)
	}
}
