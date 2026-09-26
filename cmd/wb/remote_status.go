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

func newRemoteStatusCmd(inv *invocation) *cobra.Command {
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
			return runRemoteStatus(defaultRemoteDeps(), inv.projectsRoot, stale, machine, jsonOut, os.Stdout, os.Stderr)
		},
	}
	addJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	cmd.Flags().StringVar(&machine, "machine", "", "only this <login>/<machine>")
	return cmd
}

type remoteStatusReport struct {
	Diagnostics remoteStatusDiagnostics `json:"diagnostics"`
	Machines    []remoteMachineRow      `json:"machines"`
	Entries     []remotestate.Entry     `json:"entries"`
	Claims      []claimRow              `json:"claims"`
}

type remoteStatusDiagnostics struct {
	Provider          string    `json:"provider"`
	Store             string    `json:"store"`
	ConfiguredMachine string    `json:"configured_machine"`
	RefreshedAt       time.Time `json:"refreshed_at"`
	StaleMachines     int       `json:"stale_machines"`
	UnknownProvenance int       `json:"unknown_provenance"`
	Mismatches        []string  `json:"mismatches,omitempty"`
}

func runRemoteStatus(deps remoteDeps, projectsRoot string, stale time.Duration, machine string, jsonOut bool, out, errOut io.Writer) error {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	progress := newLiveProgressWithHeartbeat(
		progressOutput(errOut, false), true, remoteProgressHeartbeat(deps),
	)
	progress.start("remote status: refreshing machines and claims")
	status, err := remotestate.ReadStatus(context.Background(), provider)
	if err != nil {
		progress.finish("remote status: refresh failed")
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	entries, claims := status.Machines, status.Claims
	progress.finish(fmt.Sprintf("remote status: refreshed %d machines, %d claims", len(entries), len(claims)))
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
		// Filter claims to match the machine filter in JSON mode. Allocate a
		// fresh slice rather than claimRowsAll[:0]: that in-place compaction
		// shares claimRowsAll's backing array, and since claimRowsAll is
		// still read below (writeStatusWorklist filters it itself, per
		// machine section), overwriting through the alias corrupted it —
		// e.g. a single kept entry got duplicated into every slot it didn't
		// overwrite, rendering "remote claims: b-task, b-task" in TEXT mode.
		filteredClaims := make([]claimRow, 0, len(claimRowsAll))
		for _, cr := range claimRowsAll {
			if cr.Error == "" && cr.Holder == machine {
				filteredClaims = append(filteredClaims, cr)
			}
		}
		claimsForJSON = filteredClaims
	}
	rows := machineRows(entries, deps.now(), stale)
	diagnostics := buildRemoteStatusDiagnostics(cfg, entries, rows, deps.now())
	if jsonOut {
		return json.NewEncoder(out).Encode(remoteStatusReport{Diagnostics: diagnostics, Machines: rows, Entries: entries, Claims: claimsForJSON})
	}
	if machine == "" || len(entries) > 0 {
		writeRemoteStatusDiagnostics(out, diagnostics)
	}
	writeStatusWorklist(out, entries, rows, claimRowsAll)
	return nil
}

func buildRemoteStatusDiagnostics(cfg remotestate.Config, entries []remotestate.Entry, rows []remoteMachineRow, now time.Time) remoteStatusDiagnostics {
	diagnostics := remoteStatusDiagnostics{Provider: cfg.Provider, Store: cfg.StoreID(), ConfiguredMachine: cfg.Machine, RefreshedAt: now}
	for i, entry := range entries {
		if i < len(rows) && rows[i].Stale {
			diagnostics.StaleMachines++
		}
		store := entry.Snapshot.RemoteStore
		if store == "" {
			diagnostics.UnknownProvenance++
			continue
		}
		if store != diagnostics.Store {
			diagnostics.Mismatches = append(diagnostics.Mismatches, entry.Snapshot.Key()+" publishes via "+store)
		}
	}
	return diagnostics
}

func writeRemoteStatusDiagnostics(out io.Writer, diagnostics remoteStatusDiagnostics) {
	_, _ = fmt.Fprintf(out, "remote provider: %s (%s); refreshed %s; stale=%d; provenance_unknown=%d\n",
		diagnostics.Provider, diagnostics.Store, diagnostics.RefreshedAt.UTC().Format(time.RFC3339), diagnostics.StaleMachines, diagnostics.UnknownProvenance)
	for _, mismatch := range diagnostics.Mismatches {
		_, _ = fmt.Fprintf(out, "warning: remote provider mismatch: %s; configured store is %s\n", mismatch, diagnostics.Store)
	}
	_, _ = fmt.Fprintln(out)
}
