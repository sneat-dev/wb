package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
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

// refused runs a hostile case: the mutation makes a valid envelope hostile and
// Validate must refuse it with a BadEnvelopeError naming a field, never the
// hostile value.
func TestHostileEnvelopesAreRefused(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	only, _ := exportedEnvelope(t, true)
	long := strings.Repeat("a", 257)
	future := now.Add(time.Hour)
	negative := -1
	notANumber, infinity := math.NaN(), math.Inf(1)
	big, small := uint64(10), uint64(5)
	cases := map[string]struct {
		metricsOnly bool
		change      func(*Envelope)
		reason      string
	}{
		"schema version":                  {false, func(e *Envelope) { e.SchemaVersion = 2 }, "schema_version"},
		"fleet schema version":            {false, func(e *Envelope) { e.Fleet.SchemaVersion = 1 }, "fleet.schema_version"},
		"machine too long":                {false, func(e *Envelope) { e.Machine = long }, "machine is longer"},
		"machine with a control":          {false, func(e *Envelope) { e.Machine = "vm\x00" }, "machine is not plain"},
		"exported_at missing":             {false, func(e *Envelope) { e.ExportedAt = time.Time{} }, "exported_at"},
		"exported_at in the future":       {false, func(e *Envelope) { e.ExportedAt = future }, "exported_at"},
		"no fleet":                        {false, func(e *Envelope) { e.Fleet = nil }, "no fleet"},
		"fleet in a metrics-only export":  {true, func(e *Envelope) { e.Fleet = copyEnvelope(t, full).Fleet }, "carries a fleet"},
		"no metrics":                      {false, func(e *Envelope) { e.Metrics = nil }, "no metrics"},
		"a string over 256 bytes":         {false, func(e *Envelope) { e.Fleet.Worktrees[0].Task = long }, "fleet.worktrees[0].task is longer"},
		"a control character":             {false, func(e *Envelope) { e.Fleet.Worktrees[0].Branch = "a\x1bb" }, "fleet.worktrees[0].branch is not plain"},
		"a bidirectional control":         {false, func(e *Envelope) { e.Fleet.Worktrees[0].Name = "a\u202eb" }, "fleet.worktrees[0].name is not plain"},
		"a line separator":                {false, func(e *Envelope) { e.Fleet.Repositories[0].Name = "a b" }, "fleet.repositories[0].name is not plain"},
		"invalid utf-8":                   {false, func(e *Envelope) { e.Fleet.Agents[0].Model = "a\xffb" }, "fleet.agents[0].model is not plain"},
		"a plain http address":            {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "http://github.com/a/b/pull/1" }, "fleet.pull_requests[0].url is not a safe"},
		"an address with a port":          {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://github.com:8443/a/b/pull/1" }, "url is not a safe"},
		"an address of an IP":             {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://127.0.0.1/a" }, "url is not a safe"},
		"an address over 2048 bytes":      {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://github.com/" + strings.Repeat("a", 2048) }, "url is longer than 2048"},
		"a web address with user info":    {false, func(e *Envelope) { e.Fleet.Repositories[0].RemoteURLWeb = "https://user@github.com/a/b" }, "remote_url_web is not a safe"},
		"a negative counter":              {false, func(e *Envelope) { e.Fleet.RepositoriesTotal = -1 }, "fleet.repositories_total is negative"},
		"a negative counter in a pointer": {false, func(e *Envelope) { e.Fleet.Worktrees[0].Ahead = &negative }, "fleet.worktrees[0].ahead is negative"},
		"a negative count in an entry":    {false, func(e *Envelope) { e.Fleet.Repositories[0].WorktreeCount = -3 }, "fleet.repositories[0].worktree_count is negative"},
		"a future observation":            {false, func(e *Envelope) { e.Fleet.Worktrees[0].ObservedAt = future }, "fleet.worktrees[0].observed_at is in the future"},
		"a future activity":               {false, func(e *Envelope) { e.Fleet.Worktrees[0].LastActivityAt = future }, "last_activity_at is in the future"},
		"a future boot time":              {false, func(e *Envelope) { e.Fleet.Machines[0].BootTime = future }, "boot_time is in the future"},
		"a future snapshot":               {false, func(e *Envelope) { e.Fleet.SnapshotAt = future }, "snapshot_at is in the future"},
		"too many agents":                 {false, func(e *Envelope) { e.Fleet.Agents = make([]Agent, 201) }, "fleet.agents has more than 200"},
		"too many machines":               {false, func(e *Envelope) { e.Fleet.Machines = make([]Machine, 65) }, "fleet.machines has more than 64"},
		"too many repositories":           {false, func(e *Envelope) { e.Fleet.Repositories = make([]Repository, 5001) }, "fleet.repositories has more than 5000"},
		"too many worktrees":              {false, func(e *Envelope) { e.Fleet.Worktrees = make([]Worktree, 5001) }, "fleet.worktrees has more than 5000"},
		"too many pull requests":          {false, func(e *Envelope) { e.Fleet.PullRequests = make([]PullRequest, 5001) }, "fleet.pull_requests has more than 5000"},
		"an unknown route":                {false, func(e *Envelope) { e.Fleet.Worktrees[0].Route = "elsewhere" }, "fleet.worktrees[0].route"},
		"an unknown machine route":        {false, func(e *Envelope) { e.Fleet.Machines[0].Route = "" }, "fleet.machines[0].route"},
		"an unknown repository route":     {false, func(e *Envelope) { e.Fleet.Repositories[0].Route = "x" }, "fleet.repositories[0].route"},
		"an unknown pull request route":   {false, func(e *Envelope) { e.Fleet.PullRequests[0].Route = "x" }, "fleet.pull_requests[0].route"},
		"an unknown agent route":          {false, func(e *Envelope) { e.Fleet.Agents[0].Route = "x" }, "fleet.agents[0].route"},
		"an unknown owner state":          {false, func(e *Envelope) { e.Fleet.Worktrees[0].OwnerState = "asleep" }, "owner_state"},
		"an unknown agent kind":           {false, func(e *Envelope) { e.Fleet.Agents[0].Kind = "daemon" }, "fleet.agents[0].kind"},
		"too many code indexes":           {false, func(e *Envelope) { e.Fleet.Worktrees[0].CodeIndex = make([]CodeIndex, 33) }, "code_index has more than 32"},
		"too many kinds": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Statistics: &CodeStatistics{Kinds: make([]KindCount, 33)}}}
		}, "kinds has more than 32"},
		"a string in a code index":    {false, func(e *Envelope) { e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: long}} }, "indexer is longer"},
		"metrics of an unknown route": {false, func(e *Envelope) { e.Metrics.Route = RouteLiveRemote }, "metrics.route is not"},
		"none with samples":           {false, func(e *Envelope) { e.Metrics.Route = RouteNone; e.Metrics.Reason = ReasonNoSource }, "needs a reason and no samples"},
		"none without a reason":       {false, func(e *Envelope) { e.Metrics = &EnvelopeMetrics{Route: RouteNone} }, "needs a reason"},
		"none with a free-text reason": {false, func(e *Envelope) {
			e.Metrics = &EnvelopeMetrics{Route: RouteNone, Reason: "boom"}
		}, "needs a reason"},
		"a reason on a history":   {false, func(e *Envelope) { e.Metrics.Reason = ReasonNoSource }, "metrics.reason"},
		"361 samples":             {false, func(e *Envelope) { e.Metrics.Samples = make([]machinemetrics.Sample, 361) }, "more than 360"},
		"a sample with no time":   {false, func(e *Envelope) { e.Metrics.Samples[1].SampledAt = time.Time{} }, "metrics.samples[1].sampled_at"},
		"a sample in the future":  {false, func(e *Envelope) { e.Metrics.Samples[2].SampledAt = future }, "metrics.samples[2].sampled_at"},
		"samples out of order":    {false, func(e *Envelope) { e.Metrics.Samples[1].SampledAt = e.Metrics.Samples[0].SampledAt }, "increasing order"},
		"a percent over 100":      {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = ptr(100.5) }, "cpu_percent"},
		"a negative percent":      {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = ptr(-1.0) }, "cpu_percent"},
		"a percent that is NaN":   {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = &notANumber }, "cpu_percent"},
		"a negative load":         {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = ptr(-0.5) }, "load1"},
		"a load that is infinite": {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = &infinity }, "load1"},
		"a load that is NaN":      {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = &notANumber }, "load1"},
		"memory used above total": {false, func(e *Envelope) {
			e.Metrics.Samples[0].MemoryUsedBytes, e.Metrics.Samples[0].MemoryTotalBytes = &big, &small
		}, "memory figures"},
		"memory used with no total": {false, func(e *Envelope) {
			e.Metrics.Samples[0].MemoryUsedBytes, e.Metrics.Samples[0].MemoryTotalBytes = &small, nil
		}, "memory figures"},
		"memory total with no used": {false, func(e *Envelope) {
			e.Metrics.Samples[0].MemoryUsedBytes, e.Metrics.Samples[0].MemoryTotalBytes = nil, &small
		}, "memory figures"},
		"disk free above total": {false, func(e *Envelope) {
			e.Metrics.Samples[0].DiskFreeBytes, e.Metrics.Samples[0].DiskTotalBytes = &big, &small
		}, "disk figures"},
		"disk free with no total": {false, func(e *Envelope) {
			e.Metrics.Samples[0].DiskFreeBytes, e.Metrics.Samples[0].DiskTotalBytes = &small, nil
		}, "disk figures"},
	}
	for name, test := range cases {
		base := full
		if test.metricsOnly {
			base = only
		}
		envelope := copyEnvelope(t, base)
		test.change(&envelope)
		err := envelope.Validate(test.metricsOnly, now)
		var bad *BadEnvelopeError
		if !errors.As(err, &bad) || !strings.Contains(bad.Reason, test.reason) {
			t.Errorf("%s: err = %v, want a refusal naming %q", name, err, test.reason)
			continue
		}
		if strings.Contains(err.Error(), long) {
			t.Errorf("%s: the refusal echoes the hostile value", name)
		}
	}
}

