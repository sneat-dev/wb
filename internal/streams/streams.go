// Package streams owns the identity of a dependency stream: one named
// cross-repository unit of work spanning a library and the consumers that must
// change with it.
//
// A stream introduces no second identity for the same work. Its name is the WB
// worktree task name, its worktrees are created by the existing worktree
// creation path, and its fleet-wide claim is the one `wb worktree create`
// already takes. What this package adds is the durable record that ties those
// per-repository checkouts together — membership, roles, the `stream/<name>`
// branch and its draft pull request, the branch lease, and every live local
// link — so `status`, `sync`, `propagate` and `end` can act on the set rather
// than on one repository at a time.
//
// The record lives under WB's home directory, never inside a member
// repository: `stream-state-is-untracked-and-local` makes that a requirement
// rather than a convenience, so a stream survives an interrupted session and
// `git status` in every member stays clean.
//
// Implements: dependency-streams#req:stream-is-a-named-set-of-worktrees,
// dependency-streams#req:stream-state-is-untracked-and-local.
package streams

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the stream-state format this binary writes and the newest
// it can read. A newer file is refused rather than silently misread: stream
// state carries live links whose reversal detail is the only record of the
// published versions a consumer had before linking.
const SchemaVersion = 1

// BranchPrefix is the namespace every stream branch lives in. It is also what
// the push hook keys its "CI on the stream pull request is the gate" decision
// on, so it is exported rather than spelled out at each call site.
const BranchPrefix = "stream/"

// Role separates the one repository whose published artifacts the others
// resolve from the repositories that resolve them. Propagation direction is
// not symmetric, so the distinction is part of the state rather than something
// each verb re-derives.
type Role string

const (
	// RoleLibrary is the repository whose published artifacts the consumers
	// resolve. Exactly one member holds it.
	RoleLibrary Role = "library"
	// RoleConsumer is a repository that resolves the library's artifacts.
	RoleConsumer Role = "consumer"
)

// Mechanism names how one live local link replaces a published dependency.
type Mechanism string

const (
	// MechanismGoWork is an untracked `go.work` at the consumer worktree root.
	MechanismGoWork Mechanism = "go.work"
	// MechanismPnpmLink is the package manager's own link from a built dist
	// into the consumer's node_modules.
	MechanismPnpmLink Mechanism = "pnpm-link"
)

