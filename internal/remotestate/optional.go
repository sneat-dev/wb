package remotestate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/agentfields"
)

// maxCPUCount bounds a published CPU count.
const maxCPUCount = 65536

var shortName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// CleanHardware blanks the hardware facts that fail their rules: an operating
// system or architecture name that is not a short plain word, a CPU count outside
// 1 to 65536, and a boot time in the future or before 1970. The publisher applies
// it so that what it sends is what the hub accepts.
func (s Snapshot) CleanHardware() Snapshot {
	if !shortName.MatchString(s.OS) {
		s.OS = ""
	}
	if !shortName.MatchString(s.Arch) {
		s.Arch = ""
	}
	if s.CPUCount < 1 || s.CPUCount > maxCPUCount {
		s.CPUCount = 0
	}
	if s.BootTime.Before(time.Unix(0, 0)) || s.BootTime.After(time.Now().Add(maxSkew)) {
		s.BootTime = time.Time{}
	}
	return s
}

// MaxAgents is the most agents one published snapshot carries.
const MaxAgents = 200

// AgentState is one agent of the publishing machine, as the optional `agents`
// list of a snapshot carries it. It holds the closed set of fields of
// cockpit-views#req:remote-snapshot-agents-and-metrics and nothing else: no
// path, command line, prompt, environment value or free-text error. Kind is
// "session" or "run"; SessionID and RunID are the identifiers the fleet
// document carries for it.
type AgentState struct {
	Kind       string    `yaml:"kind" json:"kind"`
	SessionID  string    `yaml:"session_id,omitempty" json:"session_id,omitempty"`
	RunID      string    `yaml:"run_id,omitempty" json:"run_id,omitempty"`
	Runtime    string    `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Model      string    `yaml:"model,omitempty" json:"model,omitempty"`
	State      string    `yaml:"state" json:"state"`
	Activity   string    `yaml:"activity,omitempty" json:"activity,omitempty"`
	Task       string    `yaml:"task,omitempty" json:"task,omitempty"`
	Repository string    `yaml:"repository,omitempty" json:"repository,omitempty"`
	StartedAt  time.Time `yaml:"started_at,omitempty" json:"started_at,omitzero"`
}

// MetricsSample is the latest machine sample a snapshot may carry, with the
// six measurements and the time of cockpit-views#req:machine-metrics-route.
// A measurement is absent, never guessed or zero-filled.
type MetricsSample struct {
	CPUPercent       *float64  `yaml:"cpu_percent,omitempty" json:"cpu_percent,omitempty"`
	Load1            *float64  `yaml:"load1,omitempty" json:"load1,omitempty"`
	MemoryUsedBytes  *uint64   `yaml:"memory_used_bytes,omitempty" json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes *uint64   `yaml:"memory_total_bytes,omitempty" json:"memory_total_bytes,omitempty"`
	DiskFreeBytes    *uint64   `yaml:"disk_free_bytes,omitempty" json:"disk_free_bytes,omitempty"`
	DiskTotalBytes   *uint64   `yaml:"disk_total_bytes,omitempty" json:"disk_total_bytes,omitempty"`
	SampledAt        time.Time `yaml:"sampled_at" json:"sampled_at"`
}

// Extras is what the daemon adds to a snapshot beyond what the scan builds:
// this machine's agents and its latest metrics sample.
type Extras struct {
	Agents  []AgentState
	Metrics *MetricsSample
}

// PublishSource is what the daemon's fleet snapshotter tells the periodic
// publisher about this machine without a scan: the extras the opt-in flags may
// add, and a token that is the same while nothing the snapshotter observes of
// this machine's repositories and worktrees has changed.
type PublishSource interface {
	PublishExtras() Extras
	// ChangeToken is "" when the source has seen nothing yet; the publisher
	// then never skips a scan on its account.
	ChangeToken() string
}