// TestSampleOnTheBoundariesIsAccepted is the other side of the limits: the
// values exactly on a cap are fine.
func TestSampleOnTheBoundariesIsAccepted(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	envelope := copyEnvelope(t, full)
	total := uint64(7)
	envelope.Metrics.Samples = make([]machinemetrics.Sample, 360)
	for index := range envelope.Metrics.Samples {
		envelope.Metrics.Samples[index] = machinemetrics.Sample{SampledAt: now.Add(-time.Duration(360-index) * time.Second)}
	}
	envelope.Metrics.Samples[0].CPUPercent, envelope.Metrics.Samples[0].Load1 = ptr(100.0), ptr(0.0)
	envelope.Metrics.Samples[1].MemoryUsedBytes, envelope.Metrics.Samples[1].MemoryTotalBytes = &total, &total
	envelope.Metrics.Samples[2].DiskFreeBytes, envelope.Metrics.Samples[2].DiskTotalBytes = ptr(uint64(0)), &total
	envelope.Fleet.Agents = make([]Agent, 200)
	for index := range envelope.Fleet.Agents {
		envelope.Fleet.Agents[index] = Agent{Entry: Entry{Route: RouteLocal}, Kind: AgentRun}
	}
	envelope.Fleet.Worktrees[0].Task = strings.Repeat("é", 128) // 256 bytes
	envelope.Fleet.Worktrees[0].LastActivityAt = now.Add(maxSkew)
	if err := envelope.Validate(false, now); err != nil {
		t.Fatal(err)
	}
	envelope.Fleet.Worktrees[0].Task = strings.Repeat("é", 129) // 258 bytes
	if err := envelope.Validate(false, now); err == nil {
		t.Fatal("a 258 byte string was accepted")
	}
}

