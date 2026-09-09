package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// pluginDescriptor is the stable, typed contract for a WB preconfigured
// plugin. It names only lifecycle verbs WB can run locally; it deliberately
// does not imply that a plugin may inspect repositories, call a service, or
// mutate a graph during an unrelated WB command.
type pluginDescriptor struct {
	ID             string   `json:"id"`
	DefaultEnabled bool     `json:"default_enabled"`
	Command        string   `json:"command"`
	Lifecycle      []string `json:"lifecycle"`
	Summary        string   `json:"summary"`
}

var builtInPlugins = []pluginDescriptor{
	{
		ID:             "codegrapher",
		DefaultEnabled: true,
		Command:        "wb codegrapher",
		Lifecycle:      []string{"status", "install", "update"},
		Summary:        "Code intelligence CLI installed independently from repository graph refresh",
	},
}

func newPluginCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "plugin",
		Short: "Inspect WB's preconfigured tool plugins",
		Long: `WB plugins are typed local-tool integrations. A plugin must explicitly
declare the lifecycle verbs it supports; being preconfigured does not authorize
background repository indexing, service calls, or source changes.`,
	}
	command.AddCommand(newPluginListCmd())
	return command
}

func newPluginListCmd() *cobra.Command {
	var format string
	var jsonShortcut bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List preconfigured WB plugins and their lifecycle",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := codeGrapherFormat(format, jsonShortcut)
			if err != nil {
				return err
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(builtInPlugins)
			}
			for _, plugin := range builtInPlugins {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s\tdefault=%t\t%s\n", plugin.ID, plugin.DefaultEnabled, plugin.Summary); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shortcut for --format=json")
	setDiscoveryTerms(command, "plugin extension integration codegrapher list lifecycle tool")
	return command
}
