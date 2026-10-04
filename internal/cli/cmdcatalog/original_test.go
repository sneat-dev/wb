package cmdcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	commands := NewCommands()
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
	var catalog Catalog
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
	helpCmd = NewHelp(func(_ int, message string) error { return fmt.Errorf("%s", message) })
	root.AddCommand(helpCmd)
	return root, helpCmd
}

func TestNewWBHelpCommandNoArgsShowsRootHelp(t *testing.T) {
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

func TestResolveHelpTargetTooManyArgsReturnsNil(t *testing.T) {
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

func TestContainsStringFindsValue(t *testing.T) {
	t.Parallel()
	if !containsString([]string{"a", "b", "c"}, "b") {
		t.Fatal("containsString did not find a present value")
	}
	if containsString([]string{"a", "b", "c"}, "z") {
		t.Fatal("containsString found an absent value")
	}
}
