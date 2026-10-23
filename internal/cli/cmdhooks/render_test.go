package cmdhooks

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbexec"
)

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
func TestMetricBar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value int
		want  string
	}{
		{value: 0, want: "·"},
		{value: 3, want: "███"},
		{value: 99, want: strings.Repeat("█", 20)},
	} {
		if got := metricBar(test.value); got != test.want {
			t.Fatalf("metricBar(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}
func TestPrintHooksCheckShowsExplicitProfileExclusion(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	command := &output
	if err := printHooksCheckDetails(command, hooks.CheckReport{
		ManagedPath:      "/managed/hooks",
		ExcludedProfiles: []string{"worktree"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "! profile worktree (explicitly excluded by policy)") {
		t.Fatalf("hooks check hid explicit safety exception:\n%s", got)
	}
}
func TestPrintHookMetricsExplainsPushAttempts(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	cmd := &output
	if err := printHookMetrics(cmd, hooks.MetricsSummary{
		From:         "2026-07-19",
		Through:      "2026-07-20",
		Commits:      2,
		PushAttempts: 1,
		HookRuns:     3,
		Blocks: []hooks.BlockMetrics{
			{ID: "go/pre-push", Profile: "go", Hook: "pre-push", Runs: 2, AverageDurationMS: 1250},
		},
		Days: []hooks.DailyMetrics{
			{Date: "2026-07-19", Commits: 2},
			{Date: "2026-07-20", PushAttempts: 1},
		},
	}, "/tmp/events.jsonl"); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, wanted := range []string{"2 commits", "1 push attempts", "go/pre-push", "average 1.25s", "Git has no post-push hook", "/tmp/events.jsonl"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("output missing %q:\n%s", wanted, got)
		}
	}
}
func TestCwDepsPrintHooksCheckRendersHealthyAndUnhealthyReports(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	command := &out
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
	if err := printHooksCheck(&out, builtin); err != nil {
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
	if err := printHooksCheck(&out, unhealthy); err != nil {
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
	t.Parallel()
	single := &CheckError{Count: 2}
	if got := single.Error(); got != "hooks check found 2 problem(s); run `wb hooks repair`" {
		t.Errorf("single-repository error = %q", got)
	}
	fleet := &CheckError{Count: 3, Fleet: true}
	if got := fleet.Error(); got != "fleet hooks check found 3 problem(s); run `wb hooks repair --fleet`" {
		t.Errorf("fleet error = %q", got)
	}
}
func TestCwDepsPrintHookMetricsShapes(t *testing.T) {
	t.Parallel()
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
	if err := printHookMetrics(&out, summary, "/tmp/metrics.jsonl"); err != nil {
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
	if err := printHookMetrics(&out, hooks.MetricsSummary{From: "a", Through: "b"}, "/tmp/none.jsonl"); err != nil {
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
	t.Parallel()
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
	if err := printHookProfileDelta(&out, delta, "/tmp/metrics.jsonl"); err != nil {
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
	if err := printHookProfileDelta(&out, hooks.ProfileDelta{From: "a", Through: "b"}, "/tmp/none.jsonl"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "per-block cost:") {
		t.Errorf("empty delta invented a block section:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0 push(es)") {
		t.Errorf("empty delta hid the zero saving:\n%s", out.String())
	}
}
func TestCwDepsMetricBarAndSmallHelpers(t *testing.T) {
	t.Parallel()
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
}
func TestLifecycleShortSHATruncatesTo12Chars(t *testing.T) {
	t.Parallel()
	full := "abcdef0123456789"
	got := lifecycleShortSHA(full)
	if got != full[:12] {
		t.Fatalf("lifecycleShortSHA(%q) = %q, want %q", full, got, full[:12])
	}
	if len(got) != 12 {
		t.Fatalf("lifecycleShortSHA(%q) returned length %d, want 12", full, len(got))
	}
}
func TestAgentHookShellCommandForcesExitZero(t *testing.T) {
	t.Parallel()
	command := agentHookShellCommand("/opt/homebrew/bin/wb", wbexec.QuoteShellWord)
	for _, expected := range []string{"/opt/homebrew/bin/wb", agentHookInvocation, "2>/dev/null", "exit 0"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("the hook command is missing %q: %s", expected, command)
		}
	}
	if quoted := agentHookShellCommand("/path with spaces/wb", wbexec.QuoteShellWord); !strings.Contains(quoted, `'/path with spaces/wb'`) {
		t.Fatalf("an executable path with spaces was not quoted: %s", quoted)
	}
}
