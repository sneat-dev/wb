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

func TestNewWBHelpCommandAmbiguousTopicListsMatches(t *testing.T) {
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
