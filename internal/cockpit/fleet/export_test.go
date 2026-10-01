package fleet

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// exportedEnvelope is this machine's export built in process from the sentinel
// sources (a local worktree, an agent, and another machine's cached snapshot),
// with the sampler holding 3 samples, and the clock it was built at.
func exportedEnvelope(t *testing.T, metricsOnly bool) (Envelope, time.Time) {
	t.Helper()
	snapshotter, clock := newSnapshotter(sentinelSources().collectors(), func(options *Options) {
		options.Sampler = filledSampler(t, &countingSource{}, 3)
	})
	refreshAndSettle(t, snapshotter)
	return snapshotter.Export(metricsOnly), clock.Now()
}

// copyEnvelope is a deep copy, so a case can change it freely.
func copyEnvelope(t *testing.T, envelope Envelope) Envelope {
	t.Helper()
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var clone Envelope
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

// TestExportCarriesOnlyThisMachinesEntries proves
// cockpit-views#ac:export-carries-only-the-metadata-set for the envelope: the
// cached entries of another machine's snapshot are not exported, the local
// ones are, a metrics-only export has no fleet, no source sentinel appears,
// and what is built passes its own strict decoder.
func TestExportCarriesOnlyThisMachinesEntries(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, sentinel) {
		t.Fatalf("the export carries a source sentinel: %s", text)
	}
	for _, want := range []string{`"task-a"`, `"agt-1"`, `"` + testMachine + `"`, `"schema_version":1`} {
		if !strings.Contains(text, want) {
			t.Errorf("the export lacks %s: %s", want, text)
		}
	}
	for _, other := range []string{`"desktop"`, `"task-x"`, `"stream-x"`, `acme/gadgets`} {
		if strings.Contains(text, other) {
			t.Errorf("the export carries another machine's entry %s: %s", other, text)
		}
	}
	if len(full.Fleet.Machines) != 1 || full.Fleet.Machines[0].Route != RouteLocal || full.Machine != testMachine {
		t.Errorf("machines = %+v, machine = %q, want this machine alone", full.Fleet.Machines, full.Machine)
	}
	if full.Metrics.Route != RouteLocal || len(full.Metrics.Samples) != 3 || !full.ExportedAt.Equal(now) {
		t.Errorf("metrics = %+v, exported_at = %v", full.Metrics, full.ExportedAt)
	}
	if _, err := DecodeEnvelope(bytes.NewReader(body), false, now); err != nil {
		t.Errorf("the envelope this binary writes is refused by its own decoder: %v", err)
	}

	only, _ := exportedEnvelope(t, true)
	onlyBody, _ := json.Marshal(only)
	if only.Fleet != nil || strings.Contains(string(onlyBody), `"fleet"`) || only.Metrics == nil {
		t.Errorf("a metrics-only export = %s, want metrics and no fleet", onlyBody)
	}
	if _, err := DecodeEnvelope(bytes.NewReader(onlyBody), true, now); err != nil {
		t.Errorf("the metrics-only envelope is refused by its own decoder: %v", err)
	}
}

// TestEnvelopeFieldsAreExactlyTheNamedSet pins the envelope's own field lists.
func TestEnvelopeFieldsAreExactlyTheNamedSet(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		got, want []string
	}{
		"Envelope":        {jsonFields(Envelope{}), []string{"schema_version", "machine", "exported_at", "fleet", "metrics"}},
		"EnvelopeMetrics": {jsonFields(EnvelopeMetrics{}), []string{"route", "samples", "reason"}},
		"ExportError":     {jsonFields(ExportError{}), []string{"schema_version", "error"}},
	} {
		if !sameSet(test.got, test.want) {
			t.Errorf("%s fields = %v, want exactly %v", name, test.got, test.want)
		}
	}
}

func TestNewExportErrorNamesTheCode(t *testing.T) {
	t.Parallel()
	body, _ := json.Marshal(NewExportError(ErrorExportRefused))
	if string(body) != `{"schema_version":1,"error":"export_refused"}` {
		t.Errorf("error = %s", body)
	}
}

