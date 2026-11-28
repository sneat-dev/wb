package deskpublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
)

// ErrNothingToPublish is returned for an envelope that carries no fleet: a
// metrics-only export, a daemon warming up, or one that cannot list its
// repositories (desk-connect#req:publish-on-change).
var ErrNothingToPublish = errors.New("export holds no publishable fleet")

// Snapshot is a reduced, canonically encoded envelope ready to publish.
type Snapshot struct {
	Detail    desk.Level
	TaskKeyID string
	Envelope  []byte
	Hash      string
}

// Request is the publish request for this snapshot observed at observedAt.
func (snapshot Snapshot) Request(observedAt time.Time) desk.SnapshotRequest {
	return desk.SnapshotRequest{SchemaVersion: desk.SchemaVersion, Detail: snapshot.Detail,
		TaskKeyID: snapshot.TaskKeyID, ObservedAt: observedAt, Hash: snapshot.Hash,
		Envelope: json.RawMessage(snapshot.Envelope)}
}

// The reduced types embed the fleet types and shadow the fields a level
// withholds with empty, omitted fields of the same JSON name: encoding/json
// takes the shallower field, so the embedded value is never encoded.

type reducedWorktree struct {
	fleet.Worktree
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Task    string `json:"task,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Branch  string `json:"branch,omitempty"`
	TaskKey string `json:"task_key,omitempty"`
}

type reducedPullRequest struct {
	fleet.PullRequest
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	FailedCheck string `json:"failed_check,omitempty"`
}

type reducedAgent struct {
	fleet.Agent
	Task      string   `json:"task,omitempty"`
	Worktrees []string `json:"worktrees,omitempty"`
	TaskKey   string   `json:"task_key,omitempty"`
}

type reducedSlowTask struct {
	TaskKey         string    `json:"task_key,omitempty"`
	DurationSeconds int       `json:"duration_seconds"`
	LandedAt        time.Time `json:"landed_at"`
}

type reducedThroughput struct {
	*fleet.Throughput
	Slowest []reducedSlowTask `json:"slowest"`
}

type reducedDocument struct {
	*fleet.Document
	Worktrees    []reducedWorktree    `json:"worktrees"`
	PullRequests []reducedPullRequest `json:"pull_requests"`
	Agents       []reducedAgent       `json:"agents"`
	Throughput   *reducedThroughput   `json:"throughput,omitempty"`
}

type reducedEnvelope struct {
	fleet.Envelope
	ExportedAt *time.Time       `json:"exported_at,omitempty"`
	Fleet      *reducedDocument `json:"fleet,omitempty"`
}

// fullEnvelope is the export with the local throughput block put back:
// the export leaves it out, and it is in the metadata set.
type fullEnvelope struct {
	fleet.Envelope
	ExportedAt *time.Time      `json:"exported_at,omitempty"`
	Fleet      *fleet.Document `json:"fleet,omitempty"`
}

// Reduce applies level to an export envelope and this machine's throughput,
// which the export itself leaves out
// (desk-connect#req:detail-level-applied-on-the-machine). At `repositories`
// it needs key; at `full` it ignores it.
func Reduce(envelope fleet.Envelope, throughput *fleet.Throughput, level desk.Level, key TaskKey) (Snapshot, error) {
	if envelope.Fleet == nil || envelope.Fleet.WarmingUp || fleet.Unlistable(*envelope.Fleet) {
		return Snapshot{}, ErrNothingToPublish
	}
	document := stable(*envelope.Fleet)
	document.Throughput = throughput
	// The resource samples change on every pass and are not part of what a
	// snapshot shows; the export time is the request's observed_at.
	envelope.Metrics = nil
	var value any
	snapshot := Snapshot{Detail: level}
	switch level {
	case desk.LevelFull:
		value = fullEnvelope{Envelope: envelope, Fleet: &document}
	case desk.LevelRepositories:
		if len(key.secret) != TaskKeyBytes {
			return Snapshot{}, ErrInvalidTaskKey
		}
		value = reducedEnvelope{Envelope: envelope, Fleet: reduceDocument(&document, key)}
		snapshot.TaskKeyID = key.ID()
	default:
		return Snapshot{}, fmt.Errorf("unknown desk detail level %q", level)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode desk snapshot: %w", err)
	}
	sum := sha256.Sum256(encoded)
	snapshot.Envelope = encoded
	snapshot.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return snapshot, nil
}

// stable is document without what changes on every pass although nothing in
// the fleet did: the snapshot time, each entry's observation time and each
// pull request's check time. Without them the hash changes only when the
// fleet does, so an idle machine publishes nothing
// (desk-connect#req:publish-on-change). The slices are copied, never changed.
func stable(document fleet.Document) fleet.Document {
	document.SnapshotAt = time.Time{}
	document.Machines = slices.Clone(document.Machines)
	for index := range document.Machines {
		document.Machines[index].ObservedAt = time.Time{}
	}
	document.Repositories = slices.Clone(document.Repositories)
	for index := range document.Repositories {
		document.Repositories[index].ObservedAt = time.Time{}
	}
	document.Worktrees = slices.Clone(document.Worktrees)
	for index := range document.Worktrees {
		document.Worktrees[index].ObservedAt = time.Time{}
	}
	document.PullRequests = slices.Clone(document.PullRequests)
	for index := range document.PullRequests {
		document.PullRequests[index].ObservedAt, document.PullRequests[index].CheckedAt = time.Time{}, time.Time{}
	}
	document.Agents = slices.Clone(document.Agents)
	for index := range document.Agents {
		document.Agents[index].ObservedAt = time.Time{}
	}
	return document
}

func reduceDocument(document *fleet.Document, key TaskKey) *reducedDocument {
	defaults := map[string]string{}
	for _, repository := range document.Repositories {
		defaults[repository.ID] = repository.DefaultBranch
	}
	reduced := &reducedDocument{Document: document,
		Worktrees:    make([]reducedWorktree, 0, len(document.Worktrees)),
		PullRequests: make([]reducedPullRequest, 0, len(document.PullRequests)),
		Agents:       make([]reducedAgent, 0, len(document.Agents)),
	}
	for _, worktree := range document.Worktrees {
		item := reducedWorktree{Worktree: worktree, ID: key.entry("wt", worktree.ID), TaskKey: key.Of(worktree.Task)}
		if branch := defaults[worktree.Repository]; branch != "" && worktree.Branch == branch {
			item.Branch = branch
		}
		reduced.Worktrees = append(reduced.Worktrees, item)
	}
	for _, pull := range document.PullRequests {
		reduced.PullRequests = append(reduced.PullRequests, reducedPullRequest{PullRequest: pull})
	}
	for _, agent := range document.Agents {
		reduced.Agents = append(reduced.Agents, reducedAgent{Agent: agent, TaskKey: key.Of(agent.Task)})
	}
	if document.Throughput != nil {
		slowest := make([]reducedSlowTask, 0, len(document.Throughput.Slowest))
		for _, task := range document.Throughput.Slowest {
			slowest = append(slowest, reducedSlowTask{TaskKey: key.Of(task.Task),
				DurationSeconds: task.DurationSeconds, LandedAt: task.LandedAt})
		}
		reduced.Throughput = &reducedThroughput{Throughput: document.Throughput, Slowest: slowest}
	}
	return reduced
}