func TestDecodeEnvelopeIsStrict(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	only, _ := exportedEnvelope(t, true)
	marshal := func(value any) string { body, _ := json.Marshal(value); return string(body) }
	valid, validOnly := marshal(full), marshal(only)
	insert := func(body, field string) string { return strings.Replace(body, `{`, `{`+field+`,`, 1) }

	cases := map[string]struct {
		body        string
		metricsOnly bool
		reason      string
	}{
		"an unknown top-level field":  {insert(valid, `"extra":1`), false, "unknown field"},
		"an unknown nested field":     {strings.Replace(valid, `"worktrees":[{`, `"worktrees":[{"path":"/home/x",`, 1), false, "unknown field"},
		"an unknown metrics field":    {strings.Replace(valid, `"metrics":{`, `"metrics":{"env":"x",`, 1), false, "unknown field"},
		"an unknown sample field":     {strings.Replace(valid, `"samples":[{`, `"samples":[{"cmdline":"x",`, 1), false, "unknown field"},
		"a second value":              {valid + valid, false, "data after"},
		"trailing garbage":            {valid + " x", false, "data after"},
		"not JSON":                    {"not json", false, "not an envelope"},
		"an empty body":               {"", false, "not an envelope"},
		"a wrong type":                {strings.Replace(valid, `"schema_version":1`, `"schema_version":"1"`, 1), false, "not an envelope"},
		"a negative unsigned figure":  {strings.Replace(valid, `"memory_total_bytes":`, `"memory_total_bytes":-`, 1), false, "not an envelope"},
		"a number too large":          {strings.Replace(valid, `"schema_version":1`, `"schema_version":1e999`, 1), false, "not an envelope"},
		"a fleet in a metrics export": {valid, true, "carries a fleet"},
		"no fleet in a full export":   {validOnly, false, "no fleet"},
		"a body over 8 MiB":           {valid + strings.Repeat(" ", MaxEnvelopeBytes), false, "larger than"},
	}
	for name, test := range cases {
		_, err := DecodeEnvelope(strings.NewReader(test.body), test.metricsOnly, now)
		var bad *BadEnvelopeError
		if !errors.As(err, &bad) || !strings.Contains(bad.Reason, test.reason) {
			t.Errorf("%s: err = %v, want a refusal naming %q", name, err, test.reason)
		}
	}

	if _, err := DecodeEnvelope(strings.NewReader(valid), false, now); err != nil {
		t.Errorf("a valid envelope: %v", err)
	}
	if _, err := DecodeEnvelope(strings.NewReader(validOnly+"\n"), true, now); err != nil {
		t.Errorf("a valid metrics-only envelope with a newline: %v", err)
	}
	exact := valid + strings.Repeat(" ", MaxEnvelopeBytes-len(valid))
	if _, err := DecodeEnvelope(strings.NewReader(exact), false, now); err != nil {
		t.Errorf("an envelope of exactly 8 MiB: %v", err)
	}
	_, err := DecodeEnvelope(iotest.ErrReader(errors.New("boom")), false, now)
	var bad *BadEnvelopeError
	if err == nil || errors.As(err, &bad) {
		t.Errorf("a read failure = %v, want a plain error and not a refusal of content", err)
	}
}