func TestExportMetricsOfEachAnswer(t *testing.T) {
	t.Parallel()
	sample := machinemetrics.Sample{SampledAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	for name, test := range map[string]struct {
		in   MetricsResponse
		want EnvelopeMetrics
	}{
		"local history":     {MetricsResponse{Route: RouteLocal, Samples: []machinemetrics.Sample{sample}}, EnvelopeMetrics{Route: RouteLocal, Samples: []machinemetrics.Sample{sample}}},
		"local with none":   {MetricsResponse{Route: RouteLocal}, EnvelopeMetrics{Route: RouteLocal, Samples: []machinemetrics.Sample{}}},
		"none and a reason": {MetricsResponse{Route: RouteNone, Reason: ReasonUnsupported}, EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnsupported}},
		"none and a bad one": {
			MetricsResponse{Route: RouteNone, Reason: "free text"},
			EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnavailable},
		},
		"live remote is not exported": {
			MetricsResponse{Route: RouteLiveRemote, Samples: []machinemetrics.Sample{sample}},
			EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnavailable},
		},
	} {
		got := *exportMetrics(test.in)
		gotBody, _ := json.Marshal(got)
		wantBody, _ := json.Marshal(test.want)
		if string(gotBody) != string(wantBody) {
			t.Errorf("%s: got %s, want %s", name, gotBody, wantBody)
		}
	}
}

// TestEnvelopeWithNoLocalMachineHasNoName is the warming-up daemon: a document
// with no machine entry yet still yields a well-formed envelope.
func TestEnvelopeWithNoLocalMachineHasNoName(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	envelope := NewEnvelope(emptyDocument(time.Minute), MetricsResponse{Route: RouteNone, Reason: ReasonNoSource}, now, false)
	if envelope.Machine != "" || envelope.Fleet == nil || len(envelope.Fleet.Worktrees) != 0 {
		t.Fatalf("envelope = %+v", envelope)
	}
	if err := envelope.Validate(false, now); err != nil {
		t.Fatal(err)
	}
}

// Export is this machine's export envelope built in process from the last
// published document and the sampler, without running anything: what the hub
// route will serve. It lives with the tests until that route exists, because
// nothing outside a test calls it yet.
func (s *Snapshotter) Export(metricsOnly bool) Envelope {
	s.mu.RLock()
	document := s.doc
	s.mu.RUnlock()
	now := s.now()
	answer := sanitizeMetrics(s.metricsAnswerFor(localMachineID(s.machine)), now)
	return NewEnvelope(document, MetricsResponse{Route: answer.Route, Samples: answer.Samples, Reason: answer.Reason}, now, metricsOnly)
}

// TestExportCarriesTheAgentActivityAndRunLinkFields proves the envelope of this
// machine's agents holds activity, task, worktrees, started_at, finished_at and
// exit_code, that it passes its own strict decoder, and that the same envelope
// with an activity outside herdr's five values is refused.
func TestExportCarriesTheAgentActivityAndRunLinkFields(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	var session, finished Agent
	for _, agent := range full.Fleet.Agents {
		switch agent.RunID {
		case "":
			session = agent
		case "agt-2":
			finished = agent
		}
	}
	if session.Activity != ActivityBlocked || session.Task != "task-a" || len(session.Worktrees) != 1 || session.StartedAt.IsZero() {
		t.Errorf("exported session = %+v", session)
	}
	if finished.Task != "task-a" || len(finished.Worktrees) != 1 || finished.StartedAt.IsZero() || finished.FinishedAt.IsZero() || finished.ExitCode == nil || *finished.ExitCode != 2 {
		t.Errorf("exported finished run = %+v", finished)
	}
	body, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(bytes.NewReader(body), false, now); err != nil {
		t.Fatalf("the exported envelope is refused: %v", err)
	}
	hostile := bytes.Replace(body, []byte(`"activity":"blocked"`), []byte(`"activity":"napping"`), 1)
	if bytes.Equal(hostile, body) {
		t.Fatal("the test did not change the activity")
	}
	if _, err := DecodeEnvelope(bytes.NewReader(hostile), false, now); err == nil || !strings.Contains(err.Error(), "activity is not valid") {
		t.Errorf("an unknown activity = %v, want it refused", err)
	}
}
