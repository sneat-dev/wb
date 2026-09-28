package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandsTestRoot builds a small cobra tree so buildCommandCatalog's walk
// (which visits root's children, not root itself) has at least one public
// command to report: a real Root() ancestor with the commands command and a
// leaf sibling attached.
func commandsTestRoot() (*cobra.Command, *cobra.Command) {
	root := &cobra.Command{Use: "wb"}
	leaf := &cobra.Command{Use: "ping", Short: "ping the fleet", Run: func(*cobra.Command, []string) {}}
	commands := newCommandsCmd()
	root.AddCommand(leaf, commands)
	return root, commands
}

// TestCommandsCmdRendersTextRows drives the RunE text branch: format=="json"
// false, the entries range actually iterates, and each row is written with
// fmt.Fprintf before returning nil.
func TestCommandsCmdRendersTextRows(t *testing.T) {
	t.Parallel()
	root, _ := commandsTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"commands"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "ping\tping the fleet") {
		t.Fatalf("text output = %q; want a ping\\tping the fleet row", out.String())
	}
}

// TestCommandsCmdRendersJSONCatalog drives the RunE `format == "json"`
// branch.
func TestCommandsCmdRendersJSONCatalog(t *testing.T) {
	t.Parallel()
	root, _ := commandsTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"commands", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var catalog commandCatalog
	if err := json.Unmarshal(out.Bytes(), &catalog); err != nil {
		t.Fatalf("unmarshal catalog: %v; body=%s", err, out.String())
	}
	if len(catalog.Commands) == 0 {
		t.Fatal("catalog.Commands is empty; want at least the ping and commands entries")
	}
}

// TestCommandGroupTitleFallsBackWithoutAParentGroup drives
// commandGroupTitle's no-parent and no-matching-group branches.
func TestCommandGroupTitleFallsBackWithoutAParentGroup(t *testing.T) {
	t.Parallel()
	standalone := &cobra.Command{Use: "solo"}
	if got := commandGroupTitle(standalone); got != "Commands" {
		t.Fatalf("commandGroupTitle(no parent) = %q; want Commands", got)
	}

	parent := &cobra.Command{Use: "root"}
	parent.AddGroup(&cobra.Group{ID: "core", Title: "Core Commands"})
	child := &cobra.Command{Use: "child", GroupID: "other"}
	parent.AddCommand(child)
	if got := commandGroupTitle(child); got != "other" {
		t.Fatalf("commandGroupTitle(unmatched group) = %q; want the raw group id", got)
	}

	matched := &cobra.Command{Use: "matched", GroupID: "core"}
	parent.AddCommand(matched)
	if got := commandGroupTitle(matched); got != "Core Commands" {
		t.Fatalf("commandGroupTitle(matched group) = %q; want Core Commands", got)
	}
}
