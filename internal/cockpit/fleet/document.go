// Package fleet is Cockpit's fleet read model: one versioned document of this
// machine's repositories, worktrees, branches, pull requests and agents, and
// of every other machine's published snapshot, kept current by a background
// snapshotter and served without a request ever running Git
// (cockpit#req:fleet-read-model, cockpit#req:no-fleet-scan-on-the-request-path).
//
// The types in this file are the security boundary of the read model. They
// hold exactly the metadata field set of cockpit#req:anonymous-local-reads-
// metadata-only and nothing else, and every source value reaches them through
// the mapping in mapping.go field by field: no source struct is copied, so a
// field a source gains later cannot leak. A path, a file name, a diff, a
// commit subject, a task summary, a prompt or a log body has no field here to
// land in.
package fleet

import "time"

// SchemaVersion is the document format this binary writes.
const SchemaVersion = 1

// The two routes an entry can have (cockpit#req:route-and-freshness-are-
// explicit): local for state this daemon observed itself, cached for state
// read from another machine's published snapshot.
const (
	RouteLocal  = "local"
	RouteCached = "cached"
)

// The agent kinds: a registered session or a dispatched run.
const (
	AgentSession = "session"
	AgentRun     = "run"
)

// The two branch scopes.
const (
	BranchLocal  = "local"
	BranchRemote = "remote"
)

// The owner states of a local worktree, from its heartbeat: active when the
// last sign of use is within worktrees.DefaultSessionFreshness, idle after.
const (
	OwnerActive = "active"
	OwnerIdle   = "idle"
)

// The short codes a repository's Error can hold. They name the kind of
// failure and never carry a path or the text a command printed.
const (
	ErrorTimeout    = "timeout"
	ErrorReadFailed = "read_failed"
)

// ErrorRepositoriesUnreadable is the document's own error code when the
// repositories could not be listed.
const ErrorRepositoriesUnreadable = "repositories_unreadable"

// The six states a code index can be in, as code-index-freshness defines them.
const (
	CodeIndexFresh    = "fresh"
	CodeIndexStale    = "stale"
	CodeIndexDiverged = "diverged"
	CodeIndexPending  = "pending"
	CodeIndexFailed   = "failed"
	CodeIndexNever    = "never"
)

// CodeIndex is the freshness of one indexer's index of a checkout
// (cockpit#req:code-index-freshness-is-shown): the indexer's configured name,
// its state, for a stale index the number of commits HEAD is ahead of the
// receipt (Behind), and the time of the receipt the state was read from, when
// there is one. Nothing else of a receipt, and no path, reaches the document.
//
// Statistics is what the configured code-index provider reported for the
// checkout when the receipt was written (cockpit#req:code-index-summary): it is
// present only on the indexer the provider follows, and absent when the
// provider has not been asked. Statistics appear only for a checkout the
// configured indexer has a receipt for: the provider's command opens the index
// read-write and may run Git, which the snapshotter's read-only rule forbids
// for a checkout WB's hook never indexed (it may hold an index a hostile
// repository committed).
type CodeIndex struct {
	Indexer    string          `json:"indexer"`
	State      string          `json:"state"`
	Behind     int             `json:"behind,omitempty"`
	ReceiptAt  time.Time       `json:"receipt_at,omitzero"`
	Statistics *CodeStatistics `json:"statistics,omitempty"`
	// receiptKey identifies the receipt the state was read from (its commit and
	// status); it stays inside the daemon and keys the provider's answer.
	receiptKey string
}

// CodeStatistics is a code index's statistics, counts only
// (cockpit#req:code-index-summary): whether the checkout has an index, and when
// it does the number of files, symbols and edges and the symbols by kind. These
// are statistics of the index, not counts of Cockpit entities. Error is a short
// code when the provider could not answer, and then the counts are zero; it is
// never the text a command printed or a path. Kinds is a list, never null.
type CodeStatistics struct {
	Indexed bool        `json:"indexed"`
	Files   int         `json:"files"`
	Symbols int         `json:"symbols"`
	Edges   int         `json:"edges"`
	Kinds   []KindCount `json:"kinds"`
	Error   string      `json:"error,omitempty"`
}

// KindCount is the number of symbols of one kind. The kind is a short
// lower-case word that matched kindPattern.
type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// PullRequestUnknown is the state of a locally recorded pull request: the
// record names it but not whether it is still open.
const PullRequestUnknown = "unknown"

