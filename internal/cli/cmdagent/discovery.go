package cmdagent

import (
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// NewProfiles builds `wb agent profiles`, the machine-readable profile catalog.
func NewProfiles(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "profiles",
		Short: "List configured execution profiles, providers and harness capabilities",
		Long: `List every configured profile with the harness, provider, model and effort it
resolves to, plus what each harness can report.

This is how an orchestrator discovers model identifiers instead of hardcoding
them: the answer comes from wb.yaml, not from WB.`,
		Example: `wb agent profiles
wb agent profiles --format json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			catalog, err := deps.Profiles(command.Context(), agentrun.DiscoveryRequest{ProjectsRoot: runtime.Flags().ProjectsRoot})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), catalog)
			}
			return printProfiles(command.OutOrStdout(), catalog)
		},
	}
	deps.Discovery(command, "profiles models harness capabilities discover config list json")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func printProfiles(writer io.Writer, catalog agentrun.ProfilesCatalog) error {
	if len(catalog.Profiles) == 0 {
		if _, err := fmt.Fprintln(writer, "no agent profiles configured; add agents.profiles to wb.yaml"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(writer, "%-20s %-10s %-22s %-34s %s\n", "PROFILE", "HARNESS", "PROVIDER", "MODEL", "EFFORT"); err != nil {
			return err
		}
		for _, profile := range catalog.Profiles {
			model := profile.Resolved.Model
			if profile.Resolved.Variant != "" {
				model += "#" + profile.Resolved.Variant
			}
			if !profile.Usable {
				model = "unusable: " + profile.Problem
			}
			if _, err := fmt.Fprintf(writer, "%-20s %-10s %-22s %-34s %s\n",
				profile.Name, profile.Resolved.Harness, emptyWord(profile.Resolved.Provider, "-"),
				model, emptyWord(profile.Resolved.Effort, "-")); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(writer, "\nproviders: "+strings.Join(catalog.Providers, ", ")); err != nil {
		return err
	}
	if catalog.DefaultProfile != "" {
		if _, err := fmt.Fprintln(writer, "default profile: "+catalog.DefaultProfile); err != nil {
			return err
		}
	}
	if len(catalog.Fallback) > 0 {
		if _, err := fmt.Fprintln(writer, "fallback: "+strings.Join(catalog.Fallback, " -> ")); err != nil {
			return err
		}
	}
	for _, harness := range catalog.Harnesses {
		if _, err := fmt.Fprintf(writer, "harness %-9s effort=%-11s usage=%-11s cost=%-16s task=%s isolation=%s\n",
			harness.Harness, harness.Effort, harness.TokenUsage, harness.Cost, harness.TaskDelivery, harness.Isolation); err != nil {
			return err
		}
	}
	return nil
}

// NewTaskTypes builds `wb agent task-types`, the machine-readable task-type
// catalog.
func NewTaskTypes(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "task-types",
		Short: "List task types with the policy each one resolves to",
		Long: `List every built-in and configured task type with its default profile,
complexity map, worktree default, timeout, validation suites and acceptance
policy.

Built-in task types are policy only: they name no profile and no model, so the
answer always comes from the operator's configuration.`,
		Example: `wb agent task-types
wb agent task-types --format json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			catalog, err := deps.TaskTypes(command.Context(), agentrun.DiscoveryRequest{ProjectsRoot: runtime.Flags().ProjectsRoot})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), catalog)
			}
			return printTaskTypes(command.OutOrStdout(), catalog)
		},
	}
	deps.Discovery(command, "task types policies routing complexity profiles discover list json")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func printTaskTypes(writer io.Writer, catalog agentrun.TaskTypesCatalog) error {
	if _, err := fmt.Fprintf(writer, "%-16s %-8s %-18s %-10s %-10s %-10s %s\n",
		"TASK TYPE", "SOURCE", "PROFILE", "WORKTREE", "TIMEOUT", "ACCEPT", "VALIDATE"); err != nil {
		return err
	}
	for _, entry := range catalog.TaskTypes {
		source := "configured"
		if entry.Builtin {
			source = "built-in"
		}
		if _, err := fmt.Fprintf(writer, "%-16s %-8s %-18s %-10s %-10s %-10s %s\n",
			entry.Name, source, emptyWord(entry.Policy.Profile, "-"),
			emptyWord(entry.Policy.Worktree, "auto"), emptyWord(entry.Policy.Timeout, "30m0s"),
			emptyWord(entry.Policy.Acceptance, "required"), emptyWord(strings.Join(entry.Policy.Validate, ","), "-")); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer, "\ndefault task type: "+catalog.Default)
	return err
}
