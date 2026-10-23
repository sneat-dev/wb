package main

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func addJSONFormatFlags(command *cobra.Command, jsonOut *bool) {
	shared.AddJSONFormatFlags(command, jsonOut)
}
func outputFormatChanged(command *cobra.Command) bool { return shared.OutputFormatChanged(command) }
