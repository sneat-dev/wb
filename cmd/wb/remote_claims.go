package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newRemoteClaimsCmd() *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "List every claim in the remote store, with staleness",
		Long: `Reads the remote store and lists every task claim: who holds it, when
they claimed it, their heartbeat age, and whether that heartbeat is stale
against --stale. Claims that cannot be decoded are rendered as error rows
and do not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemoteClaims(defaultRemoteDeps(), projectsRoot, stale, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print rows as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "a claim's holder is stale once their snapshot is older than this")
	return cmd
}

func runRemoteClaims(deps remoteDeps, projectsRoot string, stale time.Duration, jsonOut bool, out io.Writer) error {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	ctx := context.Background()
	claims, err := provider.Claims(ctx)
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	machines, err := provider.List(ctx)
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	rows := claimRows(claims, machines, deps.now(), stale)
	if jsonOut {
		return json.NewEncoder(out).Encode(rows)
	}
	writeClaimsTable(out, rows)
	return nil
}
