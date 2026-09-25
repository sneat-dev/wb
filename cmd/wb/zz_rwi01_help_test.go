package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// rwi01HelpTestRoot builds a command tree with the SAME leaf name ("topic")
// under two different parents, so a bare `wb help topic` cannot resolve to
// one command.
func rwi01HelpTestRoot() (root, helpCmd *cobra.Command) {
	root = &cobra.Command{Use: "wb"}
	leaf := func() *cobra.Command {
		return &cobra.Command{Use: "topic", RunE: func(*cobra.Command, []string) error { return nil }}
	}
	sub1 := &cobra.Command{Use: "sub1"}
	sub1.AddCommand(leaf())
	sub2 := &cobra.Command{Use: "sub2"}
	sub2.AddCommand(leaf())
	root.AddCommand(sub1, sub2)
	helpCmd = newWBHelpCommand()
	root.AddCommand(helpCmd)
	return root, helpCmd
}

// TestRwi01NewWBHelpCommandNoArgsShowsRootHelp drives `wb help` with no
// arguments: it must render the root's own help rather than erroring.
func TestRwi01NewWBHelpCommandNoArgsShowsRootHelp(t *testing.T) {
	t.Parallel()
	root, helpCmd := rwi01HelpTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	if err := helpCmd.RunE(helpCmd, nil); err != nil {
		t.Fatalf("help with no args: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("help with no args wrote nothing")
	}
}

// TestRwi01NewWBHelpCommandAmbiguousTopicListsMatches drives `wb help topic`
// where "topic" names a leaf under two different parents: it must reject
// with an ambiguous-topic usage error naming every match, never silently
// pick one.
func TestRwi01NewWBHelpCommandAmbiguousTopicListsMatches(t *testing.T) {
	t.Parallel()
	root, helpCmd := rwi01HelpTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err := helpCmd.RunE(helpCmd, []string{"topic"})
	if err == nil {
		t.Fatal("ambiguous help topic: want an error, got nil")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("ambiguous help topic: err = %#v, want *exitError", err)
	}
	if exitErr.code != exitUsage {
		t.Fatalf("ambiguous help topic: code = %d, want exitUsage", exitErr.code)
	}
	if !bytes.Contains([]byte(exitErr.message), []byte("ambiguous")) {
		t.Fatalf("ambiguous help topic: message = %q, want it to say ambiguous", exitErr.message)
	}
	if !bytes.Contains([]byte(exitErr.message), []byte("sub1 topic")) || !bytes.Contains([]byte(exitErr.message), []byte("sub2 topic")) {
		t.Fatalf("ambiguous help topic: message = %q, want both matches listed", exitErr.message)
	}
}

// TestRwi01ResolveHelpTargetTooManyArgsReturnsNil covers the fallback path
// where the args cannot resolve as a command AND are not a single topic
// word: resolveHelpTarget must report no target and no candidate matches
// rather than guessing.
func TestRwi01ResolveHelpTargetTooManyArgsReturnsNil(t *testing.T) {
	t.Parallel()
	root, _ := rwi01HelpTestRoot()
	target, matches := resolveHelpTarget(root, []string{"nope", "also-nope"})
	if target != nil {
		t.Fatalf("target = %v, want nil", target)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
}

// TestRwi01ContainsStringFindsValue covers the found-it branch of
// containsString directly.
func TestRwi01ContainsStringFindsValue(t *testing.T) {
	t.Parallel()
	if !containsString([]string{"a", "b", "c"}, "b") {
		t.Fatal("containsString did not find a present value")
	}
	if containsString([]string{"a", "b", "c"}, "z") {
		t.Fatal("containsString found an absent value")
	}
}
