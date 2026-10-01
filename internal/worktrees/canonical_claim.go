package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Claim modes. The empty mode is the default and means ClaimModeWorktree.
const (
	ClaimModeWorktree  = "worktree"
	ClaimModeCanonical = "canonical"
)

const leaseExtensionEventType = "worktree.canonical_lease_extended"

// CanonicalClaimState is the answer of LookupCanonicalClaim.
type CanonicalClaimState string

const (
	// CanonicalClaimNone means the clone carries no Work Log projection.
	CanonicalClaimNone CanonicalClaimState = "none"
	// CanonicalClaimLive means a canonical claim is active and its lease has
	// not lapsed.
	CanonicalClaimLive CanonicalClaimState = "live"
	// CanonicalClaimLapsed means a canonical claim is active (unsealed) and its
	// lease has passed.
	CanonicalClaimLapsed CanonicalClaimState = "lapsed"
	// CanonicalClaimSealed means the clone's projection is terminal: the
	// canonical claim it names was landed or released.
	CanonicalClaimSealed CanonicalClaimState = "sealed"
)

// CanonicalClaim describes the claim LookupCanonicalClaim found. Every field
// except State is empty for CanonicalClaimNone. LeaseExpiresAt is the
// effective expiry (the original lease, or the latest extension).
type CanonicalClaim struct {
	State          CanonicalClaimState
	ClaimID        string
	Task           string
	Repository     string
	Branch         string
	LeaseExpiresAt time.Time
}

// LookupCanonicalClaim reports the canonical claim state of the clone at
// clonePath as of now. It is read-only and never cached: each call reads the
// clone's projection once and the immutable claim once (plus its append-only
// lease-extension evidence), through the same corroboration as
// activeWorkLogClaim. The clock is injected; the lookup never reads time
// itself.
//
// The lease is lapsed from the instant it expires: now >= expiry.
//
// It fails closed. Any read, parse, repository or corroboration error, a
// relative path, a zero clock, or a claim that is not a canonical claim for
// exactly this path returns an error and a zero CanonicalClaim, never a state
// that could admit a write. A caller that cannot read the claim must refuse.
func LookupCanonicalClaim(home, clonePath string, now time.Time) (CanonicalClaim, error) {
	if now.IsZero() {
		return CanonicalClaim{}, errors.New("canonical claim lookup needs a clock reading")
	}
	if !filepath.IsAbs(clonePath) {
		return CanonicalClaim{}, fmt.Errorf("canonical claim lookup needs an absolute clone path, got %q", clonePath)
	}
	clonePath = filepath.Clean(clonePath)
	claim, projection, _, err := activeWorkLogClaimReadOnly(home, clonePath)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return CanonicalClaim{State: CanonicalClaimNone}, nil
	}
	if err != nil {
		if projection.Lifecycle == "terminal" {
			return sealedCanonicalClaim(home, clonePath, projection)
		}
		return CanonicalClaim{}, err
	}
	if claim.Mode != ClaimModeCanonical {
		return CanonicalClaim{}, fmt.Errorf("work-log claim %s at %s is not a canonical claim", claim.ClaimID, clonePath)
	}
	expiry, err := effectiveCanonicalLease(home, claim)
	if err != nil {
		return CanonicalClaim{}, err
	}
	state := CanonicalClaimLive
	if !now.Before(expiry) {
		state = CanonicalClaimLapsed
	}
	return canonicalClaimResult(state, claim, expiry), nil
}

func canonicalClaimResult(state CanonicalClaimState, claim workLogClaim, expiry time.Time) CanonicalClaim {
	return CanonicalClaim{State: state, ClaimID: claim.ClaimID, Task: claim.Task,
		Repository: claim.Repository, Branch: claim.Branch, LeaseExpiresAt: expiry}
}