// TestDecodeRefusalDoesNotEchoTheRemote: a hostile field name in a parse error
// is cut to plain text of at most 200 runes.
func TestDecodeRefusalDoesNotEchoTheRemote(t *testing.T) {
	t.Parallel()
	_, now := exportedEnvelope(t, true)
	hostile := strings.Repeat("x", 5000) + "\x1b[31m"
	_, err := DecodeEnvelope(strings.NewReader(`{"`+hostile+`":1}`), true, now)
	if err == nil || len(err.Error()) > 300 || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("err = %q", err)
	}
}

type kinds struct {
	Name    string `json:"name"`
	Count   uint16 `json:"count"`
	On      bool   `json:"on"`
	Score32 float32
	hidden  string
	Skipped string                 `json:"-"`
	Nest    struct{ Inner string } `json:"nest"`
	Embedded
	Optional *string `json:"optional"`
}

type Embedded struct {
	Shared string `json:"shared"`
}

func TestCheckValueHoldsEveryKindToItsRule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	value := kinds{Name: "ok", Count: 3, Score32: 1.5, Optional: ptr("fine"), hidden: "\x00"}
	if err := checkValue(reflectOf(&value), "root", "", now); err != nil {
		t.Fatalf("a clean value: %v", err)
	}
	value.Skipped = strings.Repeat("a", 300) // the `json:"-"` field is not on the wire
	if err := checkValue(reflectOf(&value), "root", "", now); err != nil {
		t.Fatalf("a field that is not on the wire: %v", err)
	}
	for name, change := range map[string]func(*kinds){
		"float NaN":            func(v *kinds) { v.Score32 = float32(math.NaN()) },
		"float negative":       func(v *kinds) { v.Score32 = -1 },
		"nested string":        func(v *kinds) { v.Nest.Inner = "a\x00" },
		"embedded string":      func(v *kinds) { v.Shared = "a\x00" },
		"pointed-to string":    func(v *kinds) { v.Optional = ptr(strings.Repeat("a", 300)) },
		"untagged field named": func(v *kinds) { v.Nest.Inner = strings.Repeat("a", 300) },
	} {
		clone := value
		change(&clone)
		if err := checkValue(reflectOf(&clone), "root", "", now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var unsupported struct{ Labels map[string]string }
	unsupported.Labels = map[string]string{"a": "b"}
	if err := checkValue(reflectOf(&unsupported), "root", "", now); err == nil || !strings.Contains(err.Error(), "root.Labels") {
		t.Errorf("a map was accepted: %v", err)
	}
}

func reflectOf(pointer any) reflect.Value { return reflect.ValueOf(pointer).Elem() }

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
