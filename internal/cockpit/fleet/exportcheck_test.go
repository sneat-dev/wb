package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

func refusalOf(t *testing.T, err error) string {
	t.Helper()
	var bad *BadEnvelopeError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a refusal of the envelope", err)
	}
	return bad.Reason
}

// codeIndexed is a code index with statistics, for a repository or a worktree.
func codeIndexed() []CodeIndex {
	return []CodeIndex{{Indexer: "codegrapher", State: CodeIndexFresh, Statistics: &CodeStatistics{Indexed: true, Files: 3, Kinds: []KindCount{{Kind: "func", Count: 2}}}}}
}

// TestHostileEnvelopesAreRefused runs each hostile change against a valid
// envelope: Validate must refuse it, naming the field and never the value.
func TestHostileEnvelopesAreRefused(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	only, _ := exportedEnvelope(t, true)
	long := strings.Repeat("a", 257)
	future := now.Add(time.Hour)
	negative, huge := -1, maxCount+1
	notANumber, infinity := math.NaN(), math.Inf(1)
	big, small := uint64(10), uint64(5)
	another := entryID(kindWorktree, "elsewhere")
	cases := map[string]struct {
		metricsOnly bool
		change      func(*Envelope)
		reason      string
	}{
		"schema version":                  {false, func(e *Envelope) { e.SchemaVersion = 2 }, "schema_version"},
		"fleet schema version":            {false, func(e *Envelope) { e.Fleet.SchemaVersion = 1 }, "fleet.schema_version"},
		"machine too long":                {false, func(e *Envelope) { e.Machine = long }, "machine is not valid"},
		"machine with a control":          {false, func(e *Envelope) { e.Machine = "vm\x00" }, "machine is not valid"},
		"exported_at missing":             {false, func(e *Envelope) { e.ExportedAt = time.Time{} }, "exported_at"},
		"exported_at in the future":       {false, func(e *Envelope) { e.ExportedAt = future }, "exported_at"},
		"exported_at before 2000":         {false, func(e *Envelope) { e.ExportedAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC) }, "exported_at"},
		"no fleet":                        {false, func(e *Envelope) { e.Fleet = nil }, "no fleet"},
		"fleet in a metrics-only export":  {true, func(e *Envelope) { e.Fleet = copyEnvelope(t, full).Fleet }, "carries a fleet"},
		"no metrics":                      {false, func(e *Envelope) { e.Metrics = nil }, "no metrics"},
		"a string over 256 bytes":         {false, func(e *Envelope) { e.Fleet.Worktrees[0].Task = long }, "fleet.worktrees[0].task is not valid"},
		"a control character":             {false, func(e *Envelope) { e.Fleet.Worktrees[0].Branch = "a\x1bb" }, "fleet.worktrees[0].branch is not valid"},
		"a bidirectional control":         {false, func(e *Envelope) { e.Fleet.Worktrees[0].Name = "a\u202eb" }, "fleet.worktrees[0].name is not valid"},
		"a line separator":                {false, func(e *Envelope) { e.Fleet.Repositories[0].Name = "a\u2028b" }, "fleet.repositories[0].name is not valid"},
		"the replacement character":       {false, func(e *Envelope) { e.Fleet.Agents[0].Model = "a\ufffdb" }, "fleet.agents[0].model is not valid"},
		"a private-use character":         {false, func(e *Envelope) { e.Fleet.Worktrees[0].Task = "a\ue000b" }, "task is not valid"},
		"the Hangul filler":               {false, func(e *Envelope) { e.Fleet.Worktrees[0].Task = "a\u3164b" }, "task is not valid"},
		"the blank Braille pattern":       {false, func(e *Envelope) { e.Fleet.Worktrees[0].Task = "\u2800" }, "task is not valid"},
		"a no-break space":                {false, func(e *Envelope) { e.Fleet.Worktrees[0].Stream = "a\u00a0b" }, "stream is not valid"},
		"an em space":                     {false, func(e *Envelope) { e.Fleet.Worktrees[0].Stream = "a\u2003b" }, "stream is not valid"},
		"a plain http address":            {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "http://github.com/a/b/pull/1" }, "fleet.pull_requests[0].url is not valid"},
		"an address with a port":          {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://github.com:8443/a/b/pull/1" }, "url is not valid"},
		"an address of an IP":             {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://127.0.0.1/a" }, "url is not valid"},
		"an address over 2048 bytes":      {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://github.com/" + strings.Repeat("a", 2048) }, "url is not valid"},
		"a web address with user info":    {false, func(e *Envelope) { e.Fleet.Repositories[0].RemoteURLWeb = "https://user@github.com/a/b" }, "remote_url_web is not valid"},
		"a web address not from its host": {false, func(e *Envelope) { e.Fleet.Repositories[0].RemoteURLWeb = "https://evil.example/acme/widgets" }, "remote_url_web is not built"},
		"a pull request off its host":     {false, func(e *Envelope) { e.Fleet.PullRequests[0].URL = "https://evil.example/acme/widgets/pull/7" }, "not on its repository's host"},
		"a negative counter":              {false, func(e *Envelope) { e.Fleet.RepositoriesTotal = -1 }, "fleet.repositories_total is negative"},
		"a negative counter in a pointer": {false, func(e *Envelope) { e.Fleet.Worktrees[0].Ahead = &negative }, "fleet.worktrees[0].ahead is negative"},
		"a counter over 10 million":       {false, func(e *Envelope) { e.Fleet.Worktrees[0].Behind = &huge }, "fleet.worktrees[0].behind is negative or too large"},
		"a future observation":            {false, func(e *Envelope) { e.Fleet.Worktrees[0].ObservedAt = future }, "fleet.worktrees[0].observed_at is before 2000 or in the future"},
		"a future activity":               {false, func(e *Envelope) { e.Fleet.Worktrees[0].LastActivityAt = future }, "last_activity_at is before 2000"},
		"an ancient activity":             {false, func(e *Envelope) { e.Fleet.Worktrees[0].LastActivityAt = time.Unix(0, 0) }, "last_activity_at is before 2000"},
		"a future boot time":              {false, func(e *Envelope) { e.Fleet.Machines[0].BootTime = future }, "boot_time is before 2000"},
		"a boot time before 2000":         {false, func(e *Envelope) { e.Fleet.Machines[0].BootTime = time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC) }, "boot_time is before 2000"},
		"a future snapshot":               {false, func(e *Envelope) { e.Fleet.SnapshotAt = future }, "snapshot_at is before 2000 or in the future"},
		"too many agents":                 {false, func(e *Envelope) { e.Fleet.Agents = make([]Agent, 201) }, "fleet.agents has more than 200"},
		"two machines":                    {false, func(e *Envelope) { e.Fleet.Machines = make([]Machine, 2) }, "fleet.machines has more than 1"},
		"too many repositories":           {false, func(e *Envelope) { e.Fleet.Repositories = make([]Repository, 5001) }, "fleet.repositories has more than 5000"},
		"too many worktrees":              {false, func(e *Envelope) { e.Fleet.Worktrees = make([]Worktree, 5001) }, "fleet.worktrees has more than 5000"},
		"too many pull requests":          {false, func(e *Envelope) { e.Fleet.PullRequests = make([]PullRequest, 5001) }, "fleet.pull_requests has more than 5000"},
		"a cached entry":                  {false, func(e *Envelope) { e.Fleet.Worktrees[0].Route = RouteCached }, "fleet.worktrees[0].route is not valid"},
		"a live-remote entry":             {false, func(e *Envelope) { e.Fleet.Agents[0].Route = RouteLiveRemote }, "fleet.agents[0].route is not valid"},
		"an entry with no route":          {false, func(e *Envelope) { e.Fleet.Machines[0].Route = "" }, "fleet.machines[0].route is not valid"},
		"an empty id":                     {false, func(e *Envelope) { e.Fleet.Repositories[0].ID = "" }, "fleet.repositories[0].id is not valid"},
		"an id of the wrong shape":        {false, func(e *Envelope) { e.Fleet.Repositories[0].ID = "repo-../../etc" }, "id is not valid"},
		"a duplicate id":                  {false, func(e *Envelope) { e.Fleet.Agents[0].ID = e.Fleet.Worktrees[0].ID }, "id is not unique"},
		"another machine's id":            {false, func(e *Envelope) { e.Fleet.Worktrees[0].MachineID = another }, "machine_id is not the machine's"},
		"a machine whose id differs":      {false, func(e *Envelope) { e.Fleet.Machines[0].MachineID = another }, "fleet.machines[0].machine_id is not its id"},
		"entries and no machine":          {false, func(e *Envelope) { e.Fleet.Machines = []Machine{} }, "entries and no machine"},
		"a null collection":               {false, func(e *Envelope) { e.Fleet.Worktrees = nil }, "null collection"},
		"a null kinds list": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = codeIndexed()
			e.Fleet.Repositories[0].CodeIndex[0].Statistics.Kinds = nil
		}, "null kinds"},
		"a null kinds list on a worktree": {false, func(e *Envelope) {
			e.Fleet.Worktrees[0].CodeIndex = codeIndexed()
			e.Fleet.Worktrees[0].CodeIndex[0].Statistics.Kinds = nil
		}, "null kinds"},
		"an unknown owner state":           {false, func(e *Envelope) { e.Fleet.Worktrees[0].OwnerState = "asleep" }, "owner_state is not valid"},
		"an unknown lifecycle":             {false, func(e *Envelope) { e.Fleet.Worktrees[0].Lifecycle = "zombie" }, "lifecycle is not valid"},
		"an unknown agent kind":            {false, func(e *Envelope) { e.Fleet.Agents[0].Kind = "daemon" }, "fleet.agents[0].kind is not valid"},
		"an unknown agent state":           {false, func(e *Envelope) { e.Fleet.Agents[0].State = "exploding" }, "fleet.agents[0].state is not valid"},
		"an agent with no state":           {false, func(e *Envelope) { e.Fleet.Agents[0].State = "" }, "fleet.agents[0].state is not valid"},
		"a runtime that is free text":      {false, func(e *Envelope) { e.Fleet.Agents[0].Runtime = "rm -rf /" }, "runtime is not valid"},
		"a model that is too long":         {false, func(e *Envelope) { e.Fleet.Agents[0].Model = strings.Repeat("m", 65) }, "model is not valid"},
		"a session id with a slash":        {false, func(e *Envelope) { e.Fleet.Agents[0].SessionID = "a/b" }, "session_id is not valid"},
		"a run id with a space":            {false, func(e *Envelope) { e.Fleet.Agents[0].RunID = "a b" }, "run_id is not valid"},
		"an agent repository that is text": {false, func(e *Envelope) { e.Fleet.Agents[0].Repository = "acme/widgets" }, "repository is not valid"},
		"an unknown pull request state":    {false, func(e *Envelope) { e.Fleet.PullRequests[0].State = "stuck" }, "pull_requests[0].state is not valid"},
		"an os that is free text":          {false, func(e *Envelope) { e.Fleet.Machines[0].OS = "linux; rm" }, "os is not valid"},
		"an arch that is free text":        {false, func(e *Envelope) { e.Fleet.Machines[0].Arch = "x 86" }, "arch is not valid"},
		"a version that is free text":      {false, func(e *Envelope) { e.Fleet.Machines[0].WBVersion = "v1 <script>" }, "wb_version is not valid"},
		"a host that is not a hostname":    {false, func(e *Envelope) { e.Fleet.Repositories[0].Host = "a b" }, "host is not valid"},
		"a repository error that is text":  {false, func(e *Envelope) { e.Fleet.Repositories[0].Error = "/home/x: denied" }, "fleet.repositories[0].error is not valid"},
		"a document error that is text":    {false, func(e *Envelope) { e.Fleet.Error = "boom" }, "fleet.error is not valid"},
		"a provider name that is text":     {false, func(e *Envelope) { e.Fleet.CodeIndexProvider = "a b" }, "code_index_provider is not valid"},
		"too many code indexes":            {false, func(e *Envelope) { e.Fleet.Worktrees[0].CodeIndex = make([]CodeIndex, 33) }, "code_index has more than 32"},
		"too many kinds": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: "x", State: CodeIndexFresh, Statistics: &CodeStatistics{Kinds: make([]KindCount, 33)}}}
		}, "kinds has more than 32"},
		"an indexer that is text": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: "a b", State: CodeIndexFresh}}
		}, "indexer is not valid"},
		"an indexer that is empty": {false, func(e *Envelope) { e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{State: CodeIndexFresh}} }, "indexer is not valid"},
		"an unknown index state":   {false, func(e *Envelope) { e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: "x", State: "weird"}} }, "state is not valid"},
		"a statistics error that is text": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: "x", State: CodeIndexFresh, Statistics: &CodeStatistics{Kinds: []KindCount{}, Error: "/x failed"}}}
		}, "error is not valid"},
		"a kind that is not a word": {false, func(e *Envelope) {
			e.Fleet.Repositories[0].CodeIndex = []CodeIndex{{Indexer: "x", State: CodeIndexFresh, Statistics: &CodeStatistics{Kinds: []KindCount{{Kind: "Bad Kind"}}}}}
		}, "kind is not valid"},
		"metrics of an unknown route": {false, func(e *Envelope) { e.Metrics.Route = RouteLiveRemote }, "metrics.route is not valid"},
		"none with samples":           {false, func(e *Envelope) { e.Metrics.Route = RouteNone; e.Metrics.Reason = ReasonNoSource }, "needs a reason and no samples"},
		"none without a reason":       {false, func(e *Envelope) { e.Metrics = &EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}} }, "needs a reason"},
		"none with a free-text reason": {false, func(e *Envelope) {
			e.Metrics = &EnvelopeMetrics{Route: RouteNone, Reason: "boom", Samples: []machinemetrics.Sample{}}
		}, "metrics.reason is not valid"},
		"a reason on a history":   {false, func(e *Envelope) { e.Metrics.Reason = ReasonNoSource }, "metrics.reason is set"},
		"null samples":            {false, func(e *Envelope) { e.Metrics.Samples = nil }, "metrics.samples is null"},
		"361 samples":             {false, func(e *Envelope) { e.Metrics.Samples = make([]machinemetrics.Sample, 361) }, "more than 360"},
		"a sample with no time":   {false, func(e *Envelope) { e.Metrics.Samples[1].SampledAt = time.Time{} }, "metrics.samples[1].sampled_at"},
		"a sample in the future":  {false, func(e *Envelope) { e.Metrics.Samples[2].SampledAt = future }, "metrics.samples[2].sampled_at"},
		"a sample before 2000":    {false, func(e *Envelope) { e.Metrics.Samples[0].SampledAt = time.Unix(5, 0) }, "metrics.samples[0].sampled_at"},
		"samples out of order":    {false, func(e *Envelope) { e.Metrics.Samples[1].SampledAt = e.Metrics.Samples[0].SampledAt }, "increasing order"},
		"a percent over 100":      {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = ptr(100.5) }, "cpu_percent"},
		"a negative percent":      {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = ptr(-1.0) }, "cpu_percent"},
		"a percent that is NaN":   {false, func(e *Envelope) { e.Metrics.Samples[0].CPUPercent = &notANumber }, "cpu_percent"},
		"a negative load":         {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = ptr(-0.5) }, "load1"},
		"a load that is infinite": {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = &infinity }, "load1"},
		"a load that is NaN":      {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = &notANumber }, "load1"},
		"a load over 100000":      {false, func(e *Envelope) { e.Metrics.Samples[0].Load1 = ptr(100001.0) }, "load1 is out of range"},
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