// sealedCanonicalClaim confirms a terminal projection against the private
// claim and terminal records. It deliberately does not corroborate against
// live Git: a sealed claim's clone is back on its base branch, which is the
// expected state, not an error.
func sealedCanonicalClaim(home, clonePath string, projection workLogProjection) (CanonicalClaim, error) {
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return CanonicalClaim{}, err
	}
	defer func() { _ = runDir.Close() }()
	claim, err := readWorkLogClaimAt(runDir, projection.ClaimID)
	if err != nil {
		return CanonicalClaim{}, err
	}
	terminal, err := readWorkLogTerminalAt(runDir, projection.ClaimID)
	if err != nil {
		return CanonicalClaim{}, err
	}
	if claim.ClaimID != projection.ClaimID || terminal.ClaimID != projection.ClaimID || filepath.Clean(claim.Worktree) != clonePath {
		return CanonicalClaim{}, errors.New("terminal work-log projection does not match its immutable claim and terminal records")
	}
	if claim.Mode != ClaimModeCanonical {
		return CanonicalClaim{}, fmt.Errorf("work-log claim %s at %s is not a canonical claim", claim.ClaimID, clonePath)
	}
	if err := validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
		return CanonicalClaim{}, err
	}
	return canonicalClaimResult(CanonicalClaimSealed, claim, *claim.LeaseExpiresAt), nil
}

// validateClaimModeLease is the single statement of the mode/lease contract:
// the mode is one of the known values, a canonical claim carries a lease that
// ends after the instant it was recorded, and no other claim carries one.
func validateClaimModeLease(mode string, lease, recordedAt time.Time) error {
	switch mode {
	case "", ClaimModeWorktree:
		if !lease.IsZero() {
			return errors.New("lease_expires_at is valid only for a canonical claim")
		}
	case ClaimModeCanonical:
		if lease.IsZero() || !lease.After(recordedAt) {
			return errors.New("a canonical claim requires lease_expires_at after the instant it is recorded")
		}
	default:
		return fmt.Errorf("claim mode %q is invalid; want %q or %q", mode, ClaimModeWorktree, ClaimModeCanonical)
	}
	return nil
}

func claimLeaseExpiry(lease time.Time) *time.Time {
	if lease.IsZero() {
		return nil
	}
	utc := lease.UTC()
	return &utc
}

