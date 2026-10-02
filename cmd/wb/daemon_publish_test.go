package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// capturingProvider is the fake remote store of the periodic publisher: it
// counts publishes and keeps what it was given, and touches no network or Git.
type capturingProvider struct {
	remotestate.Provider
	mu   sync.Mutex
	seen []remotestate.Snapshot
	errs []error
}

func (p *capturingProvider) Publish(_ context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, snapshot)
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: "sha"}, nil
}

// fixedSource is a fake publish source.
type fixedSource struct {
	extras remotestate.Extras
	token  string
}

func (f fixedSource) PublishExtras() remotestate.Extras { return f.extras }
func (f fixedSource) ChangeToken() string               { return f.token }

type refusedWith400 struct{}

func (refusedWith400) Error() string   { return "hub returned HTTP 400" }
func (refusedWith400) HTTPStatus() int { return 400 }

func publishDeps(provider remotestate.Provider, login func() (string, error), opened *int) remoteDeps {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	return remoteDeps{
		login: login, now: func() time.Time { return now },
		open: func(remotestate.Config, string) (remotestate.Provider, error) {
			*opened++
			return provider, nil
		},
	}
}

func publishConfig(publish remotestate.PublishConfig) remotestate.Config {
	return remotestate.Config{Provider: "git", Repo: "acme/wb-state", Machine: "mac", Publish: publish}
}

// TestCockpitFleetOptionsPublishOnlyWhenTheOwnerOptedIn proves the opt-in
// defaults of cockpit-views#req:periodic-remote-publish: a machine that only
// ever published by hand, or has no store, or sets a bad interval, has no
// publisher, and one that sets an interval has one.
func TestCockpitFleetOptionsPublishOnlyWhenTheOwnerOptedIn(t *testing.T) {
	t.Parallel()
	host := func() (string, error) { return "h", nil }
	build := func(content string) bool {
		var logs bytes.Buffer
		options := cockpitFleetOptions(t.TempDir(), t.TempDir(), cockpitConfigFile(t, content)(), wbconfig.DefaultCockpitConfig(), &logs, host)
		return options.Publisher != nil
	}
	const store = "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop-1\n"
	for name, test := range map[string]struct {
		config string
		want   bool
	}{
		"hand-published git store":         {store, false},
		"only the redaction":               {store + "  publish:\n    unpushed: counts\n", false},
		"flags without an interval":        {store + "  publish:\n    agents: true\n    metrics: true\n", false},
		"interval set":                     {store + "  publish:\n    interval: 10m\n", true},
		"interval below the minimum":       {store + "  publish:\n    interval: 1m\n", true},
		"hub with an interval":             {"remote:\n  provider: hub\n  url: https://hub.example\n  token_file: /tmp/token\n  machine: laptop-2\n  publish:\n    interval: 15m\n", true},
		"negative interval is a bad value": {store + "  publish:\n    interval: -5m\n", false},
		"no remote section":                {"cockpit: {}\n", false},
	} {
		if got := build(test.config); got != test.want {
			t.Errorf("%s: publisher = %v, want %v", name, got, test.want)
		}
	}
	if none := cockpitFleetOptions(t.TempDir(), t.TempDir(), t.TempDir()+"/absent.yaml", wbconfig.DefaultCockpitConfig(), io.Discard, host); none.Publisher != nil {
		t.Error("a machine with no wb.yaml has a publisher")
	}
	// The snapshotter is always told where the login will be known from, and it is
	// not known before anything resolved it.
	if wired := cockpitFleetOptions(t.TempDir(), t.TempDir(), t.TempDir()+"/absent.yaml", wbconfig.DefaultCockpitConfig(), io.Discard, host); wired.LoginSource == nil || wired.LoginSource() != "" || wired.Login != "" {
		t.Error("the fleet options name no login source, or one that knows a login already")
	}
}

