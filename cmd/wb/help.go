package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const (
	rootGroupAgent    = "agent-workflow"
	rootGroupFleet    = "fleet-operations"
	rootGroupQuality  = "quality-delivery"
	rootGroupChange   = "dependencies-change"
	rootGroupMaintain = "maintenance"
	rootGroupLearn    = "learn"
)

func configureRootHelp(root *cobra.Command) {
	root.AddGroup(
		&cobra.Group{ID: rootGroupAgent, Title: "Agent workflow"},
		&cobra.Group{ID: rootGroupFleet, Title: "Fleet operations"},
		&cobra.Group{ID: rootGroupQuality, Title: "Quality and delivery"},
		&cobra.Group{ID: rootGroupChange, Title: "Dependencies and change"},
		&cobra.Group{ID: rootGroupMaintain, Title: "Maintenance"},
		&cobra.Group{ID: rootGroupLearn, Title: "Learn and configure"},
	)
	root.SetHelpCommand(newWBHelpCommand())
	root.SetHelpCommandGroupID(rootGroupLearn)
	root.SetCompletionCommandGroupID(rootGroupLearn)
}

func groupedRootCommand(command *cobra.Command, group string) *cobra.Command {
	command.GroupID = group
	return command
}

func newWBHelpCommand() *cobra.Command {
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
				return &exitError{code: exitUsage, message: fmt.Sprintf(
					"help topic %q is ambiguous; use one of: %s",
					strings.Join(args, " "), strings.Join(matches, ", "),
				)}
			}
			return &exitError{code: exitUsage, message: fmt.Sprintf(
				"unknown help topic %q; run `wb commands --search %q`",
				strings.Join(args, " "), strings.Join(args, " "),
			)}
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
	visitPublicCommands(root, func(command *cobra.Command) {
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

// prepareHelpPresentation removes inherited selectors from a selected leaf's
// help when the exact same invocation would reject them. Each run builds a new
// command tree, so hiding the shared root flag here cannot leak to another
// invocation or alter parsing.
func prepareHelpPresentation(root *cobra.Command, args []string) {
	target := requestedHelpTarget(root, args)
	if target == nil || target == root {
		return
	}
	commandID := persistentCommandID(target)
	for flag, support := range persistentFlagSupport {
		if support[commandID] || support["*"] || descendantSupportsPersistentFlag(target, support) {
			continue
		}
		if inherited := root.PersistentFlags().Lookup(flag); inherited != nil {
			inherited.Hidden = true
		}
	}
}

func descendantSupportsPersistentFlag(parent *cobra.Command, support map[string]bool) bool {
	if parent.Runnable() || !parent.HasAvailableSubCommands() {
		return false
	}
	found := false
	visitPublicCommands(parent, func(command *cobra.Command) {
		if command.Runnable() && support[persistentCommandID(command)] {
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

func usageRecoveryHint(root *cobra.Command, args []string) string {
	target, _, _ := root.Find(args)
	if target == nil {
		target = root
	}
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

func visitPublicCommands(root *cobra.Command, visit func(*cobra.Command)) {
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
