package cmdcatalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func NewHelp(exitError func(int, string) error) *cobra.Command {
	return &cobra.Command{
		Use:                "help [command]",
		Short:              "Open exact command help; unique command names resolve anywhere",
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			root := command.Root()
			if len(args) == 0 {
				return root.Help()
			}
			target, matches := resolveHelpTarget(root, args)
			if target != nil {
				return target.Help()
			}
			if len(matches) > 1 {
				return exitError(shared.ExitUsage, fmt.Sprintf(
					"help topic %q is ambiguous; use one of: %s",
					strings.Join(args, " "), strings.Join(matches, ", "),
				))
			}
			return exitError(shared.ExitUsage, fmt.Sprintf(
				"unknown help topic %q; run `wb commands --search %q`",
				strings.Join(args, " "), strings.Join(args, " "),
			))
		},
	}
}

func resolveHelpTarget(root *cobra.Command, args []string) (*cobra.Command, []string) {
	if target, remaining, err := root.Find(args); err == nil && target != root && len(remaining) == 0 {
		return target, nil
	}
	if len(args) != 1 {
		return nil, nil
	}
	topic := args[0]
	var matches []*cobra.Command
	VisitPublic(root, func(command *cobra.Command) {
		if command.Name() == topic || containsString(command.Aliases, topic) {
			matches = append(matches, command)
		}
	})
	if len(matches) == 1 {
		return matches[0], nil
	}
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		paths = append(paths, match.CommandPath())
	}
	sort.Strings(paths)
	return nil, paths
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func PrepareHelp(root *cobra.Command, args []string, persistentFlagSupport map[string]map[string]bool, commandID func(*cobra.Command) string) {
	target := requestedHelpTarget(root, args)
	if target == nil || target == root {
		return
	}
	id := commandID(target)
	for flag, support := range persistentFlagSupport {
		if support[id] || support["*"] || descendantSupportsPersistentFlag(target, support, commandID) {
			continue
		}
		if inherited := root.PersistentFlags().Lookup(flag); inherited != nil {
			inherited.Hidden = true
		}
	}
}

func descendantSupportsPersistentFlag(parent *cobra.Command, support map[string]bool, commandID func(*cobra.Command) string) bool {
	if parent.Runnable() || !parent.HasAvailableSubCommands() {
		return false
	}
	found := false
	VisitPublic(parent, func(command *cobra.Command) {
		if command.Runnable() && support[commandID(command)] {
			found = true
		}
	})
	return found
}

func requestedHelpTarget(root *cobra.Command, args []string) *cobra.Command {
	if len(args) > 0 && args[0] == "help" {
		target, _ := resolveHelpTarget(root, args[1:])
		return target
	}
	requested := false
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			requested = true
			break
		}
	}
	if !requested {
		return nil
	}
	target, _, err := root.Find(args)
	if err != nil {
		return nil
	}
	return target
}

func UsageRecoveryHint(root *cobra.Command, args []string) string {
	target, _, _ := root.Find(args)
	if target != root && target.Name() != "help" {
		return fmt.Sprintf("run `%s --help` to see the accepted arguments and flags.", target.CommandPath())
	}
	query := firstCommandQuery(args)
	if query != "" {
		return fmt.Sprintf("run `wb commands --search %q` to find the intent, or `wb --help` for all command groups.", query)
	}
	return "run `wb --help` to see the available command groups and flags."
}

func firstCommandQuery(args []string) string {
	if len(args) > 1 && args[0] == "help" {
		return strings.Join(args[1:], " ")
	}
	for _, arg := range args {
		if arg != "" && !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func VisitPublic(root *cobra.Command, visit func(*cobra.Command)) {
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, command := range parent.Commands() {
			if command.Hidden || command.Name() == "help" || command.Name() == "completion" {
				continue
			}
			visit(command)
			walk(command)
		}
	}
	walk(root)
}
