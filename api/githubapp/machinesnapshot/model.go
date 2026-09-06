// Package machinesnapshot defines the public, privacy-safe HTTP and durable
// storage contract for hosted WB machine state.
package machinesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SchemaVersion is the only hosted snapshot schema this build accepts.
	SchemaVersion = 1
	// SnapshotPath is the authenticated endpoint used by the CLI and hub host.
	SnapshotPath = "/v0/workbench/machines/snapshot"
	// Collection is the authoritative durable collection for hosted records.
	Collection = "workbench_machine_snapshots"

	MaxWorktrees      = 5000
	MaxIdentityLength = 128
	MaxRepositoryLen  = 256
	MaxTaskLength     = 256
	MaxBranchLength   = 512
	MaxStatusLength   = 64
	MaxOwnerLength    = 256
	MaxAttentionLen   = 1024
	MaxPRURLLength    = 2048

	AttentionOwnerInactive      = "owner session is no longer active"
	AttentionSupersessionReview = "supersession evidence requires review"
	AttentionAbsorptionReview   = "absorption evidence requires review"
	AttentionReviewRequired     = "worktree requires attention"
)

var safeIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var safeRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var (
	ErrInvalidSnapshot  = errors.New("invalid hosted machine snapshot")
	ErrStaleSnapshot    = errors.New("hosted machine snapshot is older than the current snapshot")
	ErrSnapshotConflict = errors.New("hosted machine snapshot conflicts at the same published time")
)

// Snapshot is the complete allowlist of machine state that may cross the
// hosted boundary. It intentionally has no path, projects root, commit SHA,
// repository diagnostics, prompts, credentials, or command output.
type Snapshot struct {
	SchemaVersion int        `json:"schema_version" firestore:"schema_version"`
	Login         string     `json:"login" firestore:"login"`
	Machine       string     `json:"machine" firestore:"machine"`
	PublishedAt   time.Time  `json:"published_at" firestore:"published_at"`
	LastSeenAt    time.Time  `json:"last_seen_at,omitempty" firestore:"last_seen_at,omitempty"`
	Worktrees     []Worktree `json:"worktrees" firestore:"worktrees"`
}

// Worktree is the hosted dashboard projection of one WB worktree.
type Worktree struct {
	Task            string       `json:"task" firestore:"task"`
	Stream          string       `json:"stream,omitempty" firestore:"stream,omitempty"`
	Repository      string       `json:"repository" firestore:"repository"`
	Branch          string       `json:"branch" firestore:"branch"`
	Lifecycle       string       `json:"lifecycle,omitempty" firestore:"lifecycle,omitempty"`
	OwnerState      string       `json:"owner_status,omitempty" firestore:"owner_status,omitempty"`
	Owner           string       `json:"owner,omitempty" firestore:"owner,omitempty"`
	LastActivityAt  time.Time    `json:"last_activity_at,omitempty" firestore:"last_activity_at,omitempty"`
	NeedsAttention  bool         `json:"needs_attention,omitempty" firestore:"needs_attention,omitempty"`
	AttentionReason string       `json:"attention_reason,omitempty" firestore:"attention_reason,omitempty"`
	PullRequest     *PullRequest `json:"pull_request,omitempty" firestore:"pull_request,omitempty"`
}

// PullRequest is the hosted link for one worktree review.
type PullRequest struct {
	Number int    `json:"number" firestore:"number"`
	URL    string `json:"url" firestore:"url"`
	State  string `json:"state,omitempty" firestore:"state,omitempty"`
}

// StoredSnapshot is the durable, server-stamped record. Digest identifies the
// exact validated payload without retaining the request bytes.
type StoredSnapshot struct {
	Snapshot   Snapshot  `json:"snapshot" firestore:"snapshot"`
	ReceivedAt time.Time `json:"received_at" firestore:"received_at"`
	Digest     string    `json:"digest" firestore:"digest"`
}

// StoreResult is returned by an atomic latest-snapshot replacement.
type StoreResult struct {
	Current StoredSnapshot
	Updated bool
}

// SnapshotStore is the durable persistence port used by the host adapter.
// StoreLatest MUST atomically key records by login/machine, keep the candidate
// with the newest PublishedAt, reject a different payload at the same
// PublishedAt, and return the existing record without a write when Digest is
// already current. ListLatest returns at most one record for every key.
type SnapshotStore interface {
	StoreLatest(ctx context.Context, snapshot StoredSnapshot) (StoreResult, error)
	ListLatest(ctx context.Context) ([]StoredSnapshot, error)
}

// ResolveLatest is the deterministic comparison durable adapters apply inside
// their transaction. It makes retries idempotent and prevents delayed
// deliveries from replacing newer machine state.
func ResolveLatest(current *StoredSnapshot, candidate StoredSnapshot) (StoreResult, error) {
	if current == nil {
		return StoreResult{Current: candidate, Updated: true}, nil
	}
	if current.Snapshot.Key() != candidate.Snapshot.Key() {
		return StoreResult{}, errors.New("cannot compare hosted snapshots for different machines")
	}
	if current.Digest == candidate.Digest {
		return StoreResult{Current: *current, Updated: false}, nil
	}
	if candidate.Snapshot.PublishedAt.Before(current.Snapshot.PublishedAt) {
		return StoreResult{Current: *current}, ErrStaleSnapshot
	}
	if candidate.Snapshot.PublishedAt.Equal(current.Snapshot.PublishedAt) {
		return StoreResult{Current: *current}, ErrSnapshotConflict
	}
	return StoreResult{Current: candidate, Updated: true}, nil
}

