package worktreerun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"time"
)

type collaborationSessionPorts struct {
	root            func(string) (string, error)
	resolveAncestor func(string, int) (session.Record, bool)
	lookupExact     func(string, int) (session.Record, bool, error)
	lookupRecord    func(string, session.Record) (session.Record, bool, error)
	lookupRecipient func(string, string) (session.Record, bool)
	listSessions    func(string) ([]session.View, error)
	pid             func() int
	resolveCheckout func(context.Context, string, string) (worktreecollab.Checkout, error)
	observeLegacy   func(string, string) (worktreecollab.ObservedOwner, error)
}

func defaultCollaborationSessionPorts() collaborationSessionPorts {
	return collaborationSessionPorts{root: wbhome.Root, resolveAncestor: session.ResolveForProcess,
		lookupExact: session.LookupExact, lookupRecord: session.LookupActiveRecordExact, lookupRecipient: session.LookupByWBSessionID, listSessions: session.List, pid: os.Getpid,
		resolveCheckout: worktrees.ResolveCollaborationCheckout, observeLegacy: worktrees.ObserveCollaborationLegacyOwner}
}

// NewCollaborationService binds collaboration authority to the registered sessions
// and checkout identity of the projects root supplied at execution time.
func NewCollaborationService(projectsRoot string) (worktreecollab.Service, error) {
	return defaultCollaborationSessionPorts().service(projectsRoot)
}

func (ports collaborationSessionPorts) service(projectsRoot string) (worktreecollab.Service, error) {
	home, err := ports.root(projectsRoot)
	if err != nil {
		return worktreecollab.Service{}, err
	}
	directory := filepath.Join(home, session.DirName)
	uniqueSession := func(id string) (bool, error) {
		views, err := ports.listSessions(directory)
		if err != nil {
			return false, err
		}
		matches := 0
		for _, view := range views {
			if view.WBSessionID == id && (view.State == session.StateLive || view.State == "") {
				matches++
			}
		}
		return matches == 1, nil
	}
	return worktreecollab.Service{Store: worktreecollab.NewStore(home), Ports: worktreecollab.ServicePorts{
		Resolve: func(ctx context.Context, idOrPath string) (worktreecollab.Checkout, error) {
			return ports.resolveCheckout(ctx, projectsRoot, idOrPath)
		},
		Caller: func() (string, error) {
			declared, found := ports.resolveAncestor(directory, ports.pid())
			if !found {
				return "", fmt.Errorf("invoke this command from a live registered ancestor session")
			}
			exact, live, err := ports.exactRecord(directory, declared)
			if err != nil || !live || exact.WBSessionID != declared.WBSessionID || exact.Lifecycle != "" {
				return "", fmt.Errorf("registered ancestor session cannot be corroborated: %v", err)
			}
			unique, err := uniqueSession(exact.WBSessionID)
			if err != nil || !unique {
				return "", fmt.Errorf("registered ancestor session ID is ambiguous or unavailable: %v", err)
			}
			return exact.WBSessionID, nil
		},
		Live: func(id string) (bool, error) {
			unique, err := uniqueSession(id)
			if err != nil || !unique {
				return false, err
			}
			record, found := ports.lookupRecipient(directory, id)
			if !found {
				return false, nil
			}
			exact, live, err := ports.exactRecord(directory, record)
			if err != nil {
				return false, err
			}
			return live && exact.WBSessionID == id && exact.Lifecycle == "", nil
		},
		OwnerStatus: func(id string) (string, error) {
			views, err := ports.listSessions(directory)
			if err != nil {
				return "unknown", nil
			}
			liveCount := 0
			unprovenDuplicates := 0
			seen := false
			uncertain := false
			for _, view := range views {
				if view.WBSessionID != id {
					continue
				}
				seen = true
				if view.State != session.StateGone {
					unprovenDuplicates++
				}
				exact, live, err := ports.exactRecord(directory, view.Record)
				if err != nil || exact.WBSessionID != id || exact.Lifecycle != "" {
					uncertain = true
					continue
				}
				if live {
					liveCount++
				}
			}
			if liveCount > 1 || unprovenDuplicates > 1 || uncertain || !seen {
				return "unknown", nil
			}
			if liveCount == 1 {
				return "live", nil
			}
			return "inactive", nil
		},
		ObserveLegacy: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return ports.observeLegacy(checkout.Root, directory)
		},
		ObserveLegacyForInspection: func(checkout worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktrees.ObserveCollaborationLegacyOwnerReadOnly(checkout.Root, directory)
		},
		Now: time.Now, NewMessageID: session.NewID,
	}}, nil
}

func (ports collaborationSessionPorts) exactRecord(directory string, record session.Record) (session.Record, bool, error) {
	if ports.lookupRecord != nil {
		return ports.lookupRecord(directory, record)
	}
	if record.Scope != "" {
		return session.Record{}, false, fmt.Errorf("scoped collaboration lookup is unavailable")
	}
	return ports.lookupExact(directory, record.PID)
}
