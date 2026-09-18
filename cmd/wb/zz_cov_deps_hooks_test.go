package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

// cwDepsHooksReportFixture is a healthy report exercising every rendered
// section: explicit policy paths, automatic profiles with an active and an
// excluded profile, hook blocks, and a local metrics path.
func cwDepsHooksReportFixture() hooks.CheckReport {
	return hooks.CheckReport{
		RepoRoot:         "/tmp/cw-deps/repo",
		ManagedPath:      "/tmp/cw-deps/repo/.git/wb/hooks",
		ConfigPaths:      []string{"/tmp/cw-deps/repo/.wb-hooks.yaml"},
		Hooks:            []string{"pre-commit", "pre-push"},
		ProfilesAuto:     true,
		ActiveProfiles:   []hooks.ActiveProfile{{Name: "worktree", Reason: "a WB worktree"}},
		ExcludedProfiles: []string{"node"},
		HookBlocks:       map[string][]string{"pre-commit": {"format", "lint"}},
		MetricsPath:      "/tmp/cw-deps/repo/.git/wb/metrics.jsonl",
	}
}

func TestCwDepsPrintHooksCheckRendersHealthyAndUnhealthyReports(t *testing.T) {
	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	if err := printHooksCheck(command, cwDepsHooksReportFixture()); err != nil {
		t.Fatalf("healthy report: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"/tmp/cw-deps/repo",
		"✓ policy /tmp/cw-deps/repo/.wb-hooks.yaml",
		"✓ profile worktree (a WB worktree)",
		"! profile node (explicitly excluded by policy)",
		"✓ pre-commit blocks format, lint",
		"✓ core.hooksPath /tmp/cw-deps/repo/.git/wb/hooks",
		"✓ managed hooks pre-commit, pre-push",
		"✓ local metrics /tmp/cw-deps/repo/.git/wb/metrics.jsonl",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("healthy report missing %q:\n%s", want, text)
		}
	}

	// A report with no explicit policy path names the built-in templates, and
	// automatic profiles with nothing detected says so.
	out.Reset()
	builtin := hooks.CheckReport{RepoRoot: "/tmp/cw-deps/repo", ProfilesAuto: true, HookBlocks: map[string][]string{}}
	if err := printHooksCheck(cwDepsNewOutCommand(&out), builtin); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ conservative built-in templates", "✓ automatic profiles enabled; none detected"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("built-in report missing %q:\n%s", want, out.String())
		}
	}

	// Findings replace the healthy tail and carry their code, message, and
	// path.
	out.Reset()
	unhealthy := cwDepsHooksReportFixture()
	unhealthy.Findings = []hooks.Finding{
		{Code: "hooks-missing", Message: "pre-push shim is missing", Path: "/tmp/cw-deps/repo/.git/hooks/pre-push"},
		{Code: "hooks-drift", Message: "stale shim"},
	}
	if err := printHooksCheck(cwDepsNewOutCommand(&out), unhealthy); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"✗ hooks-missing: pre-push shim is missing (/tmp/cw-deps/repo/.git/hooks/pre-push)",
		"✗ hooks-drift: stale shim",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("findings report missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "✓ core.hooksPath") {
		t.Errorf("a report with findings still claimed a healthy core.hooksPath:\n%s", out.String())
	}
}

func TestCwDepsHooksCheckErrorNamesTheRepair(t *testing.T) {
	single := &hooksCheckError{count: 2}
	if got := single.Error(); got != "hooks check found 2 problem(s); run `wb hooks repair`" {
		t.Errorf("single-repository error = %q", got)
	}
	fleet := &hooksCheckError{count: 3, fleet: true}
	if got := fleet.Error(); got != "fleet hooks check found 3 problem(s); run `wb hooks repair --fleet`" {
		t.Errorf("fleet error = %q", got)
	}
}

