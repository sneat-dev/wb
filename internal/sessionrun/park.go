package sessionrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ParkOutput struct {
	ParkedSessionID string `json:"parked_session_id"`
	Status          string `json:"status"`
	MemberCount     int    `json:"member_count"`
	// WBSessionID names the parked source session, and RegisteredAtPark says
	// whether park had to register it first. A caller that never ran
	// `wb session register` learns its own identity from exactly here.
	WBSessionID      string `json:"wb_session_id"`
	RegisteredAtPark bool   `json:"registered_at_park"`
}

type ParkRequest struct {
	ProjectsRoot, Runtime, Model, WBSessionID string
	PID                                       int
	Continuation                              []byte
	OverrideSecrets                           []string
}
type ParkRegistration struct {
	PID            int
	Runtime, Model string
}

// ParkResult keeps post-success presentation facts separate from public JSON.
type ParkResult struct {
	Output       ParkOutput
	Warnings     []secretscan.Finding
	Registration ParkRegistration
}
type ParkDependencies struct {
	Directory     func(string) (string, error)
	Home          func(string) (string, error)
	PID           func() int
	ResolveSource func(string, int, session.AutoRegisterHints) (session.Record, bool, error)
	List          func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)
	Store         func(string) sessionpark.Store
	Capture       func(context.Context, string, []worktrees.ListResult, session.Record, func([]sessionpark.Worktree) error) error
	NewID         func() (string, error)
	Now           func() time.Time
	MarkParked    func(string, int, string) (session.Record, error)
	LoadScanner   func() (*secretscan.Scanner, []string, error)
}

func DefaultParkDependencies() ParkDependencies {
	return ParkDependencies{Directory: DirForWrite, Home: wbhome.Root, PID: os.Getpid, ResolveSource: session.ResolveOrRegisterForProcess, List: worktrees.List, Store: sessionpark.NewStore, Capture: worktrees.CaptureParkedSessionAggregate, NewID: sessionpark.NewID, Now: func() time.Time { return time.Now().UTC() }, MarkParked: session.MarkParked, LoadScanner: DefaultScanner}
}

type ParkService struct{ deps ParkDependencies }

func NewPark(deps ParkDependencies) *ParkService { return &ParkService{deps: deps} }
func (s *ParkService) Park(ctx context.Context, request ParkRequest) (ParkResult, error) {
	deps := s.deps
	projectsRoot, runtime, model, wbSessionID, pid, overrideSecrets, body := request.ProjectsRoot, request.Runtime, request.Model, request.WBSessionID, request.PID, request.OverrideSecrets, request.Continuation
	continuation := strings.TrimSpace(string(body))
	if continuation == "" {
		return ParkResult{}, fmt.Errorf("park requires non-empty continuation context via --context-file")
	}
	if len([]byte(continuation)) > sessionpark.MaxContinuationBytes {
		return ParkResult{}, fmt.Errorf("park continuation exceeds %d bytes", sessionpark.MaxContinuationBytes)
	}
	overrides, err := secretscan.ParseOverrides(overrideSecrets)
	if err != nil {
		return ParkResult{}, err
	}
	// The continuation becomes immutable the instant it is stored
	// below (store.Create / the retry-equality check), so the scan
	// must run, and can only refuse, before that point: the write is
	// the damage, not a later commit or push of it.
	secretWarnings, err := ScanContinuation(deps.LoadScanner, overrides, secretscan.Segment{Name: "continuation", Content: []byte(continuation)})
	if err != nil {
		return ParkResult{}, err
	}
	// Session resolution runs only once the continuation is accepted.
	// A park-time registration is a durable write, and a park refused
	// for a missing or secret-bearing continuation must leave no trace
	// of itself — the same reason the scan precedes the store.
	dir, err := deps.Directory(projectsRoot)
	if err != nil {
		return ParkResult{}, err
	}
	source, registeredAtPark, err := deps.ResolveSource(dir, deps.PID(), session.AutoRegisterHints{
		PID: pid, Runtime: runtime, Model: model, WBSessionID: wbSessionID,
	})
	if err != nil {
		return ParkResult{}, err
	}
	results, err := deps.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Workers: 1})
	if err != nil {
		return ParkResult{}, err
	}
	ownedResults := make([]worktrees.ListResult, 0, len(results))
	for _, result := range results {
		if ownedBySession(result, source) {
			ownedResults = append(ownedResults, result)
		}
	}
	home, err := deps.Home(projectsRoot)
	if err != nil {
		return ParkResult{}, err
	}
	store := deps.Store(filepath.Join(home, "parked-sessions"))
	var id string
	var owned []sessionpark.Worktree
	err = deps.Capture(ctx, projectsRoot, ownedResults, source, func(captured []sessionpark.Worktree) error {
		bundle, found, findErr := store.FindBySource(source.WBSessionID)
		if findErr != nil {
			return findErr
		}
		id = bundle.ParkedSessionID
		if found {
			retry := sessionpark.Bundle{
				SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: bundle.ParkedSessionID,
				Source: source, Continuation: continuation, Worktrees: captured, ParkedAt: bundle.ParkedAt,
			}
			if !sessionpark.EqualBundle(bundle, retry) {
				return fmt.Errorf("existing immutable parked session %s conflicts with the current source, continuation, or member evidence; use its original park inputs to repair lifecycle marking", bundle.ParkedSessionID)
			}
			owned = bundle.Worktrees
		} else {
			id, findErr = deps.NewID()
			if findErr != nil {
				return findErr
			}
			bundle = sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: id, Source: source, Continuation: continuation, Worktrees: captured, ParkedAt: deps.Now()}
			if _, createErr := store.Create(bundle); createErr != nil {
				return createErr
			}
			owned = captured
		}
		_, markErr := deps.MarkParked(dir, source.PID, id)
		return markErr
	})
	if err != nil {
		return ParkResult{}, err
	}
	out := ParkOutput{ParkedSessionID: id, Status: string(sessionpark.StatusParked), MemberCount: len(owned),
		WBSessionID: source.WBSessionID, RegisteredAtPark: registeredAtPark}
	return ParkResult{Output: out, Warnings: secretWarnings, Registration: ParkRegistration{PID: source.PID, Runtime: source.Runtime, Model: source.Model}}, nil
}
func ownedBySession(result worktrees.ListResult, source session.Record) bool {
	if result.WorkLogSessionID != "" && result.WorkLogSessionID == source.WBSessionID {
		return true
	}
	for _, owner := range result.Owners {
		if owner.PID == source.PID && !owner.At.Before(source.StartedAt) {
			return true
		}
	}
	return false
}