// WithExtras returns the snapshot carrying extras under the opt-in flags:
// agents only when withAgents, the sample only when withMetrics and it is set.
// With both flags false it is the snapshot unchanged.
//
// Agents are validated, not repaired (package agentfields): an agent whose kind,
// state or identifier fails is dropped, any other failing field is blanked, so a
// path, an environment value or prose in a field never reaches the payload. At
// most MaxAgents valid agents are kept, and AgentsTruncated is set when more
// valid ones were left out (an invalid agent beyond the cap is not a cut).
func (s Snapshot) WithExtras(extras Extras, withAgents, withMetrics bool) Snapshot {
	s.Agents, s.Metrics, s.AgentsTruncated = nil, nil, false
	if withAgents {
		for _, agent := range extras.Agents {
			cleaned, ok := agent.cleaned()
			if !ok {
				continue
			}
			if len(s.Agents) == MaxAgents {
				s.AgentsTruncated = true
				break
			}
			s.Agents = append(s.Agents, cleaned)
		}
	}
	if withMetrics && extras.Metrics != nil {
		if sample, ok := extras.Metrics.valid(); ok {
			s.Metrics = &sample
		}
	}
	return s
}

// cleaned applies the shared rules; false means the agent is dropped.
func (a AgentState) cleaned() (AgentState, bool) {
	fields, ok := agentfields.Clean(agentfields.Agent{
		Kind: a.Kind, SessionID: a.SessionID, RunID: a.RunID, Runtime: a.Runtime, Model: a.Model, State: a.State,
		Activity: a.Activity, Task: a.Task, Repository: a.Repository,
	}, agentfields.IsTaskName)
	if !ok {
		return AgentState{}, false
	}
	a.Kind, a.SessionID, a.RunID, a.Runtime, a.Model, a.State = fields.Kind, fields.SessionID, fields.RunID, fields.Runtime, fields.Model, fields.State
	a.Activity, a.Task, a.Repository = fields.Activity, fields.Task, fields.Repository
	if !a.StartedAt.IsZero() && a.StartedAt.After(time.Now().Add(maxSkew)) {
		a.StartedAt = time.Time{}
	}
	return a, true
}

// maxSkew is how far ahead of the clock a published time may be.
const maxSkew = 5 * time.Second

// valid returns the sample with each out-of-range measurement dropped, and false
// when its time is missing or in the future.
func (m MetricsSample) valid() (MetricsSample, bool) {
	if m.SampledAt.IsZero() || m.SampledAt.After(time.Now().Add(maxSkew)) {
		return MetricsSample{}, false
	}
	if m.CPUPercent != nil && !(*m.CPUPercent >= 0 && *m.CPUPercent <= 100) {
		m.CPUPercent = nil
	}
	if m.Load1 != nil && !(*m.Load1 >= 0 && !math.IsInf(*m.Load1, 0)) {
		m.Load1 = nil
	}
	if m.MemoryUsedBytes == nil || m.MemoryTotalBytes == nil || *m.MemoryUsedBytes > *m.MemoryTotalBytes {
		m.MemoryUsedBytes, m.MemoryTotalBytes = nil, nil
	}
	if m.DiskFreeBytes == nil || m.DiskTotalBytes == nil || *m.DiskFreeBytes > *m.DiskTotalBytes {
		m.DiskFreeBytes, m.DiskTotalBytes = nil, nil
	}
	return m, true
}

// WithoutOptional returns the snapshot without the optional fields an older
// hub refuses: the hardware facts, the agents and the sample.
func (s Snapshot) WithoutOptional() Snapshot {
	s.OS, s.Arch, s.CPUCount, s.BootTime = "", "", 0, time.Time{}
	s.Agents, s.Metrics, s.AgentsTruncated = nil, nil, false
	return s
}

// HasOptional reports whether the snapshot carries any optional field.
func (s Snapshot) HasOptional() bool {
	return s.OS != "" || s.Arch != "" || s.CPUCount != 0 || !s.BootTime.IsZero() || len(s.Agents) > 0 || s.Metrics != nil
}

// ActivityBucket is the granularity of the published times a digest and the
// daemon's change token take: a heartbeat that moves a worktree's last activity
// by less than this does not make a snapshot "changed", so an active machine is
// not published (and git does not gain a commit) for every heartbeat.
const ActivityBucket = 15 * time.Minute

