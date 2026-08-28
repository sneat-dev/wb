package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const discoveryTermsAnnotation = "wb.dev/discovery-terms"

type commandCatalog struct {
	SchemaVersion int            `json:"schema_version"`
	Query         string         `json:"query,omitempty"`
	Commands      []commandEntry `json:"commands"`
}

type commandEntry struct {
	Path           string   `json:"path"`
	Summary        string   `json:"summary"`
	Aliases        []string `json:"aliases,omitempty"`
	Examples       []string `json:"examples,omitempty"`
	DiscoveryTerms []string `json:"discovery_terms,omitempty"`
	Group          string   `json:"group,omitempty"`
	Runnable       bool     `json:"runnable"`
	HasSubcommands bool     `json:"has_subcommands"`
	score          int
}

func newCommandsCmd() *cobra.Command {
	var search, format string
	command := &cobra.Command{
		Use:   "commands",
		Short: "Search WB commands by intent or emit the machine-readable catalog",
		Long: `Search every public WB command without recursively opening help pages.

The JSON catalog is designed for cold AI agents and adapters. It includes exact
paths, aliases, examples, discovery terms, grouping, and whether a command is
runnable even when it also owns subcommands. Search requires every query word
to match and ranks the most specific intent first.`,
		Example: `# Find how to finish and land agent work
wb commands --search "finish work" --format json

# Inventory all public commands as stable machine-readable data
wb commands --format json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			catalog := buildCommandCatalog(command.Root(), search)
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(catalog)
			}
			for _, entry := range catalog.Commands {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s\t%s\n", entry.Path, entry.Summary); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&search, "search", "", "intent or keywords; every word must match")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "find discover search list command help capability agent json")
	return command
}

func setDiscoveryTerms(command *cobra.Command, terms string) {
	if command.Annotations == nil {
		command.Annotations = map[string]string{}
	}
	command.Annotations[discoveryTermsAnnotation] = terms
}

func buildCommandCatalog(root *cobra.Command, query string) commandCatalog {
	terms := strings.Fields(strings.ToLower(query))
	entries := make([]commandEntry, 0)
	visitPublicCommands(root, func(command *cobra.Command) {
		entry := commandCatalogEntry(command)
		discovery := strings.ToLower(strings.Join(entry.DiscoveryTerms, " "))
		haystack := strings.ToLower(strings.Join([]string{
			entry.Path,
			entry.Summary,
			command.Long,
			strings.Join(entry.Aliases, " "),
			strings.Join(entry.Examples, " "),
			discovery,
		}, " "))
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				return
			}
			entry.score += 5
			if strings.Contains(discovery, term) {
				entry.score += 8
			}
			if strings.Contains(strings.ToLower(entry.Path), term) {
				entry.score += 4
			}
		}
		if query != "" && strings.Contains(haystack, strings.ToLower(query)) {
			entry.score += 20
		}
		entries = append(entries, entry)
	})
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].Path < entries[j].Path
	})
	return commandCatalog{SchemaVersion: 1, Query: query, Commands: entries}
}

func commandCatalogEntry(command *cobra.Command) commandEntry {
	terms := strings.Fields(command.Annotations[discoveryTermsAnnotation])
	return commandEntry{
		Path:           command.CommandPath(),
		Summary:        command.Short,
		Aliases:        append([]string(nil), command.Aliases...),
		Examples:       catalogExamples(command.Example),
		DiscoveryTerms: terms,
		Group:          commandGroupTitle(command),
		Runnable:       command.Runnable(),
		HasSubcommands: command.HasAvailableSubCommands(),
	}
}

func catalogExamples(raw string) []string {
	var examples []string
	var parts []string
	flush := func() {
		if len(parts) > 0 {
			examples = append(examples, strings.Join(parts, " "))
			parts = nil
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			flush()
			continue
		}
		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		parts = append(parts, line)
		if !continued {
			flush()
		}
	}
	flush()
	return examples
}

func commandGroupTitle(command *cobra.Command) string {
	parent := command.Parent()
	if parent == nil || command.GroupID == "" {
		return "Commands"
	}
	for _, group := range parent.Groups() {
		if group.ID == command.GroupID {
			return group.Title
		}
	}
	return command.GroupID
}
