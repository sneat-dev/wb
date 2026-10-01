package remotestate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// previousSnapshot is the snapshot type of the previous wb version, which does
// not know the optional fields: the decoder it uses is the same yaml.Unmarshal
// without strict field checking that Decode wraps.
type previousSnapshot struct {
	SchemaVersion int       `yaml:"schema_version"`
	Login         string    `yaml:"login"`
	Machine       string    `yaml:"machine"`
	PublishedAt   time.Time `yaml:"published_at"`
	WBVersion     string    `yaml:"wb_version"`
}

func f64(value float64) *float64 { return &value }
func u64(value uint64) *uint64   { return &value }

func optionalFixture() (Snapshot, Extras) {
	published := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion, Login: "alex", Machine: "mac", PublishedAt: published, WBVersion: "v1",
		OS: "darwin", Arch: "arm64", CPUCount: 10, BootTime: published.Add(-48 * time.Hour),
	}
	extras := Extras{
		Agents:  []AgentState{{Kind: "run", RunID: "agt-1", Runtime: "claude", Model: "opus", State: "running", Activity: "working", Task: "fix", Repository: "sneat-dev/wb", StartedAt: published}},
		Metrics: &MetricsSample{CPUPercent: f64(40), Load1: f64(1.5), MemoryUsedBytes: u64(1), MemoryTotalBytes: u64(2), DiskFreeBytes: u64(3), DiskTotalBytes: u64(4), SampledAt: published},
	}
	return snapshot, extras
}

func TestWithExtrasHonoursEachOptInFlag(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	for _, test := range []struct {
		agents, metrics bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		got := base.WithExtras(extras, test.agents, test.metrics)
		if (len(got.Agents) == 1) != test.agents || (got.Metrics != nil) != test.metrics {
			t.Errorf("agents=%v metrics=%v: carried %d agents and metrics %v", test.agents, test.metrics, len(got.Agents), got.Metrics)
		}
		if got.SchemaVersion != SchemaVersion || got.OS != "darwin" || got.CPUCount != 10 {
			t.Errorf("schema or hardware changed: %+v", got)
		}
	}
	if got := base.WithExtras(Extras{}, true, true); got.Metrics != nil || got.Agents != nil {
		t.Errorf("empty extras still produced fields: %+v", got)
	}
}

func TestWithExtrasCapsAndCleansAgents(t *testing.T) {
	t.Parallel()
	base, _ := optionalFixture()
	var many []AgentState
	for range 500 {
		many = append(many, AgentState{Kind: "session", State: "live", Task: strings.Repeat("x", 5000) + "\n\x1b[31m", Runtime: "a b\x7fc", Model: "m\u0085"})
	}
	got := base.WithExtras(Extras{Agents: many}, true, false)
	if len(got.Agents) != MaxAgents {
		t.Fatalf("published %d agents, want %d", len(got.Agents), MaxAgents)
	}
	first := got.Agents[0]
	if len([]rune(first.Task)) != maxAgentText || strings.ContainsAny(first.Task, "\n\x1b") && len([]rune(first.Task)) > maxAgentText {
		t.Errorf("task not capped: %d", len([]rune(first.Task)))
	}
	if first.Runtime != "abc" || first.Model != "m" {
		t.Errorf("control characters kept: %q %q", first.Runtime, first.Model)
	}
}

func TestOptionalFieldsAreIgnoredByThePreviousDecoder(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	full := base.WithExtras(extras, true, true)
	data, err := Encode(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"agents:", "metrics:", "os: darwin", "arch: arm64", "cpu_count: 10", "boot_time:"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("encoded snapshot lacks %q", key)
		}
	}
	var previous previousSnapshot
	if err := yaml.Unmarshal(data, &previous); err != nil {
		t.Fatalf("the previous decoder refused the snapshot: %v", err)
	}
	if previous.SchemaVersion != 1 || previous.Login != "alex" || previous.Machine != "mac" {
		t.Errorf("previous decoder read %+v", previous)
	}
	decoded, err := Decode(data)
	if err != nil || len(decoded.Agents) != 1 || decoded.Metrics == nil || decoded.Metrics.Load1 == nil || decoded.OS != "darwin" {
		t.Fatalf("round trip = %+v, %v", decoded, err)
	}
	// With the flags off the document has neither list.
	plain, err := Encode(base.WithExtras(extras, false, false))
	if err != nil || strings.Contains(string(plain), "agents:") || strings.Contains(string(plain), "metrics:") {
		t.Errorf("flags off still encoded optional lists: %s %v", plain, err)
	}
}

func TestPublishedOptionalPartCarriesNoPathCommandOrEnvironment(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	data, err := Encode(base.WithExtras(extras, true, true))
	if err != nil {
		t.Fatal(err)
	}
	// The additions have a closed set of keys, none of which can hold a path, a
	// command line or an environment value.
	var tree map[string]any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	agentKeys := map[string]bool{}
	for key := range tree["agents"].([]any)[0].(map[string]any) {
		agentKeys[key] = true
	}
	for key := range agentKeys {
		if !strings.Contains(" kind session_id run_id runtime model state activity task repository started_at ", " "+key+" ") {
			t.Errorf("agent carries unexpected key %q", key)
		}
	}
	metricKeys := " cpu_percent load1 memory_used_bytes memory_total_bytes disk_free_bytes disk_total_bytes sampled_at "
	for key := range tree["metrics"].(map[string]any) {
		if !strings.Contains(metricKeys, " "+key+" ") {
			t.Errorf("metrics carries unexpected key %q", key)
		}
	}
	for _, forbidden := range []string{"path:", "dir:", "command", "environment", "env:", "cwd", "prompt", "pid"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Errorf("snapshot carries %q: %s", forbidden, data)
		}
	}
}

