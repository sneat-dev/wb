package cmddisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/diskprune"
)

type pruneHarness struct {
	report  diskprune.Report
	options diskprune.Options
	runs    int
	envs    int
}

func (h *pruneHarness) deps() PruneDependencies {
	return PruneDependencies{
		Env: func() diskprune.Env { h.envs++; return diskprune.Env{} },
		Run: func(_ context.Context, _ diskprune.Env, o diskprune.Options) diskprune.Report {
			h.runs++
			h.options = o
			return h.report
		},
	}
}

func pruneRuntime() shared.Runtime {
	return shared.Runtime{
		Flags:     func() shared.Flags { return shared.Flags{} },
		ExitError: func(code int, message string) error { return &codedError{code, message} },
	}
}

func (h *pruneHarness) execute(args ...string) (string, error) {
	command := New(pruneRuntime(), Dependencies{Prune: h.deps()})
	var out bytes.Buffer
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(append([]string{"prune"}, args...))
	err := command.Execute()
	return out.String(), err
}

func exitCode(err error) int {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	if err != nil {
		return -1
	}
	return 0
}

// Every invalid invocation exits 2 before the machine is read; an empty
// value given explicitly never widens the run.
func TestPruneRejectsInvalidFlagsBeforeReading(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--category", "docker"}, {"--category", "go-module-cache"}, {"--category", "node-package-store"},
		{"--category="}, {"--category=scratch,"}, {"--category=scratch", "--category="},
		{"--older-than", "soon"}, {"--older-than", "1h"}, {"--older-than="}, {"--older-than", "0d"}, {"--older-than", "xd"}, {"--older-than", "106752d"},
		{"--format", "yaml"},
	} {
		h := &pruneHarness{}
		_, err := h.execute(args...)
		if exitCode(err) != shared.ExitUsage || h.envs != 0 || h.runs != 0 {
			t.Errorf("%v: exit %d (%v), env %d, runs %d", args, exitCode(err), err, h.envs, h.runs)
		}
	}
	h := &pruneHarness{}
	if _, err := h.execute("extra"); err == nil || h.runs != 0 {
		t.Fatal("a positional argument was accepted")
	}
}

// The selection reaches the engine; days and comma lists are accepted.
func TestPrunePassesTheSelection(t *testing.T) {
	t.Parallel()
	h := &pruneHarness{report: diskprune.Report{Outcome: diskprune.OutcomeDone}}
	if _, err := h.execute("--apply", "--category", "scratch,go-build-cache", "--older-than", "5d"); err != nil {
		t.Fatal(err)
	}
	if !h.options.Apply || len(h.options.Categories) != 2 || h.options.ScratchAge != 5*24*time.Hour {
		t.Fatalf("options = %#v", h.options)
	}
	if _, err := h.execute("--older-than", "36501d"); err != nil || h.options.ScratchAge != 36501*24*time.Hour {
		t.Fatalf("a long age in days: %#v %v", h.options, err)
	}
	if _, err := h.execute("--older-than", "100h"); err != nil || h.options.ScratchAge != 100*time.Hour || h.options.Apply {
		t.Fatalf("hours: %#v %v", h.options, err)
	}
}

// Outcomes map to exit codes: done is 0, anything else a finding.
func TestPruneOutcomesAndOutput(t *testing.T) {
	t.Parallel()
	h := &pruneHarness{report: diskprune.Report{SchemaVersion: 1, Mode: "plan", Outcome: diskprune.OutcomePlanned, Categories: []diskprune.CategoryReport{
		{Name: "scratch", Status: diskprune.StatusDelete, Bytes: 8192,
			Items: []diskprune.ItemReport{{Path: "/tmp/go-build1", Bytes: 4096, Status: "delete"}, {Path: "/tmp/go-build2", Bytes: 4096, Status: "failed", Error: "EIO"}},
			Kept:  map[string]int{"changed within 72h": 2, "contains a checkout": 1}},
		{Name: "go-build-cache", Status: diskprune.StatusSkipped, Reason: "Go is running (7 go)"},
	}}}
	text, err := h.execute()
	if exitCode(err) != shared.ExitFindings {
		t.Fatalf("exit %v", err)
	}
	for _, want := range []string{"outcome planned", "scratch: delete, 8.0 KB", "/tmp/go-build1", "error: EIO", "kept 2: changed within 72h", "kept 1: contains a checkout", "go-build-cache: skipped (Go is running (7 go))", "--apply"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	out, _ := h.execute("--format", "json")
	var decoded diskprune.Report
	if json.Unmarshal([]byte(out), &decoded) != nil || decoded.Outcome != diskprune.OutcomePlanned {
		t.Fatalf("json = %q", out)
	}
	h.report = diskprune.Report{Mode: "apply", Outcome: diskprune.OutcomeDone}
	if text, err := h.execute("--apply"); err != nil || strings.Contains(text, "--apply to remove") {
		t.Fatalf("done = %q %v", text, err)
	}
}

// A write failure is reported as an error.
func TestPruneReportsAWriteFailure(t *testing.T) {
	t.Parallel()
	report := diskprune.Report{Outcome: diskprune.OutcomeDone}
	if err := writePrune(failingWriter{err: errors.New("EPIPE")}, "text", report); err == nil {
		t.Fatal("a failed write was ignored")
	}
	if err := writePrune(failingWriter{err: errors.New("EPIPE")}, "json", report); err == nil {
		t.Fatal("a failed JSON write was ignored")
	}
	h := &pruneHarness{report: report}
	command := New(pruneRuntime(), Dependencies{Prune: h.deps()})
	command.SetArgs([]string{"prune"})
	command.SetOut(failingWriter{err: errors.New("EPIPE")})
	command.SilenceErrors, command.SilenceUsage = true, true
	if err := command.Execute(); err == nil {
		t.Fatal("the command ignored a write failure")
	}
}

// wb disk's help carries the clean-versus-prune paragraph, and prune's help
// says what it never touches.
func TestDiskHelpExplainsCleanVersusPrune(t *testing.T) {
	t.Parallel()
	command := New(runtimeFor("/p"), Dependencies{Collect: func(context.Context, disk.Options) (disk.Report, error) { return disk.Report{}, nil }})
	if !strings.Contains(command.Long, "wb disk prune empties the shared storeroom") {
		t.Fatal("wb disk help lacks the clean-versus-prune paragraph")
	}
	var prune *cobra.Command
	for _, sub := range command.Commands() {
		if sub.Name() == "prune" {
			prune = sub
		}
	}
	long := ""
	if prune != nil {
		long = strings.Join(strings.Fields(prune.Long), " ")
	}
	for _, want := range []string{"wb clean clears finished workbenches", "go clean -cache, only while no Go process runs", "the Go module cache", "pnpm stores"} {
		if !strings.Contains(long, want) {
			t.Errorf("prune help lacks %q", want)
		}
	}
	if deps := DefaultPruneDependencies(); deps.Env == nil || deps.Run == nil || deps.ConfigPath == nil {
		t.Fatal("default dependencies incomplete")
	}
}
