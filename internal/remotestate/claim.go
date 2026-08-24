package remotestate

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// ClaimSchemaVersion is the claim format this binary writes and the newest
// it can read.
const ClaimSchemaVersion = 1

// Claim says one task is being worked on by one login/machine. The claim
// file's existence in the store IS the claim; releasing deletes it, so git
// history is the audit trail and no state field exists.
type Claim struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	Task          string    `yaml:"task" json:"task"`
	Login         string    `yaml:"login" json:"login"`
	Machine       string    `yaml:"machine" json:"machine"`
	ClaimedAt     time.Time `yaml:"claimed_at" json:"claimed_at"`
	Note          string    `yaml:"note,omitempty" json:"note,omitempty"`
}

// Holder identifies the claimant: "<login>/<machine>". Mutual exclusion is
// per machine — the same login on another machine is another holder.
func (c Claim) Holder() string { return c.Login + "/" + c.Machine }

// ClaimMode selects how an existing claim by someone else is treated.
type ClaimMode int

const (
	// ClaimNormal refuses to touch anyone else's claim.
	ClaimNormal ClaimMode = iota
	// ClaimTakeOverStale replaces another holder's claim; callers must have
	// established staleness first — the provider does not judge it.
	ClaimTakeOverStale
	// ClaimForce replaces anything, including an unreadable claim file.
	ClaimForce
)

// ClaimOutcomeKind is what a Claim call did.
type ClaimOutcomeKind string

const (
	ClaimAcquired  ClaimOutcomeKind = "acquired"
	ClaimRefreshed ClaimOutcomeKind = "refreshed"
	// ClaimHeld means no write happened; Current is the other holder's claim.
	ClaimHeld     ClaimOutcomeKind = "held"
	ClaimTookOver ClaimOutcomeKind = "took_over"
)

// ClaimOutcome reports a Claim call so commands own all messaging.
type ClaimOutcome struct {
	Kind     ClaimOutcomeKind `json:"kind"`
	Current  Claim            `json:"current"`
	Previous *Claim           `json:"previous,omitempty"` // set on took_over
	Location string           `json:"location,omitempty"` // commit SHA / URL
}

// ReleaseOutcomeKind is what a Release call did.
type ReleaseOutcomeKind string

const (
	Released ReleaseOutcomeKind = "released"
	// ReleaseNoop: no claim existed; releasing is idempotent.
	ReleaseNoop ReleaseOutcomeKind = "noop"
	// ReleaseHeldByOther: refused (force was false); Current names the holder.
	ReleaseHeldByOther ReleaseOutcomeKind = "held_by_other"
)

// ReleaseOutcome reports a Release call.
type ReleaseOutcome struct {
	Kind     ReleaseOutcomeKind `json:"kind"`
	Current  *Claim             `json:"current,omitempty"`
	Location string             `json:"location,omitempty"`
}

// ClaimEntry is one claim as read from the store; Error is set when the
// file could not be decoded (Claim then carries only Task from the path).
type ClaimEntry struct {
	Claim Claim  `json:"claim"`
	Error string `json:"error,omitempty"`
}

// ValidTaskName enforces the machine-name rule on task names, which also
// keeps claims/<task>.yaml a safe path.
func ValidTaskName(task string) error {
	if !machineName.MatchString(task) {
		return fmt.Errorf("task %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", task)
	}
	return nil
}

// EncodeClaim renders a claim as YAML.
func EncodeClaim(c Claim) ([]byte, error) { return yaml.Marshal(c) }

// DecodeClaim parses a claim, refusing formats newer than this binary knows.
func DecodeClaim(data []byte) (Claim, error) {
	var c Claim
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Claim{}, fmt.Errorf("parse claim: %w", err)
	}
	if c.SchemaVersion > ClaimSchemaVersion {
		return Claim{}, fmt.Errorf("claim schema_version %d is newer than supported %d; update wb", c.SchemaVersion, ClaimSchemaVersion)
	}
	return c, nil
}