func TestCwDepsMetricBarAndSmallHelpers(t *testing.T) {
	if got := metricBar(0); got != "·" {
		t.Errorf("metricBar(0) = %q", got)
	}
	if got := metricBar(-3); got != "·" {
		t.Errorf("metricBar(-3) = %q", got)
	}
	if got := metricBar(3); got != "███" {
		t.Errorf("metricBar(3) = %q", got)
	}
	if got := metricBar(1000); got != strings.Repeat("█", 20) {
		t.Errorf("metricBar clamps at 20, got %d bars", len([]rune(got)))
	}
	if got := argumentOrCurrent([]string{"/tmp/repo"}); got != "/tmp/repo" {
		t.Errorf("argumentOrCurrent(path) = %q", got)
	}
	if got := argumentOrCurrent(nil); got != "." {
		t.Errorf("argumentOrCurrent(nil) = %q", got)
	}
	if got := hookExecutable(); got == "" {
		t.Error("hookExecutable returned nothing; the running test binary is a valid executable")
	}
}

func TestCwDepsPrintHookMetricsShapes(t *testing.T) {
	summary := hooks.MetricsSummary{
		From: "2026-09-01", Through: "2026-09-14", RepositoryFilter: "acme/app",
		Commits: 4, PushAttempts: 2, CommitChecks: 4, HookFailures: 1, HookRuns: 6, AverageDurationMS: 1200,
		Days: []hooks.DailyMetrics{
			{Date: "2026-09-13", Commits: 2, PushAttempts: 1, CommitChecks: 2, HookFailures: 0},
			{Date: "2026-09-14", Commits: 2, PushAttempts: 1, CommitChecks: 2, HookFailures: 1},
		},
		Blocks: []hooks.BlockMetrics{{ID: "format", Runs: 3, Failures: 1, AverageDurationMS: 300}},
	}
	var out bytes.Buffer
	if err := printHookMetrics(cwDepsNewOutCommand(&out), summary, "/tmp/metrics.jsonl"); err != nil {
		t.Fatalf("printHookMetrics: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Local hook metrics · 2026-09-01 through 2026-09-14",
		"Repository filter:", "acme/app",
		"Totals: 4 commits · 2 push attempts · 4 commit checks · 1 failures · 6 hook runs",
		"Average hook duration: 1.2s",
		"Blocks:", "format",
		"Pushes are pre-push attempts",
		"Events: /tmp/metrics.jsonl",
		"██",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics report missing %q:\n%s", want, text)
		}
	}

	// An empty window still prints the totals and says where the events live,
	// without inventing blocks.
	out.Reset()
	if err := printHookMetrics(cwDepsNewOutCommand(&out), hooks.MetricsSummary{From: "a", Through: "b"}, "/tmp/none.jsonl"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Blocks:") || strings.Contains(out.String(), "Repository filter:") {
		t.Errorf("empty metrics report invented sections:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "·") {
		t.Errorf("empty metric bars should render the zero marker:\n%s", out.String())
	}
}

func TestCwDepsPrintHookProfileDeltaShapes(t *testing.T) {
	delta := hooks.ProfileDelta{
		From: "2026-09-01", Through: "2026-09-14",
		Commit:     hooks.ProfileCost{Runs: 2, Failures: 0, TotalDurationMS: 1000, AverageDurationMS: 500, MaxDurationMS: 700},
		StreamPush: hooks.ProfileCost{Runs: 3, Failures: 1, TotalDurationMS: 300, AverageDurationMS: 100, MaxDurationMS: 150},
		OtherPush:  hooks.ProfileCost{Runs: 1, Failures: 0, TotalDurationMS: 4000, AverageDurationMS: 4000, MaxDurationMS: 4000},
		SavedRuns:  3, SavedDurationMS: 12000, SavedBasisMS: 4000,
		Blocks:     []hooks.BlockMetrics{{ID: "lint", Runs: 2, Failures: 1, AverageDurationMS: 250}},
		Unmeasured: []string{"a push with no tier recorded"},
	}
	var out bytes.Buffer
	if err := printHookProfileDelta(cwDepsNewOutCommand(&out), delta, "/tmp/metrics.jsonl"); err != nil {
		t.Fatalf("printHookProfileDelta: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Hook profile cost · 2026-09-01 through 2026-09-14",
		"commit", "stream push", "other push",
		"3 push(es) to a stream branch ran no local verification, saving about 12000 ms at the measured 4000 ms average",
		"per-block cost:", "lint",
		"? not measured: a push with no tier recorded",
		"source: /tmp/metrics.jsonl",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("profile delta missing %q:\n%s", want, text)
		}
	}

	// With nothing priced there is no per-block section, but the zero saving
	// is still explained and nothing is silently omitted.
	out.Reset()
	if err := printHookProfileDelta(cwDepsNewOutCommand(&out), hooks.ProfileDelta{From: "a", Through: "b"}, "/tmp/none.jsonl"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "per-block cost:") {
		t.Errorf("empty delta invented a block section:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0 push(es)") {
		t.Errorf("empty delta hid the zero saving:\n%s", out.String())
	}
}

// TestCwDepsHooksInstallAndCheckInProcess drives both the single-repository
// and fleet hooks commands against scratch clones, so installation, validation,
// and the findings exit path are all real.
func TestCwDepsHooksInstallAndCheckInProcess(t *testing.T) {
	root := t.TempDir()
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	initTestRepository(t, filepath.Join(root, "acme", "other"))

	// check before install: unmanaged hooks are findings, not a crash.
	stdout, _, err := cwCovExec(t, root, newHooksCheckCmd, app)
	if _, ok := err.(*hooksCheckError); !ok {
		t.Fatalf("check before install error = %v, want a hooks-check error\n%s", err, stdout)
	}
	// --format=json still exits non-zero for findings; the envelope is what
	// matters, so the error is expected here.
	jsonOut, _, jsonErr := cwCovExec(t, root, newHooksCheckCmd, app, "--format=json")
	if _, ok := jsonErr.(*hooksCheckError); !ok {
		t.Fatalf("check --format=json error = %v\n%s", jsonErr, jsonOut)
	}
	if !json.Valid([]byte(jsonOut)) {
		t.Fatalf("check --format=json wrote no JSON envelope: %s", jsonOut)
	}
	var checked hooks.CheckReport
	if err := json.Unmarshal([]byte(jsonOut), &checked); err != nil {
		t.Fatalf("check JSON: %v\n%s", err, jsonOut)
	}
	if len(checked.Findings) == 0 {
		t.Error("an unmanaged repository must report at least one finding")
	}

	// install: the shims land and the report names the repository.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(false) }, app)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "hooks ready for") {
		t.Errorf("install report = %q", stdout)
	}
	if checked.ManagedPath == "" {
		t.Fatal("the check report named no managed hooks path")
	}
	if _, statErr := os.Stat(filepath.Join(checked.ManagedPath, "pre-commit")); statErr != nil {
		t.Errorf("install did not write the managed pre-commit shim under %s: %v", checked.ManagedPath, statErr)
	}
	// The same repository now validates clean.
	if stdout, _, err := cwCovExec(t, root, newHooksCheckCmd, app); err != nil {
		t.Fatalf("check after install: %v\n%s", err, stdout)
	}

	// A repository path with --fleet is refused rather than silently ignored.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(false) }, app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("install --fleet with a path = %v", err)
	}
	if _, _, err := cwCovExec(t, root, newHooksCheckCmd, app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("check --fleet with a path = %v", err)
	}

	// Fleet install processes every local repository and reports the count.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(false) }, "--fleet")
	if err != nil {
		t.Fatalf("fleet install: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Processed 2 repositories; 0 failed") {
		t.Errorf("fleet install report = %q", stdout)
	}
	// Fleet check is then clean, in text and JSON.
	if stdout, _, err := cwCovExec(t, root, newHooksCheckCmd, "--fleet"); err != nil {
		t.Fatalf("fleet check: %v\n%s", err, stdout)
	}
	fleetJSON, _, err := cwCovExec(t, root, newHooksCheckCmd, "--fleet", "--format=json")
	if err != nil || !json.Valid([]byte(fleetJSON)) {
		t.Fatalf("fleet check JSON = %v\n%s", err, fleetJSON)
	}
	var fleetResults []fleetHooksCheck
	if err := json.Unmarshal([]byte(fleetJSON), &fleetResults); err != nil || len(fleetResults) != 2 {
		t.Fatalf("fleet results = %+v, %v", fleetResults, err)
	}
	// The repair spelling reuses the same installer.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(true) }, app)
	if err != nil || !strings.Contains(stdout, "hooks ready for") {
		t.Fatalf("repair: %v\n%s", err, stdout)
	}

	// A hidden hook runner refuses a malformed hook name outright and a
	// well-formed but unconfigured one without executing anything.
	if _, _, err := cwCovExec(t, root, newHooksRunCmd, "../evil"); err == nil ||
		!strings.Contains(err.Error(), "invalid hook name") {
		t.Fatalf("malformed hook = %v", err)
	}
	if _, _, err := cwCovExec(t, root, newHooksRunCmd, "not-a-real-hook"); err == nil ||
		!strings.Contains(err.Error(), "disabled or not configured") {
		t.Fatalf("unconfigured hook = %v", err)
	}
}

// TestCwDepsHooksMetricsAndMeasureCommandsInProcess drives both reporting
// commands over a recorded metrics window and over an absent one.
func TestCwDepsHooksMetricsAndMeasureCommandsInProcess(t *testing.T) {
	root := t.TempDir()
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	metricsFile := filepath.Join(t.TempDir(), "metrics.jsonl")
	now := time.Now().UTC()
	events := []hooks.Event{
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now.Add(-2 * time.Hour), Repository: "acme/app",
			Hook: "post-commit", Action: "commit", Outcome: "passed", DurationMS: 120, OS: "linux", Arch: "amd64"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now.Add(-1 * time.Hour), Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "failed", DurationMS: 900, Ref: "refs/heads/main", OS: "linux", Arch: "amd64"},
	}
	var lines bytes.Buffer
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(metricsFile, lines.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", metricsFile, "--days", "30", "--repo", "acme")
	if err != nil {
		t.Fatalf("hooks metrics: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Local hook metrics", "Totals:", "Events:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("metrics output missing %q:\n%s", want, stdout)
		}
	}
	jsonOut, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", metricsFile, "--format=json")
	if err != nil || !json.Valid([]byte(jsonOut)) {
		t.Fatalf("metrics JSON = %v\n%s", err, jsonOut)
	}
	stdout, _, err = cwCovExec(t, root, newHooksMeasureCmd, app, "--file", metricsFile, "--days", "30", "--repo", "acme")
	if err != nil {
		t.Fatalf("hooks measure: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Hook profile cost", "commit", "stream push", "other push", "source:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("measure output missing %q:\n%s", want, stdout)
		}
	}
	measureJSON, _, err := cwCovExec(t, root, newHooksMeasureCmd, app, "--file", metricsFile, "--format=json")
	if err != nil || !json.Valid([]byte(measureJSON)) {
		t.Fatalf("measure JSON = %v\n%s", err, measureJSON)
	}
	// A missing events file is an empty window, not a failure.
	absent := filepath.Join(t.TempDir(), "absent.jsonl")
	if stdout, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", absent); err != nil {
		t.Fatalf("metrics over an absent file: %v\n%s", err, stdout)
	}
	if stdout, _, err := cwCovExec(t, root, newHooksMeasureCmd, app, "--file", absent); err != nil {
		t.Fatalf("measure over an absent file: %v\n%s", err, stdout)
	}
	// A malformed line is refused rather than silently ignored.
	broken := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(broken, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", broken); err == nil {
		t.Fatal("a malformed metrics line must be refused")
	}
}
