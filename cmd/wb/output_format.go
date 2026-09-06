package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addJSONFormatFlags gives commands with a JSON output mode one canonical
// selector and keeps --json as an exact, order-aware shortcut. Both flags write
// the same value, so --json is equivalent to --format=json and --json=false is
// equivalent to --format=text.
func addJSONFormatFlags(command *cobra.Command, jsonOut *bool) {
	command.Flags().Var(&jsonFormatValue{jsonOut: jsonOut}, "format", "stdout format: text or json")
	command.Flags().BoolVar(jsonOut, "json", false, "shortcut for --format=json")
}

func outputFormatChanged(command *cobra.Command) bool {
	return command.Flags().Changed("format") || command.Flags().Changed("json")
}

type jsonFormatValue struct {
	jsonOut *bool
}

func (value *jsonFormatValue) Set(format string) error {
	switch format {
	case "text":
		*value.jsonOut = false
	case "json":
		*value.jsonOut = true
	default:
		return fmt.Errorf("unsupported format %q; use text or json", format)
	}
	return nil
}

func (value *jsonFormatValue) String() string {
	if value != nil && value.jsonOut != nil && *value.jsonOut {
		return "json"
	}
	return "text"
}

func (*jsonFormatValue) Type() string { return "string" }
