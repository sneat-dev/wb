package cmdstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/spf13/cobra"
)

func TestParseLibraryTargets(t *testing.T) {
	t.Parallel()
	parsed, err := parseLibraryTargets([]string{"github.com/acme/library/backend@v0.6.0", "@acme/package@1.2.3", "lodash@4.17.21"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed = %+v, want three libraries", parsed)
	}
	if parsed[0].Name != "github.com/acme/library/backend" || parsed[0].Target != "v0.6.0" ||
		parsed[0].Ecosystem != string(streams.EcosystemGo) {
		t.Errorf("go library = %+v", parsed[0])
	}
	// A scoped npm package starts with @, so the separator is the LAST @.
	if parsed[1].Name != "@acme/package" || parsed[1].Target != "1.2.3" ||
		parsed[1].Ecosystem != string(streams.EcosystemNpm) {
		t.Errorf("scoped npm library = %+v", parsed[1])
	}
	if parsed[2].Name != "lodash" || parsed[2].Ecosystem != string(streams.EcosystemNpm) {
		t.Errorf("bare npm library = %+v", parsed[2])
	}

	for _, bad := range []string{"no-version", "@1.2.3", "name@", "@"} {
		if _, err := parseLibraryTargets([]string{bad}); err == nil || !strings.Contains(err.Error(), "must be <name>@<version>") {
			t.Errorf("parseLibraryTargets(%q) error = %v, want a named usage error", bad, err)
		}
	}
	if targets, err := parseLibraryTargets(nil); err != nil || len(targets) != 0 {
		t.Fatalf("empty input = (%+v, %v)", targets, err)
	}
}

func TestPrintStreamSyncRendersEveryRowShape(t *testing.T) {
	t.Parallel()
	results := []streamsync.Result{
		{
			Stream: "checkout-rewrite", Repository: "acme/app",
			RecordedRemoteHead: "remote-head", RemoteAdvanced: true,
			StreamRebase: streamsync.RebaseResult{Branch: "stream/checkout-rewrite", Rebased: true},
			AgentRebases: []streamsync.RebaseResult{
				{Branch: "agent/one", Agent: "lane-1", Rebased: true},
				{Branch: "agent/two", Agent: "lane-2", Conflicts: []string{"pkg/a.go"}},
				{Branch: "agent/three", Agent: "lane-3", Detail: "could not rebase"},
			},
			Bumps: []streamsync.BumpResult{{
				Action: "bumped", Library: streamsync.Library{Name: "github.com/acme/lib", Target: "v1.2.3"},
			}},
			Batch: &streamsync.BatchResult{
				Passed: true, Runs: 2,
				Elements: []streamsync.Element{{Name: "a"}, {Name: "b"}},
				Skipped:  []string{"-race"}, Unguarded: []string{"e2e"}, Unverified: []string{"fuzz"},
			},
			Unpushed:    streamsync.UnpushedReport{Repository: "acme/app", Branch: "stream/x", Commits: 2},
			PushSkipped: "push not justified by a named trigger",
			Push:        &streamsync.PushDecision{SHA: "abc123", Trigger: streamsync.TriggerExplicit, Reason: "hand-off"},
			Errors:      []string{"bump failed"},
			BaseBefore:  "before",
			BaseAfter:   "after",
		},
	}

	command := newStreamSyncCmd(testRuntime(), testDependencies())
	var out bytes.Buffer
	command.SetOut(&out)
	// The rich result carries a conflicting agent rebase, so reporting it is a
	// findings exit; the rendering is what this assertion is about.
	if err := printStreamSync(testRuntime(), command, "text", results); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("rich result error = %v, want findings", err)
	}
	text := out.String()
	for _, want := range []string{
		"acme/app on stream/checkout-rewrite",
		"fast-forwarded to fetched remote head remote-head",
		"rebased", "CONFLICT: pkg/a.go", "failed: could not rebase",
		"bumped", "github.com/acme/lib", "v1.2.3",
		"batch passed in 2 run(s) over 2 element(s)",
		"skipped: -race", "UNGUARDED: e2e", "UNVERIFIED: fuzz",
		"pushed abc123: trigger=", "! bump failed",
		"push not justified by a named trigger",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stream sync text missing %q:\n%s", want, text)
		}
	}

	// A failed result must turn into exit findings, not a silent success.
	failing := []streamsync.Result{{StreamRebase: streamsync.RebaseResult{Branch: "stream/x"}}}
	if err := printStreamSync(testRuntime(), command, "text", failing); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("un-rebased stream did not report findings: %v", err)
	}
	// A clean result is a success.
	clean := []streamsync.Result{{
		Repository:   "acme/app",
		StreamRebase: streamsync.RebaseResult{Branch: "stream/ok", Rebased: true},
		Unpushed:     streamsync.UnpushedReport{Repository: "acme/app"},
	}}
	out.Reset()
	if err := printStreamSync(testRuntime(), command, "text", clean); err != nil {
		t.Fatalf("clean stream reported an error: %v", err)
	}
	if !strings.Contains(out.String(), "nothing unpushed") {
		t.Errorf("clean stream output = %q", out.String())
	}

	out.Reset()
	if err := printStreamSync(testRuntime(), command, "json", results); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("json rich result error = %v, want findings", err)
	}
	var decoded []streamsync.Result
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("stream sync JSON: %v\n%s", err, out.String())
	}
	if len(decoded) != 1 || decoded[0].Repository != "acme/app" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestPrintBatchRendersCulpritAndScanLimit(t *testing.T) {
	t.Parallel()
	command := newStreamSyncCmd(testRuntime(), testDependencies())
	var out bytes.Buffer
	command.SetOut(&out)
	batch := streamsync.BatchResult{
		Passed: false, Runs: 3,
		Elements:           []streamsync.Element{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Culprit:            &streamsync.Element{Name: "b", SHA: "sha-b"},
		ProvenGood:         []streamsync.Element{{Name: "a"}},
		FailingCheck:       "test",
		Skipped:            []string{"-race"},
		Unguarded:          []string{"e2e"},
		Unverified:         []string{"fuzz"},
		UnexaminedElements: 1,
	}
	if err := printBatch(&out, batch); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"batch FAILED in 3 run(s) over 3 element(s)",
		"culprit: b (test)", "proven good: 1 element(s) before it",
		"skipped: -race", "UNGUARDED: e2e", "UNVERIFIED: fuzz",
		"never examined",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("batch text missing %q:\n%s", want, text)
		}
	}

	// The interaction-failure path states that no element was the cause.
	out.Reset()
	if err := printBatch(&out, streamsync.BatchResult{
		Passed: false, Runs: 3, Elements: []streamsync.Element{{Name: "a"}},
		InteractionFailure: true, FailingCheck: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "interaction failure") {
		t.Errorf("interaction failure not reported:\n%s", out.String())
	}
}

func TestStreamSyncCommandUsageRefusals(t *testing.T) {
	t.Parallel()
	root := "/fixture"

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testRuntime(), testDependencies()) }, "cw-cov", "--library", "no-version")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("bad --library exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(err.Error(), "must be <name>@<version>") {
		t.Errorf("bad --library error = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testRuntime(), testDependencies()) }, "cw-cov", "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), `unsupported format "toml"`) {
		t.Fatalf("bad --format error = %v", err)
	}
	// A stream that does not exist is an error, never a silent no-op.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamSyncCmd(testRuntime(), testDependencies()) }, "absent-stream"); err == nil {
		t.Fatal("syncing an unknown stream must fail")
	}
}
