package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWorktreeMergeForcedProgressIsNewlineDelimited(t *testing.T) {
	var output bytes.Buffer
	writer := &worktreeMergeLineWriter{out: &output}
	for _, text := range []string{"\rworktree merge: preparing", "\rworktree merge: waiting", "\n"} {
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "worktree merge: preparing\nworktree merge: waiting\n"; got != want {
		t.Fatalf("progress output = %q, want %q", got, want)
	}
}

func TestWorktreeMergeCommandExposesCombinedAndTwoPhaseJourney(t *testing.T) {
	command := newWorktreeMergeCmd()
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("Use = %q", command.Use)
	}
	for _, flag := range []string{"target", "route", "cleanup", "on-failure", "format", "progress"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("combined merge is missing --%s", flag)
		}
	}
	if route := command.Flags().Lookup("route"); route == nil || route.DefValue != "auto" {
		t.Fatalf("--route = %#v, want auto", route)
	}
	if cleanup := command.Flags().Lookup("cleanup"); cleanup == nil || cleanup.DefValue != "false" {
		t.Fatalf("--cleanup = %#v, want false", cleanup)
	}
	for _, name := range []string{"prepare", "land", "resume", "revert"} {
		if child, _, err := command.Find([]string{name}); err != nil || child == nil || child.Name() != name {
			t.Errorf("merge command is missing %s: child=%v err=%v", name, child, err)
		}
		if child, _, _ := command.Find([]string{name}); child.Flags().Lookup("progress") == nil {
			t.Errorf("merge %s is missing --progress", name)
		}
	}
	for _, phrase := range []string{"prepared locally, not landed", "never force-push", "exact remote target", "forward revert", "forward repair"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("merge help is missing %q", phrase)
		}
	}
}
