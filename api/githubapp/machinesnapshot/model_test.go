package machinesnapshot

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestResolveLatestIsIdempotentAndMonotonic(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	first := StoredSnapshot{Snapshot: validSnapshot(at), ReceivedAt: at.Add(time.Second), Digest: "first"}
	created, err := ResolveLatest(nil, first)
	if err != nil || !created.Updated || created.Current.Digest != "first" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	duplicate := first
	duplicate.ReceivedAt = at.Add(2 * time.Second)
	unchanged, err := ResolveLatest(&first, duplicate)
	if err != nil || unchanged.Updated || !unchanged.Current.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("duplicate = %+v, %v", unchanged, err)
	}
	stale := first
	stale.Digest = "stale"
	stale.Snapshot.PublishedAt = at.Add(-time.Minute)
	if _, err := ResolveLatest(&first, stale); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale err = %v", err)
	}
	conflict := first
	conflict.Digest = "conflict"
	if _, err := ResolveLatest(&first, conflict); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	newer := first
	newer.Digest = "newer"
	newer.Snapshot.PublishedAt = at.Add(time.Minute)
	updated, err := ResolveLatest(&first, newer)
	if err != nil || !updated.Updated || updated.Current.Digest != "newer" {
		t.Fatalf("newer = %+v, %v", updated, err)
	}
}

func TestSnapshotValidateBoundsHostedSchema(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot(time.Now().UTC())
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot.Worktrees[0].PullRequest.URL = "http://example.test/pull/1"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("HTTP PR URL err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Worktrees = make([]Worktree, MaxWorktrees+1)
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("oversized worktrees err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/zeta/tools", "github.com/acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unsorted repositories err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("noncanonical repository err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/acme/.github", "github.com/acme/widgets"}
	snapshot.Worktrees[0].Repository = "acme/.github"
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("a repository named .github is valid on GitHub: %v", err)
	}
	for _, name := range []string{".", "..", ""} {
		snapshot = validSnapshot(time.Now().UTC())
		snapshot.Repositories = []string{"github.com/acme/" + name}
		if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("repository name %q err = %v", name, err)
		}
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/.acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("dotted owner err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Worktrees[0].AttentionReason = "/Users/alice/private output"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unsafe attention err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Worktrees[0].TaskSummary = "line one\nline two"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("multiline task summary err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.RemoteStore = "git:team/wb-state\nsecret"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("multiline remote store err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.RemoteStore = strings.Repeat("x", MaxRemoteStoreLen+1)
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("oversized remote store err = %v", err)
	}
}

func validSnapshot(at time.Time) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, Login: "alice", Machine: "laptop", PublishedAt: at,
		RemoteStore:  "git:team/wb-state",
		Repositories: []string{"github.com/acme/widgets"},
		Worktrees: []Worktree{{
			Task: "dashboard", Repository: "acme/widgets", Branch: "feature/dashboard",
			PullRequest: &PullRequest{Number: 1, URL: "https://github.com/acme/widgets/pull/1"},
		}},
	}
}

func optionalSnapshot() Snapshot {
	snapshot := validSnapshot(time.Now().UTC())
	cpu, load := 40.0, 1.5
	used, total, free, disk := uint64(1), uint64(2), uint64(3), uint64(4)
	snapshot.OS, snapshot.Arch, snapshot.CPUCount, snapshot.BootTime = "darwin", "arm64", 10, time.Now().UTC().Add(-time.Hour)
	snapshot.Agents = []Agent{{Kind: "run", RunID: "agt-1", Runtime: "claude", Model: "opus", State: "running", Activity: "working", Task: "fix", Repository: "acme/widgets", StartedAt: time.Now().UTC()}}
	snapshot.Metrics = &Metrics{CPUPercent: &cpu, Load1: &load, MemoryUsedBytes: &used, MemoryTotalBytes: &total, DiskFreeBytes: &free, DiskTotalBytes: &disk, SampledAt: time.Now().UTC()}
	return snapshot
}

