package cmdcatalog

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type refusalWriter struct{ err error }

func (w refusalWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCatalogSearchAndMetadataContracts(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "wb"}
	root.AddGroup(&cobra.Group{ID: "ops", Title: "Operations"})
	a := &cobra.Command{Use: "alpha", Short: "finish work", Aliases: []string{"done"}, GroupID: "ops", Example: "# note\nwb alpha \\\n --yes\n\n# next\nwb alpha --dry-run", Run: func(*cobra.Command, []string) {}}
	b := &cobra.Command{Use: "beta", Short: "finish work", Run: func(*cobra.Command, []string) {}}
	hidden := &cobra.Command{Use: "secret", Hidden: true}
	hidden.AddCommand(&cobra.Command{Use: "nested"})
	root.AddCommand(a, b, hidden, &cobra.Command{Use: "help"}, &cobra.Command{Use: "completion"})
	SetDiscoveryTerms(a, "finish work")
	SetDiscoveryTerms(a, "finish work")
	if got := buildCommandCatalog(root, "alpha"); len(got.Commands) != 1 || got.Commands[0].Path != "wb alpha" {
		t.Fatalf("path-token search = %+v", got)
	}
	got := buildCommandCatalog(root, "FINISH work")
	if len(got.Commands) != 2 || got.Commands[0].Path != "wb alpha" || got.Commands[0].Group != "Operations" {
		t.Fatalf("ranking/metadata=%+v", got)
	}
	if !reflect.DeepEqual(got.Commands[0].Examples, []string{"wb alpha --yes", "wb alpha --dry-run"}) {
		t.Fatalf("examples=%v", got.Commands[0].Examples)
	}
	got.Commands[0].Aliases[0] = "changed"
	if a.Aliases[0] != "done" {
		t.Fatal("catalog aliases mutate source")
	}
	if got := buildCommandCatalog(root, "finish absent"); len(got.Commands) != 0 {
		t.Fatalf("AND search=%+v", got)
	}
	if got := buildCommandCatalog(root, ""); len(got.Commands) != 2 || got.Commands[0].Path != "wb alpha" {
		t.Fatalf("tie ordering=%+v", got)
	}
}

func TestCatalogUsesCurrentWriterAndPropagatesRefusals(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("writer refused")
	for _, format := range []string{"text", "json"} {
		root, command := commandsTestRoot()
		root.SetOut(refusalWriter{sentinel})
		if err := command.Flags().Set("format", format); err != nil {
			t.Fatal(err)
		}
		if err := command.RunE(command, nil); !errors.Is(err, sentinel) {
			t.Fatalf("%s error=%v", format, err)
		}
	}
	root, command := commandsTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	if err := command.Flags().Set("format", "toml"); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err == nil || err.Error() != `unsupported format "toml"; use text or json` || out.Len() != 0 {
		t.Fatalf("format error=%v output=%q", err, out.String())
	}
	root = &cobra.Command{Use: "wb"}
	command = NewCommands()
	root.AddCommand(command)
	_ = command.Flags().Set("search", "absent")
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHelpResolutionAndCodedErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("coded refusal")
	var code int
	var message string
	root := &cobra.Command{Use: "wb"}
	parent := &cobra.Command{Use: "group"}
	leaf := &cobra.Command{Use: "leaf", Aliases: []string{"unique"}, Run: func(*cobra.Command, []string) {}}
	parent.AddCommand(leaf)
	root.AddCommand(parent)
	help := NewHelp(func(c int, m string) error { code = c; message = m; return sentinel })
	root.AddCommand(help)
	var out bytes.Buffer
	root.SetOut(&out)
	for _, args := range [][]string{{"group", "leaf"}, {"unique"}} {
		if err := help.RunE(help, args); err != nil {
			t.Fatal(err)
		}
	}
	if err := help.RunE(help, []string{"missing"}); err != sentinel || code != 2 || !strings.Contains(message, "unknown help topic") {
		t.Fatalf("error=%v code=%d message=%q", err, code, message)
	}
	other := &cobra.Command{Use: "other"}
	other.AddCommand(&cobra.Command{Use: "leaf"})
	root.AddCommand(other)
	if err := help.RunE(help, []string{"leaf"}); err != sentinel || !strings.Contains(message, "ambiguous") {
		t.Fatalf("ambiguous=%v %q", err, message)
	}
}

func TestHelpSelectorPolicyAndRecovery(t *testing.T) {
	t.Parallel()
	makeTree := func() *cobra.Command {
		root := &cobra.Command{Use: "wb"}
		root.PersistentFlags().String("filter", "", "filter")
		root.PersistentFlags().Bool("all", false, "all")
		root.PersistentFlags().Bool("unsupported", false, "unsupported")
		parent := &cobra.Command{Use: "parent"}
		parent.AddCommand(&cobra.Command{Use: "leaf", Run: func(*cobra.Command, []string) {}})
		root.AddCommand(parent)
		return root
	}
	id := func(c *cobra.Command) string { return strings.TrimPrefix(c.CommandPath(), "wb ") }
	support := map[string]map[string]bool{"filter": {"parent leaf": true}, "all": {"*": true}, "unsupported": {}, "absent": {}}
	for _, args := range [][]string{nil, {"parent"}, {"--help"}, {"help", "missing"}, {"missing", "--help"}, {"parent", "--help"}, {"parent", "leaf", "-h"}, {"help", "parent", "leaf"}} {
		root := makeTree()
		PrepareHelp(root, args, support, id)
		requested := len(args) > 1 && (args[0] == "parent" || args[0] == "help" && args[1] == "parent")
		if root.PersistentFlags().Lookup("unsupported").Hidden != requested {
			t.Fatalf("args=%v hidden=%v", args, !requested)
		}
		if root.PersistentFlags().Lookup("filter").Hidden || root.PersistentFlags().Lookup("all").Hidden {
			t.Fatalf("accepted selector hidden args=%v", args)
		}
	}
	root := makeTree()
	for _, args := range [][]string{nil, {"--bad"}, {"unknown"}, {"help", "unknown", "topic"}, {"parent", "leaf", "--bad"}} {
		if hint := UsageRecoveryHint(root, args); !strings.Contains(hint, "run `wb") {
			t.Fatalf("hint=%q", hint)
		}
	}
	if descendantSupportsPersistentFlag(&cobra.Command{Use: "empty"}, map[string]bool{}, id) {
		t.Fatal("empty parent supported")
	}
	root.PersistentFlags().Lookup("filter").Hidden = false
	PrepareHelp(root, []string{"parent", "--help"}, map[string]map[string]bool{"filter": {}}, id)
	if !root.PersistentFlags().Lookup("filter").Hidden {
		t.Fatal("unsupported descendant selector visible")
	}
}
