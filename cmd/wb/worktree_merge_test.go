package main

import (
	"strings"
	"testing"
)

func TestWorktreeMergeCommandExposesCombinedAndTwoPhaseJourney(t *testing.T) {
	command := newWorktreeMergeCmd()
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("Use = %q", command.Use)
	}
	for _, flag := range []string{"target", "route", "cleanup", "on-failure", "format"} {
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
	}
	for _, phrase := range []string{"prepared locally, not landed", "never force-push", "exact remote target", "forward revert", "forward repair"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("merge help is missing %q", phrase)
		}
	}
}