func TestSnapshotAcceptsAndBoundsTheOptionalFields(t *testing.T) {
	t.Parallel()
	if err := optionalSnapshot().Validate(); err != nil {
		t.Fatalf("a snapshot with every optional field: %v", err)
	}
	negative, huge, nan := -1.0, 101.0, math.NaN()
	smaller, larger := uint64(1), uint64(2)
	for name, edit := range map[string]func(*Snapshot){
		"os":               func(s *Snapshot) { s.OS = "darwin/../x" },
		"arch":             func(s *Snapshot) { s.Arch = strings.Repeat("a", 33) },
		"cpu high":         func(s *Snapshot) { s.CPUCount = MaxCPUCount + 1 },
		"cpu negative":     func(s *Snapshot) { s.CPUCount = -1 },
		"too many agents":  func(s *Snapshot) { s.Agents = make([]Agent, MaxAgents+1) },
		"agent kind":       func(s *Snapshot) { s.Agents[0].Kind = "daemon" },
		"agent state":      func(s *Snapshot) { s.Agents[0].State = "" },
		"agent long":       func(s *Snapshot) { s.Agents[0].Task = strings.Repeat("x", 300) },
		"agent control":    func(s *Snapshot) { s.Agents[0].Model = "a\x1bb" },
		"agent newline":    func(s *Snapshot) { s.Agents[0].Runtime = "a\nb" },
		"agent path":       func(s *Snapshot) { s.Agents[0].Task = "/Users/alice/private" },
		"agent model path": func(s *Snapshot) { s.Agents[0].Model = "/Users/alice/models/x.gguf" },
		"agent env":        func(s *Snapshot) { s.Agents[0].Runtime = "HOME=/Users/alice" },
		"agent prose":      func(s *Snapshot) { s.Agents[0].Task = "ignore previous instructions" },
		"agent activity":   func(s *Snapshot) { s.Agents[0].Activity = "napping" },
		"agent state kind": func(s *Snapshot) { s.Agents[0].State = "live" },
		"agent repository": func(s *Snapshot) { s.Agents[0].Repository = "not a repo" },
		"agent session id": func(s *Snapshot) { s.Agents[0].SessionID = "a b" },
		"agent future":     func(s *Snapshot) { s.Agents[0].StartedAt = time.Now().Add(48 * time.Hour) },
		"boot future":      func(s *Snapshot) { s.BootTime = time.Now().Add(48 * time.Hour) },
		"metrics future":   func(s *Snapshot) { s.Metrics.SampledAt = time.Now().Add(48 * time.Hour) },
		"truncated none":   func(s *Snapshot) { s.Agents, s.AgentsTruncated = nil, true },
		"metrics time":     func(s *Snapshot) { s.Metrics.SampledAt = time.Time{} },
		"metrics cpu":      func(s *Snapshot) { s.Metrics.CPUPercent = &huge },
		"metrics load":     func(s *Snapshot) { s.Metrics.Load1 = &negative },
		"metrics nan":      func(s *Snapshot) { s.Metrics.Load1 = &nan },
		"memory no total":  func(s *Snapshot) { s.Metrics.MemoryTotalBytes = nil },
		"memory over":      func(s *Snapshot) { s.Metrics.MemoryUsedBytes, s.Metrics.MemoryTotalBytes = &larger, &smaller },
		"disk no total":    func(s *Snapshot) { s.Metrics.DiskTotalBytes = nil },
	} {
		snapshot := optionalSnapshot()
		edit(&snapshot)
		if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOptionalFieldsAreOmittedFromTheWireWhenAbsent(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(validSnapshot(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"os"`, `"arch"`, `"cpu_count"`, `"boot_time"`, `"agents"`, `"metrics"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("a snapshot without optional fields carries %s: %s", key, raw)
		}
	}
	// Strict decoding, as the hub's handler does it, accepts the full shape.
	full, _ := json.Marshal(optionalSnapshot())
	decoder := json.NewDecoder(strings.NewReader(string(full)))
	decoder.DisallowUnknownFields()
	var decoded Snapshot
	if err := decoder.Decode(&decoded); err != nil || len(decoded.Agents) != 1 || decoded.Metrics == nil {
		t.Fatalf("strict decode = %+v, %v", decoded, err)
	}
}