func TestDigestIgnoresTimeAndSampleButNotContent(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	full := base.WithExtras(extras, true, true)
	later := full
	later.PublishedAt = later.PublishedAt.Add(time.Hour)
	later.LastSeenAt = later.PublishedAt
	moved := *later.Metrics
	moved.SampledAt, moved.CPUPercent = later.PublishedAt, f64(99)
	later.Metrics = &moved
	if full.Digest() == "" || full.Digest() != later.Digest() {
		t.Error("the digest moved with the publish time or the sample")
	}
	changed := full
	changed.Agents = []AgentState{{Kind: "run", RunID: "agt-1", State: "running", Activity: "idle"}}
	if changed.Digest() == full.Digest() {
		t.Error("the digest did not move with the agents")
	}
	changed = full
	changed.Worktrees = []WorktreeState{{Task: "t"}}
	if changed.Digest() == full.Digest() {
		t.Error("the digest did not move with the worktrees")
	}
}

func TestWithoutOptionalAndHasOptional(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	full := base.WithExtras(extras, true, true)
	if !full.HasOptional() {
		t.Fatal("a full snapshot reports no optional field")
	}
	stripped := full.WithoutOptional()
	if stripped.HasOptional() || stripped.Login != "alex" || stripped.PublishedAt != full.PublishedAt {
		t.Errorf("WithoutOptional = %+v", stripped)
	}
	for _, only := range []Snapshot{{OS: "x"}, {Arch: "x"}, {CPUCount: 1}, {BootTime: time.Unix(1, 0)}, {Agents: []AgentState{{}}}, {Metrics: &MetricsSample{}}} {
		if !only.HasOptional() {
			t.Errorf("%+v reports no optional field", only)
		}
	}
	if (Snapshot{}).HasOptional() {
		t.Error("an empty snapshot reports an optional field")
	}
}

type statusErr int

func (e statusErr) Error() string   { return "refused" }
func (e statusErr) HTTPStatus() int { return int(e) }

// scriptedProvider returns each queued error in turn and records the snapshots.
type scriptedProvider struct {
	dqCovProvider
	errs []error
	seen []Snapshot
}

func (p *scriptedProvider) Publish(_ context.Context, snapshot Snapshot) (PublishResult, error) {
	p.seen = append(p.seen, snapshot)
	if len(p.errs) == 0 {
		return PublishResult{Location: "ok"}, nil
	}
	err := p.errs[0]
	p.errs = p.errs[1:]
	return PublishResult{Location: "x"}, err
}

func TestPublishWithFallbackRetriesOnceWithoutOptionalFieldsOn400(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	full := base.WithExtras(extras, true, true)

	provider := &scriptedProvider{errs: []error{statusErr(400)}}
	result, diagnostic, err := PublishWithFallback(context.Background(), provider, full)
	if err != nil || !errors.Is(diagnostic, ErrOptionalFieldsDropped) || result.Location != "ok" {
		t.Fatalf("result=%+v diagnostic=%v err=%v", result, diagnostic, err)
	}
	if len(provider.seen) != 2 || !provider.seen[0].HasOptional() || provider.seen[1].HasOptional() {
		t.Fatalf("attempts = %d, second carried optional fields: %v", len(provider.seen), provider.seen[1].HasOptional())
	}

	// The retry is made once: a second refusal is the error.
	provider = &scriptedProvider{errs: []error{statusErr(400), statusErr(400)}}
	if _, diagnostic, err = PublishWithFallback(context.Background(), provider, full); err == nil || diagnostic != nil || len(provider.seen) != 2 {
		t.Fatalf("second refusal: diagnostic=%v err=%v attempts=%d", diagnostic, err, len(provider.seen))
	}
}

func TestPublishWithFallbackDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()
	base, extras := optionalFixture()
	full := base.WithExtras(extras, true, true)
	for name, failure := range map[string]error{"500": statusErr(500), "plain": errors.New("boom"), "401": statusErr(401)} {
		provider := &scriptedProvider{errs: []error{failure}}
		if _, diagnostic, err := PublishWithFallback(context.Background(), provider, full); err == nil || diagnostic != nil || len(provider.seen) != 1 {
			t.Errorf("%s: diagnostic=%v err=%v attempts=%d", name, diagnostic, err, len(provider.seen))
		}
	}
	// A snapshot with nothing optional is refused for another reason: no retry.
	provider := &scriptedProvider{errs: []error{statusErr(400)}}
	if _, _, err := PublishWithFallback(context.Background(), provider, Snapshot{Login: "a"}); err == nil || len(provider.seen) != 1 {
		t.Errorf("a plain snapshot was retried: %v %d", err, len(provider.seen))
	}
	// Success on the first attempt has no diagnostic.
	provider = &scriptedProvider{}
	if _, diagnostic, err := PublishWithFallback(context.Background(), provider, full); err != nil || diagnostic != nil {
		t.Errorf("success: %v %v", diagnostic, err)
	}
}