func TestPeriodicPublisherUsesTheCLIsIdentityScanAndProvider(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened, logins := 0, 0
	deps := publishDeps(provider, func() (string, error) { logins++; return "alice", nil }, &opened)
	cfg := publishConfig(remotestate.PublishConfig{Interval: time.Minute, Agents: true, Metrics: true, Unpushed: remotestate.RedactUnpushed})
	var logs bytes.Buffer
	learned := &learnedLogin{}
	if learned.known() != "" {
		t.Fatal("a login is known before anything resolved it")
	}
	publisher := newPeriodicPublisher(deps, cfg, t.TempDir(), func(format string, args ...any) { logs.WriteString(format) }, learned.learn)
	extras := fixedSource{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}, Metrics: &remotestate.MetricsSample{SampledAt: deps.now()}}}
	publisher.Publish(t.Context(), extras)
	if len(provider.seen) != 1 || opened != 1 || logins != 1 {
		t.Fatalf("published %d, opened %d, logins %d (logs %q)", len(provider.seen), opened, logins, logs.String())
	}
	// The login the publisher resolved is what the fleet snapshotter tells this
	// machine's own publications by.
	if learned.known() != "alice" {
		t.Fatalf("the daemon learned the login %q, want alice", learned.known())
	}
	got := provider.seen[0]
	if got.Login != "alice" || got.Machine != "mac" || got.RemoteStore != "git:acme/wb-state" || got.WBVersion == "" || !got.PublishedAt.Equal(deps.now()) {
		t.Fatalf("identity = %+v", got)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH || got.CPUCount != runtime.NumCPU() {
		t.Errorf("hardware = %q %q %d", got.OS, got.Arch, got.CPUCount)
	}
	if len(got.Agents) != 1 || got.Metrics == nil || got.SchemaVersion != remotestate.SchemaVersion {
		t.Errorf("agents %d, metrics %v, schema %d", len(got.Agents), got.Metrics, got.SchemaVersion)
	}
	// The login is looked up once, not on every attempt.
	if status := publisher.Status(); status.Published != 1 || status.Diagnostic != periodic.DiagnosticNone {
		t.Fatalf("status = %+v", status)
	}
}