// Digest identifies what a snapshot says apart from when it was published, the
// changing machine sample, the agents' fast-flapping `activity` and the worktrees'
// last-activity times below ActivityBucket: it is the same before and after a
// publish when nothing a reader would act on has changed. The periodic publisher
// skips a publish whose digest is the last published one, so a git store gains no
// commit for an idle machine.
func (s Snapshot) Digest() string { return digestOf(s, true) }

// CoreDigest is Digest without the agents and their truncation marker: what the
// publisher compares to tell a change of agents alone from a change of the rest.
func (s Snapshot) CoreDigest() string { return digestOf(s, false) }

func digestOf(s Snapshot, withAgents bool) string {
	s.PublishedAt, s.LastSeenAt, s.Metrics = time.Time{}, time.Time{}, nil
	if withAgents {
		agents := make([]AgentState, len(s.Agents))
		for index, agent := range s.Agents {
			agent.Activity = ""
			agents[index] = agent
		}
		s.Agents = agents
	} else {
		s.Agents, s.AgentsTruncated = nil, false
	}
	worktrees := make([]WorktreeState, len(s.Worktrees))
	for index, worktree := range s.Worktrees {
		worktree.LastActivityAt = worktree.LastActivityAt.UTC().Truncate(ActivityBucket)
		worktrees[index] = worktree
	}
	s.Worktrees = worktrees
	data, _ := yaml.Marshal(s) // a Snapshot always marshals
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StatusCoder is implemented by an error that carries the HTTP status a hub
// refused a request with.
type StatusCoder interface {
	error
	HTTPStatus() int
}

// ErrOptionalFieldsDropped is the diagnostic PublishWithFallback returns
// alongside a successful publish that had to leave the optional fields out.
var ErrOptionalFieldsDropped = errors.New("the hub refused the optional snapshot fields (HTTP 400); published without them")

// OptionalRefusalMemory is implemented by a provider that remembers, for the
// life of the provider, that its store refused the optional fields, so the
// full payload is not sent again and refused on every publish.
type OptionalRefusalMemory interface {
	// OptionalFieldsRefused reports whether a refusal is remembered at now.
	OptionalFieldsRefused(now time.Time) bool
	// RefuseOptionalFields remembers a refusal until the given time.
	RefuseOptionalFields(until time.Time)
}

// OptionalRefusalMemoryFor is how long a refusal is remembered.
const OptionalRefusalMemoryFor = 24 * time.Hour

// PublishWithFallback publishes snapshot through provider. When the provider
// refuses it with status 400 and the snapshot carries optional fields, as an
// older hub does for fields it does not know, it publishes once more without
// them, and a provider that remembers (OptionalRefusalMemory) is told, so for
// the next 24 hours the optional fields are left out at once. The returned
// diagnostic is ErrOptionalFieldsDropped when the optional fields were left
// out and the publish succeeded, and nil otherwise; a retry is never repeated.
func PublishWithFallback(ctx context.Context, provider Provider, snapshot Snapshot, now time.Time) (result PublishResult, diagnostic, err error) {
	memory, remembers := provider.(OptionalRefusalMemory)
	if remembers && snapshot.HasOptional() && memory.OptionalFieldsRefused(now) {
		if result, err = provider.Publish(ctx, snapshot.WithoutOptional()); err != nil {
			return PublishResult{}, nil, err
		}
		return result, ErrOptionalFieldsDropped, nil
	}
	result, err = provider.Publish(ctx, snapshot)
	var status StatusCoder
	if err == nil || !snapshot.HasOptional() || !errors.As(err, &status) || status.HTTPStatus() != 400 {
		return result, nil, err
	}
	result, err = provider.Publish(ctx, snapshot.WithoutOptional())
	if err != nil {
		return PublishResult{}, nil, err
	}
	if remembers {
		memory.RefuseOptionalFields(now.Add(OptionalRefusalMemoryFor))
	}
	return result, ErrOptionalFieldsDropped, nil
}