// TestRepositoryAndPullRequestAddressesAreAcceptedWhenTheyMatch is the other
// side of the address rules: a web address built from its host and name, and a
// pull request address on the repository's host (in any case), are accepted.
func TestRepositoryAndPullRequestAddressesAreAcceptedWhenTheyMatch(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	envelope := copyEnvelope(t, full)
	repository := &envelope.Fleet.Repositories[0]
	repository.Host, repository.Name = "GitHub.com", "acme/widgets"
	repository.RemoteURLWeb = webURL(repository.Host, repository.Name)
	envelope.Fleet.PullRequests[0].Repository = repository.ID
	envelope.Fleet.PullRequests[0].URL = "https://github.com/acme/widgets/pull/7"
	if err := envelope.Validate(false, now); err != nil {
		t.Fatal(err)
	}
	envelope.Fleet.PullRequests[0].Repository = "" // no known repository: no host to compare with
	envelope.Fleet.PullRequests[0].URL = "https://elsewhere.example/x/y/pull/1"
	if err := envelope.Validate(false, now); err != nil {
		t.Fatal(err)
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
	envelope.Metrics.Samples[0].CPUPercent, envelope.Metrics.Samples[0].Load1 = ptr(100.0), ptr(float64(maxLoad))
	envelope.Metrics.Samples[1].MemoryUsedBytes, envelope.Metrics.Samples[1].MemoryTotalBytes = &total, &total
	envelope.Metrics.Samples[2].DiskFreeBytes, envelope.Metrics.Samples[2].DiskTotalBytes = ptr(uint64(0)), &total
	envelope.Fleet.Agents = make([]Agent, 200)
	for index := range envelope.Fleet.Agents {
		envelope.Fleet.Agents[index] = Agent{Entry: Entry{ID: entryID(kindAgent, "x", string(rune('a'+index%26)), strings.Repeat("z", index)), MachineID: envelope.Fleet.Machines[0].ID, Route: RouteLocal}, Kind: AgentRun, State: "running", Model: "claude-opus-4[1m]"}
	}
	envelope.Fleet.Worktrees[0].Task = strings.Repeat("é", 128) // 256 bytes
	envelope.Fleet.Worktrees[0].Name = "任务 name é"
	envelope.Fleet.Worktrees[0].LastActivityAt = now.Add(maxSkew)
	counter := maxCount
	envelope.Fleet.Worktrees[0].Ahead = &counter
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
		"an unknown top-level field":  {insert(valid, `"extra":1`), false, "an unknown field"},
		"an unknown nested field":     {strings.Replace(valid, `"worktrees":[{`, `"worktrees":[{"path":"/home/x",`, 1), false, "an unknown field"},
		"an unknown metrics field":    {strings.Replace(valid, `"metrics":{`, `"metrics":{"env":"x",`, 1), false, "an unknown field"},
		"an unknown sample field":     {strings.Replace(valid, `"samples":[{`, `"samples":[{"cmdline":"x",`, 1), false, "an unknown field"},
		"a second value":              {valid + valid, false, "data after"},
		"trailing garbage":            {valid + " x", false, "data after"},
		"not JSON":                    {"not json", false, "not valid JSON"},
		"an empty body":               {"", false, "not valid JSON"},
		"truncated JSON":              {valid[:len(valid)/2], false, "not valid JSON"},
		"a wrong type":                {strings.Replace(valid, `"schema_version":1`, `"schema_version":"1"`, 1), false, "wrong type"},
		"a negative unsigned figure":  {strings.Replace(valid, `"memory_total_bytes":`, `"memory_total_bytes":-`, 1), false, "wrong type"},
		"a number too large":          {strings.Replace(valid, `"schema_version":1`, `"schema_version":1e999`, 1), false, "not valid JSON"},
		"a bad time":                  {strings.Replace(valid, `"exported_at":"`, `"exported_at":"x`, 1), false, "invalid value"},
		"null collections":            {strings.Replace(valid, `"worktrees":[`, `"worktrees":null,"x":[`, 1), false, "an unknown field"},
		"a fleet in a metrics export": {valid, true, "carries a fleet"},
		"no fleet in a full export":   {validOnly, false, "no fleet"},
		"a body over 8 MiB":           {valid + strings.Repeat(" ", MaxEnvelopeBytes), false, "larger than"},
	}
	for name, test := range cases {
		_, err := DecodeEnvelope(strings.NewReader(test.body), test.metricsOnly, now)
		if reason := refusalOf(t, err); !strings.Contains(reason, test.reason) {
			t.Errorf("%s: reason = %q, want it to contain %q", name, reason, test.reason)
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
	var read *ReadError
	if !errors.As(err, &read) || strings.Contains(err.Error(), "boom") || errors.Unwrap(err) == nil {
		t.Errorf("a read failure = %v, want a ReadError with a fixed message and a cause", err)
	}
}

// TestNullCollectionsInTheBodyAreRefused: a null list in the JSON is the
// document's never-null invariant broken, whichever list it is.
func TestNullCollectionsInTheBodyAreRefused(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	body, _ := json.Marshal(full)
	for _, key := range []string{"machines", "repositories", "worktrees", "pull_requests", "agents"} {
		var generic map[string]any
		if err := json.Unmarshal(body, &generic); err != nil {
			t.Fatal(err)
		}
		generic["fleet"].(map[string]any)[key] = nil
		mutated, _ := json.Marshal(generic)
		if reason := refusalOf(t, errOf(DecodeEnvelope(bytes.NewReader(mutated), false, now))); !strings.Contains(reason, "null collection") {
			t.Errorf("%s: reason = %q", key, reason)
		}
	}
	var generic map[string]any
	_ = json.Unmarshal(body, &generic)
	generic["metrics"].(map[string]any)["samples"] = nil
	mutated, _ := json.Marshal(generic)
	if reason := refusalOf(t, errOf(DecodeEnvelope(bytes.NewReader(mutated), false, now))); !strings.Contains(reason, "samples is null") {
		t.Errorf("samples: reason = %q", reason)
	}
}

func errOf(_ Envelope, err error) error { return err }

// TestRefusalsNeverCarryTheRemotesText places a hostile token in a field name, a
// time, a number, a string value and a type-mismatched value, and requires that
// no refusal contains it.
func TestRefusalsNeverCarryTheRemotesText(t *testing.T) {
	t.Parallel()
	full, now := exportedEnvelope(t, false)
	body, _ := json.Marshal(full)
	valid := string(body)
	const token = "HOSTILE-TOKEN-7731"
	cases := map[string]string{
		"a field name":        strings.Replace(valid, `{`, `{"`+token+`":1,`, 1),
		"a field name nested": strings.Replace(valid, `"metrics":{`, `"metrics":{"`+token+`":1,`, 1),
		"a time string":       strings.Replace(valid, `"exported_at":"`, `"exported_at":"`+token, 1),
		"a number literal":    strings.Replace(valid, `"schema_version":1`, `"schema_version":1`+token, 1),
		"a typed number":      strings.Replace(valid, `"schema_version":1`, `"schema_version":"`+token+`"`, 1),
		"a huge number":       strings.Replace(valid, `"repositories_total":`, `"x`+token+`":`, 1),
		"a string value":      strings.Replace(valid, `"task-a"`, `"`+token+`\u0000"`, 1),
		"a vocabulary value":  strings.Replace(valid, `"kind":"run"`, `"kind":"`+token+`"`, 1),
		"trailing text":       valid + token,
	}
	for name, hostile := range cases {
		_, err := DecodeEnvelope(strings.NewReader(hostile), false, now)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("%s: the refusal carries the remote's text: %q", name, err)
		}
	}
	// A typed mismatch names the field, from the document's own names.
	_, err := DecodeEnvelope(strings.NewReader(cases["a typed number"]), false, now)
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("err = %q, want the field named", err)
	}
}

