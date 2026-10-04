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
	lookupRecipient func(string, string) (session.Record, bool)
	listSessions    func(string) ([]session.View, error)
	pid             func() int
	resolveCheckout func(context.Context, string, string) (worktreecollab.Checkout, error)
	observeLegacy   func(string, string) (worktreecollab.ObservedOwner, error)
}

func defaultCollaborationSessionPorts() collaborationSessionPorts {
	return collaborationSessionPorts{root: wbhome.Root, resolveAncestor: session.ResolveForProcess,
		lookupExact: session.LookupExact, lookupRecipient: session.LookupByWBSessionID, listSessions: session.List, pid: os.Getpid,
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
			if view.WBSessionID == id {
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
			exact, live, err := ports.lookupExact(directory, declared.PID)
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
			exact, live, err := ports.lookupExact(directory, record.PID)
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
			status := "unknown"
			matches := 0
			for _, view := range views {
				if view.WBSessionID != id {
					continue
				}
				matches++
				if matches != 1 {
					return "unknown", nil
				}
				exact, live, err := ports.lookupExact(directory, view.PID)
				if err != nil || exact.WBSessionID != id || exact.Lifecycle != "" {
					return "unknown", nil
				}
				if live {
					status = "live"
				} else {
					status = "inactive"
				}
			}
			return status, nil
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
