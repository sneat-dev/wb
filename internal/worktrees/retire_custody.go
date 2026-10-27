package worktrees

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

// retireCustodyFence is the exact current custody observation retirement may
// keep acting under. An initialized collaboration snapshot binds a live
// registered invoking session and its owner epoch/revision; a legacy checkout
// keeps the historical competing-PID policy and has no owner epoch to fence.
type retireCustodyFence struct {
	mode      string
	sessionID string
	epoch     uint64
	revision  uint64
}

func (fence retireCustodyFence) same(other retireCustodyFence) bool {
	return fence.mode == other.mode && fence.sessionID == other.sessionID &&
		fence.epoch == other.epoch && fence.revision == other.revision
}

// retireCustodyCheck is one custody observation, injectable so the retirement
// transaction's last destructive authorization is testable without package
// globals.
type retireCustodyCheck func(context.Context, string, string, ListResult) (retireCustodyFence, error)

// retireCustodyPorts are one custody observation's durable boundaries. They
// are scoped to one call so the exact-checkout resolution, private authority
// load, and registered-session corroboration can be exercised without
// replacing package globals.
type retireCustodyPorts struct {
	resolve     func(context.Context, string, string) (worktreecollab.Checkout, error)
	load        func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error)
	identity    func() AgentIdentity
	liveSession func(string, string) error
}

func productionRetireCustodyPorts() retireCustodyPorts {
	return retireCustodyPorts{
		resolve: ResolveCollaborationCheckout,
		load: func(home string, checkout worktreecollab.Checkout) (worktreecollab.State, bool, error) {
			return worktreecollab.NewStore(home).Load(checkout)
		},
		identity:    CurrentIdentity,
		liveSession: corroborateRetireCustodySession,
	}
}

// retireCheckCustody resolves the selected checkout through Git's own linked
// worktree identity, then requires a securely loaded current coordination
// owner to be the corroborated registered invoking session. Historical custody
// events are observations, not simultaneous owners: once an audited takeover
// is the current authority, an old still-live PID no longer vetoes retirement.
// Without initialized authority the established legacy competing-active-owner
// policy is retained, and every unreadable, corrupt, ambiguous, unregistered,
// or mismatched authority observation refuses.
func retireCheckCustody(ctx context.Context, projectsRoot, home string, entry ListResult) (retireCustodyFence, error) {
	return retireCheckCustodyWith(ctx, projectsRoot, home, entry, productionRetireCustodyPorts())
}

func retireCheckCustodyWith(ctx context.Context, projectsRoot, home string, entry ListResult, ports retireCustodyPorts) (retireCustodyFence, error) {
	checkout, err := ports.resolve(ctx, projectsRoot, entry.WorktreeDir)
	if err != nil {
		return retireCustodyFence{}, fmt.Errorf("retirement custody checkout: %w", err)
	}
	state, found, err := ports.load(home, checkout)
	if err != nil {
		return retireCustodyFence{}, fmt.Errorf("retirement custody authority: %w", err)
	}
	if !found {
		return retireLegacyCustody(entry)
	}
	identity := ports.identity()
	if !identity.Registered || strings.TrimSpace(identity.WBSessionID) == "" {
		return retireCustodyFence{}, fmt.Errorf("retirement requires the corroborated registered invoking session as current owner")
	}
	if state.Owner != identity.WBSessionID {
		return retireCustodyFence{}, fmt.Errorf("retirement requires the corroborated current owner %s; invoking session is %s", state.Owner, identity.WBSessionID)
	}
	if state.OwnerEpoch == 0 || state.Revision == 0 {
		return retireCustodyFence{}, fmt.Errorf("retirement custody authority has no audited owner epoch")
	}
	if err := ports.liveSession(home, state.Owner); err != nil {
		return retireCustodyFence{}, fmt.Errorf("retirement current owner session: %w", err)
	}
	return retireCustodyFence{mode: "collaboration", sessionID: state.Owner, epoch: state.OwnerEpoch, revision: state.Revision}, nil
}

// retireLegacyCustody preserves the pre-coordination policy for a checkout
// that never initialized current authority: a different active PID refuses.
func retireLegacyCustody(entry ListResult) (retireCustodyFence, error) {
	if err := retireCheckOwner(entry); err != nil {
		return retireCustodyFence{}, err
	}
	return retireCustodyFence{mode: "legacy"}, nil
}

// corroborateRetireCustodySession applies the established collaboration owner
// corroboration: exactly one registered session carries the ID, it is live,
// its exact record binds the same ID, and it is not parked or otherwise in a
// lifecycle transition.
func corroborateRetireCustodySession(home, sessionID string) error {
	return corroborateRetireCustodySessionWith(home, sessionID, retireCustodySessionPorts{
		list:        session.List,
		lookupExact: session.LookupExact,
	})
}

type retireCustodySessionPorts struct {
	list        func(string) ([]session.View, error)
	lookupExact func(string, int) (session.Record, bool, error)
}

func corroborateRetireCustodySessionWith(home, sessionID string, ports retireCustodySessionPorts) error {
	directory := filepath.Join(home, session.DirName)
	views, err := ports.list(directory)
	if err != nil {
		return err
	}
	matches := 0
	pid := 0
	for _, view := range views {
		if view.WBSessionID != sessionID {
			continue
		}
		matches++
		pid = view.PID
	}
	if matches != 1 || pid <= 0 {
		return fmt.Errorf("coordination owner session is ambiguous or unavailable")
	}
	exact, live, err := ports.lookupExact(directory, pid)
	if err != nil {
		return err
	}
	if !live || exact.WBSessionID != sessionID || exact.Lifecycle != "" {
		return fmt.Errorf("coordination owner session is not live")
	}
	return nil
}