// Entry is what every collection's entry carries: a stable identifier unique
// within its collection, the machine it belongs to, its route and when it was
// observed. Machine is the machine's name, which two logins can share;
// MachineID is the id of the machine entry it belongs to, which is unique, and
// is what a client filters by. For a cached entry ObservedAt is the publish time of the snapshot
// it came from.
type Entry struct {
	ID         string    `json:"id"`
	Machine    string    `json:"machine"`
	MachineID  string    `json:"machine_id"`
	Route      string    `json:"route"`
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// Document is the fleet read model. It is published incrementally: while the
// first pass runs it holds the repositories scanned so far and WarmingUp stays
// true, and RepositoriesScanned of RepositoriesTotal says how far the pass is.
// Before the first repository completes it is empty and well formed.
// Diagnostics counts the pull requests whose repository could not be told.
// Error is a short code when the repositories could not be listed; the
// agents and other machines are still read. AgentsTruncated says the agents
// were capped.
type Document struct {
	SchemaVersion       int       `json:"schema_version"`
	SnapshotAt          time.Time `json:"snapshot_at,omitzero"`
	WarmingUp           bool      `json:"warming_up"`
	RepositoriesTotal   int       `json:"repositories_total"`
	RepositoriesScanned int       `json:"repositories_scanned"`
	Diagnostics         int       `json:"diagnostics"`
	Error               string    `json:"error,omitempty"`
	// CodeIndexProvider is the name of the configured code-index provider, or
	// absent when none is configured, which is a normal state.
	CodeIndexProvider string        `json:"code_index_provider,omitempty"`
	Machines          []Machine     `json:"machines"`
	Repositories      []Repository  `json:"repositories"`
	Worktrees         []Worktree    `json:"worktrees"`
	Branches          []Branch      `json:"branches"`
	PullRequests      []PullRequest `json:"pull_requests"`
	Agents            []Agent       `json:"agents"`
	AgentsTruncated   bool          `json:"agents_truncated,omitempty"`
}

// Machine is this machine or another machine the remote provider has a
// snapshot for.
type Machine struct {
	Entry
	WBVersion       string `json:"wb_version,omitempty"`
	RepositoryCount int    `json:"repository_count"`
	WorktreeCount   int    `json:"worktree_count"`
}

// Repository is one repository with its counts. A count that is nil is not
// known for this entry: a cached repository has no branch or agent counts,
// because another machine's snapshot does not carry them. Error is a short
// code when the repository could not be read this pass; what it last held is
// kept.
type Repository struct {
	Entry
	Host                 string `json:"host,omitempty"`
	Name                 string `json:"name"`
	DefaultBranch        string `json:"default_branch,omitempty"`
	WorktreeCount        int    `json:"worktree_count"`
	LocalBranchCount     *int   `json:"local_branch_count,omitempty"`
	RemoteBranchCount    *int   `json:"remote_branch_count,omitempty"`
	OpenPullRequestCount *int   `json:"open_pull_request_count,omitempty"`
	ActiveAgentCount     *int   `json:"active_agent_count,omitempty"`
	Error                string `json:"error,omitempty"`
	// CodeIndex is one state per indexer configured for the repository. It is
	// absent for a cached repository, whose published snapshot does not carry
	// it, and for a local one with no indexer or whose state could not be told.
	CodeIndex []CodeIndex `json:"code_index,omitempty"`
}

// Worktree is one WB task worktree. Repository is the id of its repository.
type Worktree struct {
	Entry
	Repository     string    `json:"repository"`
	Task           string    `json:"task"`
	Stream         string    `json:"stream,omitempty"`
	Branch         string    `json:"branch"`
	Lifecycle      string    `json:"lifecycle,omitempty"`
	OwnerState     string    `json:"owner_state,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitzero"`
	// CodeIndex is as on Repository.
	CodeIndex []CodeIndex `json:"code_index,omitempty"`
}

// Branch is one local or remote branch. Worktree is the id of the worktree
// that has it checked out, and Task that worktree's task, when there is one.
// Upstream, Ahead, Behind and UpstreamGone describe a local branch's tracking
// state.
type Branch struct {
	Entry
	Repository     string    `json:"repository"`
	Name           string    `json:"name"`
	Scope          string    `json:"scope"`
	Task           string    `json:"task,omitempty"`
	Worktree       string    `json:"worktree,omitempty"`
	Upstream       string    `json:"upstream,omitempty"`
	Ahead          int       `json:"ahead,omitempty"`
	Behind         int       `json:"behind,omitempty"`
	UpstreamGone   bool      `json:"upstream_gone,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitzero"`
}

// PullRequest is one open pull request recorded locally, tied to its
// repository and, where one exists, its worktree. State is empty for a local
// record, which names the pull request but not its state; Repository is empty
// when the record's repository slug matches no single local repository.
type PullRequest struct {
	Entry
	Repository string `json:"repository,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Number     int    `json:"number"`
	State      string `json:"state,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Agent is a registered session or a dispatched run: identifiers, runtime,
// model and state, and the repository a run works in.
type Agent struct {
	Entry
	Kind       string `json:"kind"`
	SessionID  string `json:"session_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	Runtime    string `json:"runtime,omitempty"`
	Model      string `json:"model,omitempty"`
	State      string `json:"state"`
	Repository string `json:"repository,omitempty"`
}

// emptyDocument is the well-formed document served before the first snapshot:
// every collection is an empty list, never null.
func emptyDocument() Document {
	return Document{
		SchemaVersion: SchemaVersion, WarmingUp: true,
		Machines: []Machine{}, Repositories: []Repository{}, Worktrees: []Worktree{},
		Branches: []Branch{}, PullRequests: []PullRequest{}, Agents: []Agent{},
	}
}