// Stream is the durable record of one named cross-repository unit of work.
type Stream struct {
	SchemaVersion int       `json:"schema_version"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// EndedAt is set by `wb stream end`. An ended stream is kept, not
	// deleted: its event log and link history are the evidence a later
	// report is built from, and `work-and-event-logs-are-never-pruned`
	// forbids discarding them as a side effect of ending the work.
	EndedAt *time.Time `json:"ended_at,omitempty"`
	Members []Member   `json:"members"`
}

// Member is one repository inside a stream.
type Member struct {
	Repository string `json:"repository"`
	Role       Role   `json:"role"`
	// Worktree is the checkout `wb worktree create` published for this
	// member. It is the path every later verb acts in.
	Worktree string `json:"worktree"`
	// Canonical is the shared clone the worktree was cut from, recorded so
	// `status` can answer without re-deriving it from the projects root.
	Canonical string `json:"canonical"`
	Branch    string `json:"branch"`
	// Base is the branch the draft pull request targets.
	Base string `json:"base"`
	// PullRequest is the draft pull request opened from Branch to Base. Zero
	// means none was opened; PullRequestError then says why.
	PullRequest      int    `json:"pull_request,omitempty"`
	PullRequestURL   string `json:"pull_request_url,omitempty"`
	PullRequestError string `json:"pull_request_error,omitempty"`
	Lease            Lease  `json:"lease"`
	// Links are the live local links this member holds as a consumer. They
	// are written by `wb deps propagate local` and are the source of truth
	// for `--undo`, for `wb stream status`, and for the refusal that keeps a
	// linked worktree from being pushed or landed.
	Links    []Link    `json:"links,omitempty"`
	JoinedAt time.Time `json:"joined_at"`
}

// Lease is the stream's claim on `stream/<name>` in one repository. It records
// the live registered session as well as the machine, because
// `claims-carry-a-session-identity` makes a push from a different live session
// on the same machine a refusal rather than a silent overwrite.
type Lease struct {
	Login   string `json:"login,omitempty"`
	Machine string `json:"machine,omitempty"`
	Session string `json:"session,omitempty"`
	// RecordedHead is the stream head WB last observed on the remote. It is
	// what `wb stream sync` passes to `--force-with-lease`; it is empty
	// until a stream push records one.
	RecordedHead string    `json:"recorded_head,omitempty"`
	HeldSince    time.Time `json:"held_since"`
}

// Holder renders the lease holder the way the claim store spells it.
func (lease Lease) Holder() string {
	if lease.Login == "" && lease.Machine == "" {
		return ""
	}
	return lease.Login + "/" + lease.Machine
}

// Link is one live local link from a consumer worktree to a library worktree.
// It carries everything needed to reverse the link exactly, so `--undo`
// depends on the record rather than on the library worktree still existing.
type Link struct {
	// Library is the library worktree the link points at. It may no longer
	// exist when the link is undone; that is deliberate.
	Library string `json:"library"`
	// LibraryRepository names the owning repository, which survives the
	// worktree being removed.
	LibraryRepository string    `json:"library_repository"`
	Mechanism         Mechanism `json:"mechanism"`
	// Identity is the published identity being replaced: a Go module path or
	// an npm package name.
	Identity string `json:"identity"`
	// PreviousVersion is the version the consumer declared before linking.
	// It is what `--undo` restores.
	PreviousVersion string `json:"previous_version,omitempty"`
	// ContentHash identifies the library working tree, including modified and
	// untracked files, that this link exposed. The library is uncommitted by
	// construction, so it has no SHA.
	ContentHash string `json:"content_hash,omitempty"`
	// Artifacts are the untracked paths the link created, relative to the
	// consumer worktree, so removal never guesses.
	Artifacts []string  `json:"artifacts,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Library returns the member holding RoleLibrary.
func (stream Stream) Library() (Member, bool) {
	for _, member := range stream.Members {
		if member.Role == RoleLibrary {
			return member, true
		}
	}
	return Member{}, false
}

// Consumers returns every member that is not the library, in recorded order.
func (stream Stream) Consumers() []Member {
	consumers := make([]Member, 0, len(stream.Members))
	for _, member := range stream.Members {
		if member.Role != RoleLibrary {
			consumers = append(consumers, member)
		}
	}
	return consumers
}

// Member finds one member by owner/repository.
func (stream Stream) Member(repository string) (Member, bool) {
	for _, member := range stream.Members {
		if strings.EqualFold(member.Repository, repository) {
			return member, true
		}
	}
	return Member{}, false
}

// Open reports whether the stream has not been ended.
func (stream Stream) Open() bool { return stream.EndedAt == nil }

// LiveLinks returns every live link the stream currently records, paired with
// the consumer repository holding it.
func (stream Stream) LiveLinks() []MemberLink {
	var live []MemberLink
	for _, member := range stream.Members {
		for _, link := range member.Links {
			live = append(live, MemberLink{Member: member, Link: link})
		}
	}
	return live
}

// MemberLink pairs a link with the member that holds it.
type MemberLink struct {
	Member Member
	Link   Link
}

// Branch renders the stream branch name for one stream name.
func Branch(name string) string { return BranchPrefix + name }

// IsStreamBranch reports whether a branch name — or a full `refs/heads/…` ref
// — is inside the stream namespace.
func IsStreamBranch(ref string) bool {
	return strings.HasPrefix(strings.TrimPrefix(ref, "refs/heads/"), BranchPrefix)
}

// validName is the stream-name rule. It is deliberately the same shape as the
// worktree task and remote-claim name rule, because a stream name is a task
// name: a stream that could not also be a task name would introduce the second
// identity `stream-is-a-named-set-of-worktrees` forbids.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateName refuses a stream name that could not also be a worktree task
// name, before anything durable is created.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("stream name %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", name)
	}
	return nil
}

var validRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

// ValidateRepository refuses anything that is not an owner/repository slug.
func ValidateRepository(repository string) error {
	if !validRepository.MatchString(repository) {
		return fmt.Errorf("repository %q must be owner/repository", repository)
	}
	return nil
}