// TestShapeScanRefusesHostileBodiesBeforeDecoding feeds bodies built to cost far
// more to decode than their size, and requires a refusal that allocates little.
func TestShapeScanRefusesHostileBodiesBeforeDecoding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	array := func(prefix, element string, count int, suffix string) string {
		return prefix + strings.TrimSuffix(strings.Repeat(element+",", count), ",") + suffix
	}
	keys := strings.Repeat(`"a":1,`, 600_000)
	cases := map[string]struct{ body, reason string }{
		"worktrees over 5000":       {array(`{"fleet":{"worktrees":[`, `{}`, 5001, `]}}`), "more than 5000"},
		"agents over 200":           {array(`{"fleet":{"agents":[`, `{}`, 201, `]}}`), "more than 200"},
		"machines over 1":           {array(`{"fleet":{"machines":[`, `{}`, 2, `]}}`), "more than 1 "},
		"samples over 360":          {array(`{"metrics":{"samples":[`, `{}`, 361, `]}}`), "more than 360"},
		"kinds over 32":             {array(`{"fleet":{"repositories":[{"code_index":[{"statistics":{"kinds":[`, `{}`, 33, `]}}]}]}}`), "more than 32"},
		"code indexes over 32":      {array(`{"fleet":{"worktrees":[{"code_index":[`, `{}`, 33, `]}]}}`), "more than 32"},
		"unknown arrays over 5000":  {array(`{"x":[`, `1`, 5001, `]}`), "more than 5000"},
		"nesting past the depth":    {strings.Repeat(`[`, maxDepth+1) + strings.Repeat(`]`, maxDepth+1), "nested deeper"},
		"nested objects past depth": {strings.Repeat(`{"a":`, maxDepth+1) + `1` + strings.Repeat(`}`, maxDepth+1), "nested deeper"},
		"too many tokens":           {`{"fleet":{"worktrees":[{` + keys + `"a":1}]}}`, "JSON tokens"},
		"an array of empty arrays":  {array(`{"fleet":{"worktrees":[`, `[]`, 5001, `]}}`), "more than 5000"},
		"truncated inside an array": {`{"fleet":{"worktrees":[{},`, "not valid JSON"},
		"a bare scalar":             {`5`, ""},
	}
	for name, test := range cases {
		err := scanShape([]byte(test.body))
		if test.reason == "" {
			if err != nil {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if reason := refusalOf(t, err); !strings.Contains(reason, test.reason) {
			t.Errorf("%s: reason = %q, want %q", name, reason, test.reason)
		}
		// And through the whole decoder, which must not get past the scan.
		if _, err := DecodeEnvelope(strings.NewReader(test.body), false, now); err == nil {
			t.Errorf("%s: DecodeEnvelope accepted it", name)
		}
	}
	for name, body := range map[string]string{
		"an object":           `{"a":{"b":[1,2,{"c":[]}]},"d":"e"}`,
		"a nested empty":      `{"a":{},"b":[[],[{}]]}`,
		"an array of strings": `["a","b"]`,
	} {
		if err := scanShape([]byte(body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestHostileBodyOfEmptyObjectsAllocatesLittle is the memory ceiling: a body of
// about 8 MiB made of empty objects (millions of them) must be refused at the
// array's cap, long before a decoder would allocate a struct for each. The test
// reads the process-wide allocation counter, so it runs alone.
func TestHostileBodyOfEmptyObjectsAllocatesLittle(t *testing.T) { //nolint:paralleltest // it measures process-wide allocation
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	prefix, suffix := `{"fleet":{"worktrees":[`, `]}}`
	body := prefix + strings.Repeat(`{},`, (MaxEnvelopeBytes-len(prefix)-len(suffix))/3-1) + `{}` + suffix
	if len(body) > MaxEnvelopeBytes || len(body) < MaxEnvelopeBytes-16 {
		t.Fatalf("the hostile body is %d bytes, want just under %d", len(body), MaxEnvelopeBytes)
	}
	deep := strings.Repeat(`[`, MaxEnvelopeBytes-1)
	var before, after runtime.MemStats
	for name, hostile := range map[string]string{"empty objects": body, "deep nesting": deep} {
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := DecodeEnvelope(strings.NewReader(hostile), false, now)
		runtime.ReadMemStats(&after)
		refusalOf(t, err)
		// Reading the body once is 8 MiB and the scan holds little more; decoding
		// millions of structs would cost hundreds of times that.
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
			t.Errorf("%s: refusing the body allocated %d MiB, want under 64", name, allocated>>20)
		}
	}
}

// TestEveryStringFieldHasARule fails closed on the document: every string field
// of every type the envelope can hold has a rule, every rule names a real field
// (but for the ones kept for the pull request state task, which must be removed
// from rulesForUnmergedFields when their fields arrive), and no unmerged rule
// names a field that exists. A new string field fails this test until it has a
// rule.
func TestEveryStringFieldHasARule(t *testing.T) {
	t.Parallel()
	fields := map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(kind reflect.Type) {
		for kind.Kind() == reflect.Pointer || kind.Kind() == reflect.Slice {
			kind = kind.Elem()
		}
		if kind.Kind() != reflect.Struct || kind == timeType || seen[kind] {
			return
		}
		seen[kind] = true
		for index := range kind.NumField() {
			field := kind.Field(index)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || name == "-" {
				continue
			}
			if field.Anonymous && name == "" {
				walk(field.Type)
				continue
			}
			element := field.Type
			for element.Kind() == reflect.Pointer || element.Kind() == reflect.Slice {
				element = element.Elem()
			}
			if element.Kind() == reflect.String {
				fields[kind.Name()+"."+name] = true
			}
			walk(field.Type)
		}
	}
	walk(reflect.TypeFor[Envelope]())
	walk(reflect.TypeFor[machinemetrics.Sample]())
	if len(fields) < 30 {
		t.Fatalf("only %d string fields found: the enumeration is broken", len(fields))
	}
	for field := range fields {
		if _, ruled := stringRules[field]; !ruled {
			t.Errorf("the string field %s has no rule in stringRules: add one (and refuse, not accept, what you do not know)", field)
		}
	}
	for field := range stringRules {
		switch {
		case rulesForUnmergedFields[field] && fields[field]:
			t.Errorf("%s now exists: remove it from rulesForUnmergedFields", field)
		case !rulesForUnmergedFields[field] && !fields[field]:
			t.Errorf("the rule %s names no string field", field)
		}
	}
}

// TestAStringWithNoRuleIsRefused is the fail-closed behaviour itself.
func TestAStringWithNoRuleIsRefused(t *testing.T) {
	t.Parallel()
	type unruled struct {
		Note string `json:"note"`
	}
	value := unruled{Note: "fine"}
	if reason := refusalOf(t, checkValue(reflectOf(&value), "root", "", "", time.Now())); !strings.Contains(reason, "has no rule") {
		t.Errorf("reason = %q", reason)
	}
}

// TestStringRulesAcceptTheirOwnVocabularyAndRefuseTheRest is every closed
// vocabulary, from the constants that define it.
func TestStringRulesAcceptTheirOwnVocabularyAndRefuseTheRest(t *testing.T) {
	t.Parallel()
	good := map[string][]string{
		"Worktree.lifecycle":           remoteLifecycles,
		"Worktree.owner_state":         {OwnerActive, OwnerIdle, OwnerOrphaned, OwnerUnknown},
		"CodeIndex.state":              {CodeIndexFresh, CodeIndexStale, CodeIndexDiverged, CodeIndexPending, CodeIndexFailed, CodeIndexNever},
		"CodeStatistics.error":         {ErrorProviderUnavailable, ErrorProviderTimeout, ErrorProviderFailed, ErrorProviderOutput},
		"Repository.error":             {ErrorTimeout, ErrorReadFailed},
		"Document.error":               {ErrorRepositoriesUnreadable},
		"PullRequest.state":            {"open", "merged", "closed", "draft"},
		"PullRequest.mergeable":        mergeableStatesForRules,
		"Agent.kind":                   {AgentSession, AgentRun},
		"Agent.state":                  {"live", "parked", "running", "completed", "failed", "timeout", "abandoned"},
		"EnvelopeMetrics.reason":       {ReasonNoSource, ReasonUnsupported, ReasonUnavailable},
		"EnvelopeMetrics.route":        {RouteLocal, RouteNone},
		"Entry.route":                  {RouteLocal},
		"Agent.runtime":                {"claude", "codex", "gpt-5.3"},
		"Agent.session_id":             {"wbs-1"},
		"Agent.run_id":                 {"agt-1"},
		"Agent.model":                  {"opus", "claude-opus-4[1m]", "gpt/5@x+y"},
		"Machine.wb_version":           {"v0.2.0", "(devel)", "v1.2.3-0.20260101+dirty"},
		"Machine.os":                   {"linux", "darwin"},
		"Machine.arch":                 {"arm64", "amd64"},
		"KindCount.kind":               {"func", "type-alias", "a_b"},
		"CodeIndex.indexer":            {"codegrapher", "a.b:c"},
		"Document.code_index_provider": {"codegrapher"},
	}
	for key, values := range good {
		rule, ok := stringRules[key]
		if !ok {
			t.Fatalf("no rule %s", key)
		}
		for _, value := range values {
			if !rule(value) {
				t.Errorf("%s refuses %q", key, value)
			}
		}
		for _, value := range []string{"a b", "../x", "x\x00", strings.Repeat("v", 300), "\u00fcn\u00efcode", "<script>", "UPPER CASE"} {
			if rule(value) {
				t.Errorf("%s accepts %q", key, value)
			}
		}
	}
	for _, key := range []string{"Worktree.lifecycle", "Worktree.owner_state", "CodeIndex.state", "CodeStatistics.error", "Repository.error", "Document.error", "PullRequest.state", "PullRequest.mergeable", "Agent.kind", "Agent.state", "EnvelopeMetrics.reason", "EnvelopeMetrics.route", "Entry.route"} {
		if stringRules[key]("nope") {
			t.Errorf("the vocabulary %s accepts a word outside it", key)
		}
	}
	if !stringRules["Worktree.task"]("任务 é") {
		t.Error("a name with ordinary unicode letters is refused")
	}
}

// mergeableStatesForRules are the values the pull request state task's
// observation gives mergeable (its mergeableStates).
var mergeableStatesForRules = []string{"clean", "blocked", "dirty", "behind", "unstable", "has_hooks", "draft", "unknown"}

type selfDecoding struct{ Value string }

func (*selfDecoding) UnmarshalJSON([]byte) error { return nil }

type textDecoding struct{ Value int }

func (*textDecoding) UnmarshalText([]byte) error { return nil }

type EmbeddedPointer struct{ Count int }

type WithEmbedded struct {
	*EmbeddedPointer
	Own int `json:"own"`
}

type walkKinds struct {
	Count   uint16 `json:"count"`
	On      bool   `json:"on"`
	Score32 float32
	hidden  string
	Skipped string `json:"-"`
	Nest    struct{ Inner int }
	Maybe   *int `json:"maybe"`
}

func TestCheckValueRefusesWhatTheDocumentNeverHolds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for name, value := range map[string]any{
		"a byte slice":                     &struct{ Data []byte }{Data: []byte("x")},
		"a raw message":                    &struct{ Data json.RawMessage }{Data: json.RawMessage(`{}`)},
		"a map":                            &struct{ Labels map[string]int }{Labels: map[string]int{"a": 1}},
		"an interface":                     &struct{ Any any }{Any: 1},
		"a channel":                        &struct{ C chan int }{},
		"a type that decodes JSON":         &struct{ S selfDecoding }{},
		"a type that decodes text":         &struct{ S textDecoding }{},
		"a pointer to such a type":         &struct{ S *selfDecoding }{S: &selfDecoding{}},
		"a list of such types":             &struct{ S []selfDecoding }{S: []selfDecoding{{}}},
		"a float over its range":           &struct{ F float64 }{F: 1e12},
		"an int over its range":            &struct{ N int }{N: maxCount + 1},
		"a NaN":                            &struct{ F float32 }{F: float32(math.NaN())},
		"a time in the future":             &struct{ T time.Time }{T: now.Add(time.Hour)},
		"a time before 2000":               &struct{ T time.Time }{T: time.Unix(1, 0)},
		"a negative in an embedded struct": &struct{ WithEmbedded }{WithEmbedded{EmbeddedPointer: &EmbeddedPointer{Count: -1}}},
	} {
		if err := checkValue(reflect.ValueOf(value).Elem(), "root", "", "", now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	clean := walkKinds{Count: 3, On: true, Score32: 1.5, hidden: "\x00", Skipped: strings.Repeat("a", 300)}
	if err := checkValue(reflectOf(&clean), "root", "", "", now); err != nil {
		t.Errorf("a clean value: %v", err)
	}
	for name, embedded := range map[string]WithEmbedded{
		"a nil embedded pointer": {Own: 1},
		"a set embedded pointer": {EmbeddedPointer: &EmbeddedPointer{Count: 2}, Own: 1},
	} {
		if err := checkValue(reflectOf(&embedded), "root", "", "", now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	number := 4
	clean.Maybe = &number
	if err := checkValue(reflectOf(&clean), "root", "", "", now); err != nil {
		t.Errorf("a pointer to a number: %v", err)
	}
}

func reflectOf(pointer any) reflect.Value { return reflect.ValueOf(pointer).Elem() }