func TestPeriodicPublisherWithoutTheFlagsPublishesNeitherAgentsNorMetrics(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	cfg := publishConfig(remotestate.PublishConfig{Interval: 10 * time.Minute})
	publisher := newPeriodicPublisher(publishDeps(provider, func() (string, error) { return "alice", nil }, &opened), cfg, t.TempDir(), nil, nil)
	publisher.Publish(t.Context(), fixedSource{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", State: "running"}}, Metrics: &remotestate.MetricsSample{}}})
	if len(provider.seen) != 1 || provider.seen[0].Agents != nil || provider.seen[0].Metrics != nil {
		t.Fatalf("seen = %+v", provider.seen)
	}
	// Hardware is part of the machine entry and is published regardless.
	if provider.seen[0].OS == "" {
		t.Error("the machine entry lacks its hardware")
	}
}

func TestPeriodicPublisherFailsTypedWhenTheLoginOrTheStoreIsUnavailable(t *testing.T) {
	t.Parallel()
	opened := 0
	provider := &capturingProvider{}
	noLogin := newPeriodicPublisher(publishDeps(provider, func() (string, error) { return "", errors.New("gh: not logged in") }, &opened), publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	noLogin.Publish(t.Context(), nil)
	if status := noLogin.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed || opened != 0 || len(provider.seen) != 0 {
		t.Fatalf("status = %+v, opened %d", status, opened)
	}
	blank := newPeriodicPublisher(publishDeps(provider, func() (string, error) { return "", nil }, &opened), publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	blank.Publish(t.Context(), nil)
	if status := blank.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed {
		t.Fatalf("a blank login = %+v", status)
	}
	closed, cancel := context.WithCancel(t.Context())
	cancel()
	cancelled := newPeriodicPublisher(publishDeps(provider, func() (string, error) { return "alice", nil }, &opened), publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	cancelled.Publish(closed, nil)
	if status := cancelled.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed || len(provider.seen) != 0 {
		t.Fatalf("a cancelled scan = %+v", status)
	}
}

func TestCollectSnapshotStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	closed, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectSnapshot(closed, t.TempDir(), "", 1, remotestate.Snapshot{}, remotestate.RedactNone, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// With a repository to read, a scan that was cancelled reads none of them
	// (no Git runs: the check comes first) and still reports the cancellation.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme", "widgets", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := collectSnapshot(closed, root, "", 1, remotestate.Snapshot{}, remotestate.RedactNone, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan with a repository: err = %v, want context.Canceled", err)
	}
}

func TestRemotePublishByHandRetriesWithoutOptionalFieldsAndSaysSo(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{errs: []error{refusedWith400{}}}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.configPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	var out, progress bytes.Buffer
	if err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, &progress, &invocation{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.seen) != 2 || !provider.seen[0].HasOptional() || provider.seen[1].HasOptional() || !strings.Contains(progress.String(), "published without them") {
		t.Fatalf("attempts %d, progress %q", len(provider.seen), progress.String())
	}
}

func TestPublishIdentityCarriesTheHardwareFacts(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	identity := publishIdentity(publishConfig(remotestate.PublishConfig{}), "alice", at)
	if identity.Login != "alice" || identity.Machine != "mac" || !identity.PublishedAt.Equal(at) || identity.OS == "" || identity.CPUCount < 1 || identity.Agents != nil || identity.Metrics != nil {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestRemotePublishByHandSaysOnceThatHardwareIsNowIncluded(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.configPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	run := func() string {
		var out, progress bytes.Buffer
		if err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, &progress, &invocation{}); err != nil {
			t.Fatal(err)
		}
		return progress.String()
	}
	first := run()
	if strings.Count(first, "\n") != 1 || !strings.Contains(first, "os, arch, cpu_count and boot_time") || !strings.Contains(first, "never sent by hand") {
		t.Fatalf("first publish note = %q", first)
	}
	if second := run(); strings.Contains(second, "boot_time") {
		t.Fatalf("the note was repeated: %q", second)
	}
	// A dry run prints the snapshot, says nothing, and does not use up the note.
	fresh := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	fresh.configPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	var out, progress bytes.Buffer
	if err := runRemotePublishWithProgress(fresh, t.TempDir(), "", 1, true, true, &out, &progress, &invocation{}); err != nil || strings.Contains(progress.String(), "boot_time") {
		t.Fatalf("dry run: %v %q", err, progress.String())
	}
	// Without a config path or an unwritable marker nothing breaks.
	noteHardware("", &progress)
	recordHardwareNoted(noteHardware(filepath.Join(t.TempDir(), "absent", "wb.yaml"), &progress))
	recordHardwareNoted("")
}

func TestRemotePublishHelpStatesWhatIsPublished(t *testing.T) {
	t.Parallel()
	long := newRemotePublishCmd(&invocation{}).Long
	for _, want := range []string{"os, arch, cpu_count and boot_time", "never published by hand", "remote.publish.interval"} {
		if !strings.Contains(long, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestAFailedPublishDoesNotUseUpTheHardwareNote(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{errs: []error{errors.New("store down")}}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.configPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	run := func() (string, error) {
		var out, progress bytes.Buffer
		err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, &progress, &invocation{})
		return progress.String(), err
	}
	first, err := run()
	if err == nil || !strings.Contains(first, "boot_time") {
		t.Fatalf("the failing publish: %v, note %q", err, first)
	}
	second, err := run() // succeeds: the note is shown again, and only now recorded
	if err != nil || !strings.Contains(second, "boot_time") {
		t.Fatalf("the retry after a failure did not repeat the note: %v %q", err, second)
	}
	third, err := run()
	if err != nil || strings.Contains(third, "boot_time") {
		t.Fatalf("the note was shown after a success: %v %q", err, third)
	}
}

// TestRemotePublishSaysThatHardwareIsIncludedWithoutAProgressWriter: what the
// first publish must say does not depend on whether it shows progress. With no
// progress writer the note, and a publish that had to leave the optional fields
// out, are said on stderr, once; with neither a progress writer nor a stderr
// nothing is said and the note is not used up.
func TestRemotePublishSaysThatHardwareIsIncludedWithoutAProgressWriter(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.configPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	// Neither writer: the publish is made and says nothing.
	var out bytes.Buffer
	if err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, nil, &invocation{}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	deps.stderr = &stderr
	if err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, nil, &invocation{}); err != nil {
		t.Fatal(err)
	}
	if said := stderr.String(); strings.Count(said, "\n") != 1 || !strings.Contains(said, "os, arch, cpu_count and boot_time") {
		t.Fatalf("with no progress writer the first publish said %q on stderr, want the note once", said)
	}
	stderr.Reset()
	if err := runRemotePublishWithProgress(deps, t.TempDir(), "", 1, false, true, &out, nil, &invocation{}); err != nil || stderr.Len() != 0 {
		t.Fatalf("the note was repeated: %v %q", err, stderr.String())
	}
	// A publish that had to leave the optional fields out says so there too.
	refusing := publishDeps(&capturingProvider{errs: []error{refusedWith400{}}}, func() (string, error) { return "alice", nil }, &opened)
	refusing.configPath, refusing.stderr = deps.configPath, &stderr
	if err := runRemotePublishWithProgress(refusing, t.TempDir(), "", 1, false, true, &out, nil, &invocation{}); err != nil || !strings.Contains(stderr.String(), "published without them") {
		t.Fatalf("a publish without the optional fields: %v, stderr %q", err, stderr.String())
	}
	if defaultRemoteDeps().stderr != os.Stderr {
		t.Fatal("the command's own dependencies have no stderr")
	}
}