// workLogLeaseExtension is immutable evidence that a canonical claim's lease
// was extended. The chain is linear (sequence plus predecessor) and each
// event moves the expiry strictly forward; the claim itself is never mutated.
type workLogLeaseExtension struct {
	Version        int       `json:"version"`
	Type           string    `json:"type"`
	ExtensionID    string    `json:"extension_id"`
	ClaimID        string    `json:"claim_id"`
	Sequence       int       `json:"sequence"`
	PredecessorID  string    `json:"predecessor_id,omitempty"`
	At             time.Time `json:"at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

func openWorkLogLeaseExtensions(runDir *os.File, claimID string, create bool) (*os.File, error) {
	if !validClaimID(claimID) {
		return nil, errors.New("invalid lease-extension claim ID")
	}
	root, err := openPrivateChild(runDir, "lease-extensions", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return openPrivateChild(root, claimID, create)
}

// effectiveCanonicalLease is the claim's lease after every extension. It
// proves one linear, strictly advancing chain and fails on anything else, so
// damaged evidence can never lengthen or shorten a lease silently.
func effectiveCanonicalLease(home string, claim workLogClaim) (time.Time, error) {
	return effectiveCanonicalLeaseWith(openWorkLogRun, home, claim)
}

// effectiveCanonicalLeaseWith takes the run opener as a seam. The claim has
// already been corroborated, so it carries a lease.
func effectiveCanonicalLeaseWith(open func(home, effort, run string, create bool) (*os.File, string, error), home string, claim workLogClaim) (time.Time, error) {
	runDir, _, err := open(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = runDir.Close() }()
	expiry, _, err := projectCanonicalLease(runDir, claim)
	return expiry, err
}

func projectCanonicalLease(runDir *os.File, claim workLogClaim) (time.Time, []workLogLeaseExtension, error) {
	expiry := *claim.LeaseExpiresAt
	directory, err := openWorkLogLeaseExtensions(runDir, claim.ClaimID, false)
	if errors.Is(err, os.ErrNotExist) {
		return expiry, nil, nil
	}
	if err != nil {
		return time.Time{}, nil, err
	}
	defer func() { _ = directory.Close() }()
	return projectLeaseChain(directory, claim)
}

// projectLeaseChain reads the extension events in an open history directory.
func projectLeaseChain(directory *os.File, claim workLogClaim) (time.Time, []workLogLeaseExtension, error) {
	expiry := *claim.LeaseExpiresAt
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return time.Time{}, nil, err
	}
	sort.Strings(names)
	extensions := make([]workLogLeaseExtension, 0, len(names))
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if !strings.HasSuffix(name, ".json") || !validSafeSegment(id) {
			return time.Time{}, nil, fmt.Errorf("malformed lease-extension filename %q", name)
		}
		var event workLogLeaseExtension
		if err := readJSONAt(directory, name, &event); err != nil {
			return time.Time{}, nil, err
		}
		if event.Version != 1 || event.Type != leaseExtensionEventType || event.ClaimID != claim.ClaimID ||
			event.ExtensionID != id || event.Sequence < 1 || event.At.IsZero() || event.LeaseExpiresAt.IsZero() {
			return time.Time{}, nil, fmt.Errorf("malformed lease extension %q", name)
		}
		extensions = append(extensions, event)
	}
	sort.Slice(extensions, func(i, j int) bool { return extensions[i].Sequence < extensions[j].Sequence })
	previous := ""
	for index, event := range extensions {
		if event.Sequence != index+1 || event.PredecessorID != previous {
			return time.Time{}, nil, errors.New("lease-extension chain is forked, incomplete, or cyclic")
		}
		if !event.LeaseExpiresAt.After(expiry) {
			return time.Time{}, nil, fmt.Errorf("lease extension %q does not move the expiry forward", event.ExtensionID)
		}
		expiry = event.LeaseExpiresAt
		previous = event.ExtensionID
	}
	return expiry, extensions, nil
}

// appendCanonicalLeaseExtension appends one lease-extension evidence event for
// a canonical claim, moving its effective expiry to newExpiry (which must be
// later than the current one). The immutable claim is untouched. The cap on
// total lease length and the live-versus-lapsed renewal rule belong to the
// entry command, which calls this; this function only keeps the evidence
// chain well-formed.
func appendCanonicalLeaseExtension(home string, claim workLogClaim, at, newExpiry time.Time) (workLogLeaseExtension, error) {
	return appendLeaseExtensionWith(writeJSONImmutableAt, home, claim, at, newExpiry)
}

// appendLeaseExtensionWith takes the immutable writer as a seam.
func appendLeaseExtensionWith(write func(*os.File, string, any, bool) error, home string, claim workLogClaim, at, newExpiry time.Time) (workLogLeaseExtension, error) {
	if claim.Mode != ClaimModeCanonical || claim.LeaseExpiresAt == nil {
		return workLogLeaseExtension{}, errors.New("only a canonical claim has a lease to extend")
	}
	if at.IsZero() {
		return workLogLeaseExtension{}, errors.New("a lease extension needs a timestamp")
	}
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return workLogLeaseExtension{}, fmt.Errorf("open lease-extension run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, claim.ClaimID)
	if err != nil {
		return workLogLeaseExtension{}, fmt.Errorf("lock lease-extension claim: %w", err)
	}
	defer unlock()
	current, extensions, err := projectCanonicalLease(runDir, claim)
	if err != nil {
		return workLogLeaseExtension{}, fmt.Errorf("project lease before extension: %w", err)
	}
	if !newExpiry.After(current) {
		return workLogLeaseExtension{}, fmt.Errorf("lease extension to %s does not move the expiry past %s", newExpiry.UTC().Format(time.RFC3339), current.UTC().Format(time.RFC3339))
	}
	event := workLogLeaseExtension{Version: 1, Type: leaseExtensionEventType, ClaimID: claim.ClaimID,
		Sequence: len(extensions) + 1, At: at.UTC(), LeaseExpiresAt: newExpiry.UTC()}
	event.ExtensionID = fmt.Sprintf("%04d", event.Sequence)
	if len(extensions) != 0 {
		event.PredecessorID = extensions[len(extensions)-1].ExtensionID
	}
	directory, err := openWorkLogLeaseExtensions(runDir, claim.ClaimID, true)
	if err != nil {
		return workLogLeaseExtension{}, fmt.Errorf("open lease-extension history: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := write(directory, event.ExtensionID+".json", event, false); err != nil {
		return workLogLeaseExtension{}, fmt.Errorf("append immutable lease extension: %w", err)
	}
	return event, nil
}
