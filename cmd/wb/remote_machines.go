package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newRemoteMachinesCmd() *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "machines",
		Short: "List every machine in the remote store with its publish age",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemoteMachines(defaultRemoteDeps(), projectsRoot, stale, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print rows as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	return cmd
}

func runRemoteMachines(deps remoteDeps, projectsRoot string, stale time.Duration, jsonOut bool, out io.Writer) error {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	rows := machineRows(entries, deps.now(), stale)
	if jsonOut {
		return json.NewEncoder(out).Encode(rows)
	}
	writeMachinesTable(out, rows)
	return nil
}
