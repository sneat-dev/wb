package remotestate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// MaxAgents is the most agents one published snapshot carries.
const MaxAgents = 200

// maxAgentText bounds every string of an AgentState.
const maxAgentText = 200

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
// agents only when withAgents (at most MaxAgents, every string cleaned and
// length-capped), the sample only when withMetrics and it is set. With both
// flags false it is the snapshot unchanged.
func (s Snapshot) WithExtras(extras Extras, withAgents, withMetrics bool) Snapshot {
	s.Agents, s.Metrics = nil, nil
	if withAgents {
		for _, agent := range extras.Agents {
			if len(s.Agents) == MaxAgents {
				break
			}
			s.Agents = append(s.Agents, agent.cleaned())
		}
	}
	if withMetrics && extras.Metrics != nil {
		sample := *extras.Metrics
		s.Metrics = &sample
	}
	return s
}

func (a AgentState) cleaned() AgentState {
	a.Kind, a.SessionID, a.RunID = cleanText(a.Kind), cleanText(a.SessionID), cleanText(a.RunID)
	a.Runtime, a.Model, a.State, a.Activity = cleanText(a.Runtime), cleanText(a.Model), cleanText(a.State), cleanText(a.Activity)
	a.Task, a.Repository = cleanText(a.Task), cleanText(a.Repository)
	return a
}

// cleanText removes control and line-break characters and cuts the text to
// maxAgentText characters, so a published string is one bounded plain line.
func cleanText(text string) string {
	kept := make([]rune, 0, len(text))
	for _, character := range text {
		if len(kept) == maxAgentText {
			break
		}
		if !unicode.IsControl(character) && !unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			kept = append(kept, character)
		}
	}
	return string(kept)
}

// WithoutOptional returns the snapshot without the optional fields an older
// hub refuses: the hardware facts, the agents and the sample.
func (s Snapshot) WithoutOptional() Snapshot {
	s.OS, s.Arch, s.CPUCount, s.BootTime = "", "", 0, time.Time{}
	s.Agents, s.Metrics = nil, nil
	return s
}

// HasOptional reports whether the snapshot carries any optional field.
func (s Snapshot) HasOptional() bool {
	return s.OS != "" || s.Arch != "" || s.CPUCount != 0 || !s.BootTime.IsZero() || len(s.Agents) > 0 || s.Metrics != nil
}

// Digest identifies what a snapshot says apart from when it was published and
// the changing machine sample: it is the same before and after a publish when
// nothing a reader would act on has changed. The periodic publisher skips a
// publish whose digest is the last published one, so a git store gains no
// commit for an idle machine.
func (s Snapshot) Digest() string {
	s.PublishedAt, s.LastSeenAt, s.Metrics = time.Time{}, time.Time{}, nil
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