// Receipt is the server response to one publish attempt.
type Receipt struct {
	Login       string    `json:"login"`
	Machine     string    `json:"machine"`
	PublishedAt time.Time `json:"published_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Updated     bool      `json:"updated"`
}

// PublishedSnapshot is the privacy-safe stored view returned to a publisher.
// The internal digest is deliberately omitted from the wire response.
type PublishedSnapshot struct {
	Snapshot   Snapshot  `json:"snapshot"`
	ReceivedAt time.Time `json:"received_at"`
}

// ListResponse is the authenticated, privacy-safe response consumed by WB.
type ListResponse struct {
	Snapshots []PublishedSnapshot `json:"snapshots"`
}

// Validate rejects malformed and overlarge records before they reach storage.
func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be %d", ErrInvalidSnapshot, SchemaVersion)
	}
	if err := validateIdentity("login", snapshot.Login); err != nil {
		return err
	}
	if err := validateIdentity("machine", snapshot.Machine); err != nil {
		return err
	}
	if snapshot.PublishedAt.IsZero() {
		return fmt.Errorf("%w: published_at is required", ErrInvalidSnapshot)
	}
	if len(snapshot.Worktrees) > MaxWorktrees {
		return fmt.Errorf("%w: worktrees exceeds %d entries", ErrInvalidSnapshot, MaxWorktrees)
	}
	for index, worktree := range snapshot.Worktrees {
		if err := worktree.validate(); err != nil {
			return fmt.Errorf("%w: worktrees[%d]: %v", ErrInvalidSnapshot, index, err)
		}
	}
	return nil
}

func (worktree Worktree) validate() error {
	for name, value := range map[string]string{
		"task": worktree.Task, "repository": worktree.Repository, "branch": worktree.Branch,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if !printable(value) {
			return fmt.Errorf("%s contains invalid control characters", name)
		}
		if looksLikeAbsolutePath(value) {
			return fmt.Errorf("%s must not be an absolute path", name)
		}
	}
	if len(worktree.Task) > MaxTaskLength || len(worktree.Stream) > MaxTaskLength {
		return errors.New("task or stream is too long")
	}
	owner, name, found := strings.Cut(worktree.Repository, "/")
	if len(worktree.Repository) > MaxRepositoryLen || !found || strings.Contains(name, "/") ||
		!safeRepositoryPart.MatchString(owner) || !safeRepositoryPart.MatchString(name) {
		return errors.New("repository must be a bounded owner/name")
	}
	if len(worktree.Branch) > MaxBranchLength || len(worktree.Lifecycle) > MaxStatusLength || len(worktree.OwnerState) > MaxStatusLength {
		return errors.New("branch or status is too long")
	}
	if len(worktree.Owner) > MaxOwnerLength || len(worktree.AttentionReason) > MaxAttentionLen {
		return errors.New("owner or attention reason is too long")
	}
	if !printable(worktree.Stream) || !printable(worktree.Lifecycle) || !printable(worktree.OwnerState) ||
		!printable(worktree.Owner) || !printable(worktree.AttentionReason) {
		return errors.New("worktree text contains invalid control characters")
	}
	if looksLikeAbsolutePath(worktree.Stream) || looksLikeAbsolutePath(worktree.Owner) {
		return errors.New("worktree text must not contain an absolute path")
	}
	if !validAttentionReason(worktree.AttentionReason) {
		return errors.New("attention reason is not a supported hosted value")
	}
	if worktree.PullRequest != nil {
		if worktree.PullRequest.Number < 1 || len(worktree.PullRequest.URL) > MaxPRURLLength {
			return errors.New("pull request number or URL is invalid")
		}
		parsed, err := url.ParseRequestURI(worktree.PullRequest.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("pull request URL must be HTTPS")
		}
		if len(worktree.PullRequest.State) > MaxStatusLength || !printable(worktree.PullRequest.State) {
			return errors.New("pull request state is invalid")
		}
	}
	return nil
}

// NormalizeAttentionReason maps local free-form diagnostics onto the hosted
// allowlist so paths and command output never cross the wire.
func NormalizeAttentionReason(needsAttention bool, value string) string {
	if !needsAttention {
		return ""
	}
	if validAttentionReason(value) && value != "" {
		return value
	}
	return AttentionReviewRequired
}

func validAttentionReason(value string) bool {
	switch value {
	case "", AttentionOwnerInactive, AttentionSupersessionReview, AttentionAbsorptionReview, AttentionReviewRequired:
		return true
	default:
		return false
	}
}

func looksLikeAbsolutePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func validateIdentity(name, value string) error {
	if len(value) == 0 || len(value) > MaxIdentityLength || !safeIdentity.MatchString(value) {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidSnapshot, name)
	}
	return nil
}

// ValidateIdentity applies the hosted login and machine identifier contract.
func ValidateIdentity(value string) error { return validateIdentity("identity", value) }

// SnapshotKey derives the stable flat document ID for one login/machine pair.
// The delimiter prevents ambiguous concatenation and the hash keeps identity
// text from altering a storage hierarchy.
func SnapshotKey(login, machine string) (string, error) {
	if err := validateIdentity("login", login); err != nil {
		return "", err
	}
	if err := validateIdentity("machine", machine); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(login + "\x00" + machine))
	return "machine_" + hex.EncodeToString(sum[:]), nil
}

func printable(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\t' {
			return false
		}
	}
	return true
}

// SortStored gives stable responses without exposing a persistence ordering.
func SortPublished(snapshots []PublishedSnapshot) {
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Snapshot.Key() < snapshots[j].Snapshot.Key() })
}

// Key identifies a hosted machine record.
func (snapshot Snapshot) Key() string { return snapshot.Login + "/" + snapshot.Machine }
