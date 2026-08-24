package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemoteStatusCmd() *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	var machine string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Cross-machine worklist: every machine's attention repositories and worktrees",
		Long: `Reads the remote store and renders one section per machine. The local
machine is shown as last published, not re-scanned: wb status stays the live
local view. Entries that cannot be decoded are rendered as error rows and do
not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemoteStatus(defaultRemoteDeps(), projectsRoot, stale, machine, jsonOut, os.Stdout, os.Stderr)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print entries as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	cmd.Flags().StringVar(&machine, "machine", "", "only this <login>/<machine>")
	return cmd
}

type remoteStatusReport struct {
	Machines []remoteMachineRow  `json:"machines"`
	Entries  []remotestate.Entry `json:"entries"`
	Claims   []claimRow          `json:"claims"`
}

func runRemoteStatus(deps remoteDeps, projectsRoot string, stale time.Duration, machine string, jsonOut bool, out, errOut io.Writer) error {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	claims, err := provider.Claims(context.Background())
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	// claimRowsAll is computed from the unfiltered entries, before --machine
	// narrows the slice below: a claim's staleness depends on ITS holder's
	// snapshot, which the --machine filter may otherwise have dropped.
	claimRowsAll := claimRows(claims, entries, deps.now(), stale)
	claimsForJSON := claimRowsAll
	if machine != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Snapshot.Key() == machine {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			_, _ = fmt.Fprintf(errOut, "no machine %s in the remote store\n", machine)
		}
		// Filter claims to match the machine filter in JSON mode.
		filteredClaims := claimRowsAll[:0]
		for _, cr := range claimRowsAll {
			if cr.Error == "" && cr.Holder == machine {
				filteredClaims = append(filteredClaims, cr)
			}
		}
		claimsForJSON = filteredClaims
	}
	rows := machineRows(entries, deps.now(), stale)
	if jsonOut {
		return json.NewEncoder(out).Encode(remoteStatusReport{Machines: rows, Entries: entries, Claims: claimsForJSON})
	}
	writeStatusWorklist(out, entries, rows, claimRowsAll)
	return nil
}
