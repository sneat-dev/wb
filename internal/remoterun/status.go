package remoterun

import (
	"context"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type StatusReport struct {
	Diagnostics StatusDiagnostics   `json:"diagnostics"`
	Machines    []MachineRow        `json:"machines"`
	Entries     []remotestate.Entry `json:"entries"`
	Claims      []ClaimRow          `json:"claims"`
}
type StatusDiagnostics struct {
	Provider          string    `json:"provider"`
	Store             string    `json:"store"`
	ConfiguredMachine string    `json:"configured_machine"`
	RefreshedAt       time.Time `json:"refreshed_at"`
	StaleMachines     int       `json:"stale_machines"`
	UnknownProvenance int       `json:"unknown_provenance"`
	Mismatches        []string  `json:"mismatches,omitempty"`
}

func runRemoteStatus(deps Dependencies, projectsRoot string, stale time.Duration, machine string, progress StatusProgress) (StatusResult, error) {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return StatusResult{}, err
	}
	progress.Start("remote status: refreshing machines and claims")
	status, err := remotestate.ReadStatus(context.Background(), provider)
	if err != nil {
		progress.Finish("remote status: refresh failed")
		return StatusResult{}, deps.ExitError(1, "read remote store: "+err.Error())
	}
	entries, claims := status.Machines, status.Claims
	progress.Finish(fmt.Sprintf("remote status: refreshed %d machines, %d claims", len(entries), len(claims)))
	// ClaimRowsAll is computed from the unfiltered entries, before --machine
	// narrows the slice below: a claim's staleness depends on ITS holder's
	// snapshot, which the --machine filter may otherwise have dropped.
	ClaimRowsAll := ClaimRows(claims, entries, deps.Now(), stale)
	claimsForJSON := ClaimRowsAll
	missing := false
	if machine != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Snapshot.Key() == machine {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			missing = true
		}
		// Filter claims to match the machine filter in JSON mode. Allocate a
		// fresh slice rather than ClaimRowsAll[:0]: that in-place compaction
		// shares ClaimRowsAll's backing array, and since ClaimRowsAll is
		// still read below (writeStatusWorklist filters it itself, per
		// machine section), overwriting through the alias corrupted it —
		// e.g. a single kept entry got duplicated into every slot it didn't
		// overwrite, rendering "remote claims: b-task, b-task" in TEXT mode.
		filteredClaims := make([]ClaimRow, 0, len(ClaimRowsAll))
		for _, cr := range ClaimRowsAll {
			if cr.Error == "" && cr.Holder == machine {
				filteredClaims = append(filteredClaims, cr)
			}
		}
		claimsForJSON = filteredClaims
	}
	rows := MachineRows(entries, deps.Now(), stale)
	diagnostics := buildStatusDiagnostics(cfg, entries, rows, deps.Now())
	return StatusResult{Report: StatusReport{Diagnostics: diagnostics, Machines: rows, Entries: entries, Claims: claimsForJSON}, AllClaims: ClaimRowsAll, MissingMachine: missing}, nil
}
func buildStatusDiagnostics(cfg remotestate.Config, entries []remotestate.Entry, rows []MachineRow, now time.Time) StatusDiagnostics {
	diagnostics := StatusDiagnostics{Provider: cfg.Provider, Store: cfg.StoreID(), ConfiguredMachine: cfg.Machine, RefreshedAt: now}
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

type StatusRequest struct {
	ProjectsRoot, Machine string
	Stale                 time.Duration
}
type StatusProgress struct{ Start, Finish func(string) }
type StatusResult struct {
	Report         StatusReport
	AllClaims      []ClaimRow
	MissingMachine bool
}

func (s *Service) Status(req StatusRequest, progress StatusProgress) (StatusResult, error) {
	return runRemoteStatus(s.deps, req.ProjectsRoot, req.Stale, req.Machine, progress)
}
