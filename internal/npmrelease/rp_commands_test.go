package npmrelease

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
)

// rpCovFakeCommands installs `gh` and `npm` shims on PATH, backed by fixture
// files, so the real command boundary (OSCommandRunner) and the shared GitHub
// observer can be exercised without a network, credentials, or a real
// registry. Every invocation is appended to calls.log so the test can assert
// the exact commands WB issued.
type rpCovFakeCommands struct {
	t        *testing.T
	fixtures string
}

const rpCovGhShim = `#!/bin/sh
set -u
printf '%s %s\n' "gh" "$*" >> "$RPCOV_FIXTURES/calls.log"
case "${1:-}" in
  api)
    if [ -f "$RPCOV_FIXTURES/api.error" ]; then
      cat "$RPCOV_FIXTURES/api.error" >&2
      exit 1
    fi
    cat "$RPCOV_FIXTURES/api.body"
    ;;
  run)
    case "${2:-}" in
      list)
        if [ -f "$RPCOV_FIXTURES/list.error" ]; then
          cat "$RPCOV_FIXTURES/list.error" >&2
          exit 1
        fi
        if [ -f "$RPCOV_FIXTURES/list.once" ]; then
          rm -f "$RPCOV_FIXTURES/list.once"
          cat "$RPCOV_FIXTURES/run-list-first.json"
        else
          cat "$RPCOV_FIXTURES/run-list.json"
        fi
        ;;
      view)
        if [ -f "$RPCOV_FIXTURES/view.error" ]; then
          cat "$RPCOV_FIXTURES/view.error" >&2
          exit 1
        fi
        cat "$RPCOV_FIXTURES/run-view.json"
        ;;
      *)
        echo "unexpected gh run $*" >&2
        exit 3
        ;;
    esac
    ;;
  workflow)
    if [ -f "$RPCOV_FIXTURES/workflow.error" ]; then
      cat "$RPCOV_FIXTURES/workflow.error" >&2
      exit 1
    fi
    ;;
  *)
    echo "unexpected gh $*" >&2
    exit 3
    ;;
esac
`

const rpCovNpmShim = `#!/bin/sh
set -u
printf '%s %s\n' "npm" "$*" >> "$RPCOV_FIXTURES/calls.log"
if [ -f "$RPCOV_FIXTURES/npm.error" ]; then
  cat "$RPCOV_FIXTURES/npm.error" >&2
  exit 1
fi
cat "$RPCOV_FIXTURES/npm.output"
`

func rpCovInstallFakeCommands(t *testing.T) *rpCovFakeCommands {
	t.Helper()
	bin := t.TempDir()
	fixtures := filepath.Join(bin, "fixtures")
	if err := os.MkdirAll(fixtures, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"gh": rpCovGhShim, "npm": rpCovNpmShim} {
		if err := testenv.WriteExecutableFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RPCOV_FIXTURES", fixtures)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return &rpCovFakeCommands{t: t, fixtures: fixtures}
}

func (f *rpCovFakeCommands) write(name, contents string) string {
	f.t.Helper()
	path := filepath.Join(f.fixtures, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *rpCovFakeCommands) calls() string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.fixtures, "calls.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		f.t.Fatal(err)
	}
	return string(raw)
}

// rpCovHTTPBody wraps a JSON body in the --include envelope `gh api` emits.
func rpCovHTTPBody(body string) string {
	return "HTTP/2.0 200 OK\ncontent-type: application/json\n\n" + body + "\n"
}

func TestRPCovOSCommandRunnerPropagatesOutputExitCodesAndLaunchFailures(t *testing.T) {
	runnertest.AllowRealProcess(t)
	if result := (OSCommandRunner{}).Run(context.Background(), ""); result.Code != 2 || result.Err == nil {
		t.Fatalf("empty command result = %+v, want the usage refusal", result)
	}

	dir := t.TempDir()
	ok := filepath.Join(dir, "ok-tool")
	if err := testenv.WriteExecutableFile(ok, []byte("#!/bin/sh\necho out-$1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := OSCommandRunner{}.Run(context.Background(), dir, "./ok-tool", "hello")
	if result.Err != nil || result.Code != 0 || result.Output != "out-hello\n" {
		t.Fatalf("successful command result = %+v", result)
	}

	failing := filepath.Join(dir, "failing-tool")
	if err := testenv.WriteExecutableFile(failing, []byte("#!/bin/sh\necho bad >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result = OSCommandRunner{}.Run(context.Background(), dir, "./failing-tool")
	if result.Err == nil || result.Code != 7 || !strings.Contains(result.Output, "bad") {
		t.Fatalf("failing command result = %+v, want exit 7 with combined output", result)
	}

	if result := (OSCommandRunner{}).Run(context.Background(), dir, "./no-such-tool"); result.Err == nil || result.Code != 1 {
		t.Fatalf("missing executable result = %+v, want code 1", result)
	}
}

func TestRPCovResolveHeadThroughTheSharedObserver(t *testing.T) {
	runnertest.AllowRealProcess(t)
	fake := rpCovInstallFakeCommands(t)
	receipt := Receipt{Release: testRelease()}

	fake.write("api.body", rpCovHTTPBody(`{"object":{"sha":"`+releaseHead+`"}}`))
	sha, err := resolveHead(context.Background(), receipt, Options{Runner: OSCommandRunner{}})
	if err != nil || sha != releaseHead {
		t.Fatalf("resolveHead = %q, err=%v", sha, err)
	}
	if !strings.Contains(fake.calls(), "gh api repos/sneat-co/assetus/git/ref/heads/main --include") {
		t.Fatalf("calls = %q, want the exact ref endpoint", fake.calls())
	}

	// A GitHub failure must surface as a resolution failure, not a silent
	// empty head.
	fake.write("api.error", "gh: Not Found (HTTP 404)\n")
	if _, err := resolveHead(context.Background(), receipt, Options{Runner: OSCommandRunner{}}); err == nil ||
		!strings.Contains(err.Error(), "resolve workflow head") {
		t.Fatalf("observer failure error = %v", err)
	}
	if err := os.Remove(filepath.Join(fake.fixtures, "api.error")); err != nil {
		t.Fatal(err)
	}

	fake.write("api.body", rpCovHTTPBody("not-json"))
	if _, err := resolveHead(context.Background(), receipt, Options{Runner: OSCommandRunner{}}); err == nil ||
		!strings.Contains(err.Error(), "decode workflow head") {
		t.Fatalf("observer decode error = %v", err)
	}

	fake.write("api.body", rpCovHTTPBody(`{"object":{"sha":"zzz"}}`))
	if _, err := resolveHead(context.Background(), receipt, Options{Runner: OSCommandRunner{}}); err == nil ||
		!strings.Contains(err.Error(), "invalid head SHA") {
		t.Fatalf("observer invalid-sha error = %v", err)
	}
}

func TestRPCovResolveHeadRejectsAnInvalidExternalSHAResponse(t *testing.T) {
	t.Parallel()
	runner := &rpCovCommandRunner{steps: []CommandResult{{Output: "not-a-sha\n"}}}
	if _, err := resolveHead(context.Background(), Receipt{Release: testRelease()}, Options{Runner: runner}); err == nil ||
		!strings.Contains(err.Error(), "invalid head SHA") {
		t.Fatalf("error = %v, want the invalid-sha refusal", err)
	}
}

func TestRPCovListExactWorkflowRunsThroughTheSharedObserver(t *testing.T) {
	runnertest.AllowRealProcess(t)
	fake := rpCovInstallFakeCommands(t)
	receipt := Receipt{Release: testRelease(), HeadSHA: releaseHead}
	run := workflowRunFixture("123", "completed", "success", time.Now().UTC())
	fake.write("run-list.json", workflowRunList(run))

	runs, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: OSCommandRunner{}})
	if err != nil || len(runs) != 1 || workflowRunID(runs[0]) != "123" {
		t.Fatalf("observer runs = %+v, err=%v", runs, err)
	}
	listCall := ""
	for _, line := range strings.Split(fake.calls(), "\n") {
		if strings.HasPrefix(line, "gh run list") {
			listCall = line
		}
	}
	for _, want := range []string{"--workflow publish.yml", "--commit " + releaseHead, "--event workflow_dispatch", "--limit 1000"} {
		if !strings.Contains(listCall, want) {
			t.Errorf("run list command %q missing %q", listCall, want)
		}
	}

	fake.write("list.error", "gh: Server Error (HTTP 500)\n")
	if _, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: OSCommandRunner{}}); err == nil ||
		!strings.Contains(err.Error(), "list exact workflow runs") {
		t.Fatalf("observer list error = %v", err)
	}
}

func TestRPCovWaitRunThroughTheSharedObserver(t *testing.T) {
	runnertest.AllowRealProcess(t)
	fake := rpCovInstallFakeCommands(t)
	run := workflowRunFixture("123", "completed", "success", time.Now().UTC())
	fake.write("run-view.json", run)
	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}

	if err := waitRun(context.Background(), receipt, Options{Runner: OSCommandRunner{}}, func() error { return nil }); err != nil {
		t.Fatalf("observer waitRun: %v", err)
	}
	if receipt.Status != StatusAwaitingRegistry {
		t.Fatalf("receipt status = %q, want awaiting_registry", receipt.Status)
	}
	if !strings.Contains(fake.calls(), "gh run view 123 --repo sneat-co/assetus") {
		t.Fatalf("calls = %q, want the exact run observation", fake.calls())
	}

	fake.write("view.error", "gh: Not Found (HTTP 404)\n")
	if err := waitRun(context.Background(), receipt, Options{Runner: OSCommandRunner{}}, func() error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "observe workflow run 123") {
		t.Fatalf("observer view error = %v", err)
	}
}

func TestRPCovWaitRunReportsTimeoutPersistenceFailure(t *testing.T) {
	t.Parallel()
	runner := &rpCovCommandRunner{steps: []CommandResult{
		{Output: workflowRunFixture("123", "in_progress", "", time.Now().UTC())},
	}}
	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	persistErr := errorString("disk full")
	err := waitRun(context.Background(), receipt, Options{Runner: runner, Timeout: time.Nanosecond}, func() error { return persistErr })
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error = %v, want the timeout persistence failure", err)
	}
}

func TestRPCovVerifyRegistryRequiresTheExactPublishedVersion(t *testing.T) {
	t.Parallel()
	checkedAt := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	options := Options{
		Registry: "https://registry.example",
		Runner:   &rpCovCommandRunner{steps: []CommandResult{{Output: `"9.9.9"`}}},
		Now:      func() time.Time { return checkedAt },
	}
	version, gotCheckedAt, err := verifyRegistry(context.Background(), Receipt{Release: testRelease()}, options)
	if err == nil || !strings.Contains(err.Error(), "exact version evidence is required") {
		t.Fatalf("error = %v, want the version mismatch refusal", err)
	}
	if version != "9.9.9" || !gotCheckedAt.Equal(checkedAt) {
		t.Fatalf("verifyRegistry = %q, %s", version, gotCheckedAt)
	}

	options.Runner = &rpCovCommandRunner{steps: []CommandResult{{Output: `"0.1.0"`}}}
	version, _, err = verifyRegistry(context.Background(), Receipt{Release: testRelease()}, options)
	if err != nil || version != "0.1.0" {
		t.Fatalf("exact registry version = %q, err=%v", version, err)
	}
}

func TestRPCovNormalizeRequiresATupleAndDefaultsTheRef(t *testing.T) {
	t.Parallel()
	if _, err := Normalize(nil, "main"); err == nil || !strings.Contains(err.Error(), "at least one npm release tuple") {
		t.Fatalf("empty-tuple error = %v", err)
	}
	release := testRelease()
	release.Ref = ""
	normalized, err := Normalize([]Release{release}, "  release/1  ")
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].Ref != "release/1" {
		t.Fatalf("normalized ref = %q, want the trimmed campaign ref", normalized[0].Ref)
	}
}

func TestRPCovValidateReleaseRejectsAnUnparseableTargetVersion(t *testing.T) {
	t.Parallel()
	release := testRelease()
	release.Version = ""
	if err := ValidateRelease(release); err == nil || !strings.Contains(err.Error(), "fully-qualified-dependency@version") {
		t.Fatalf("error = %v, want the missing-version refusal", err)
	}
}

// TestRPCovRunThroughRealCommandsPublishesAnExactVersion is the full
// publication path driven through the real OSCommandRunner: the shared GitHub
// observer resolves the exact head and observes the workflow run, while the
// dispatch and registry verification go straight to gh and npm. The shims
// record every invocation, so the assertions are about the exact commands WB
// issued, not only the final report.
func TestRPCovRunThroughRealCommandsPublishesAnExactVersion(t *testing.T) {
	runnertest.AllowRealProcess(t)
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	fake := rpCovInstallFakeCommands(t)
	fake.write("api.body", rpCovHTTPBody(`{"object":{"sha":"`+releaseHead+`"}}`))
	fake.write("run-list-first.json", `[]`)
	fake.write("list.once", "1")
	fake.write("run-list.json", workflowRunList(run))
	fake.write("run-view.json", run)
	fake.write("npm.output", `"0.1.0"`+"\n")

	report, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: OSCommandRunner{}, Timeout: 15 * time.Second, PollInterval: time.Millisecond,
		Registry: "https://registry.example",
		Now:      func() time.Time { return created },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	receipt := report.Releases[0]
	if report.Status != StatusPublished || receipt.Status != StatusPublished {
		t.Fatalf("report = %+v", report)
	}
	if receipt.RunID != "123" || receipt.RegistryVersion != "0.1.0" || receipt.RegistryURL != "https://registry.example/@sneat%2Fextension-assetus/0.1.0" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.RunHeadSHA != releaseHead || receipt.HeadSHA != releaseHead {
		t.Fatalf("receipt head evidence = %+v", receipt)
	}

	calls := fake.calls()
	for _, want := range []string{
		"gh api repos/sneat-co/assetus/git/ref/heads/main --include",
		"gh run list --repo sneat-co/assetus --workflow publish.yml --commit " + releaseHead,
		"gh workflow run publish.yml --repo sneat-co/assetus --ref main --field approved=true",
		"gh run view 123 --repo sneat-co/assetus",
		"npm view @sneat/extension-assetus@0.1.0 version --json --registry https://registry.example",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("command log missing %q\n---\n%s", want, calls)
		}
	}
}

// TestRPCovRunThroughRealCommandsReportsDispatchFailure proves the failure
// branch of the real boundary: a rejected workflow dispatch must leave a
// dispatch_failed receipt and must not touch the npm registry.
func TestRPCovRunThroughRealCommandsReportsDispatchFailure(t *testing.T) {
	runnertest.AllowRealProcess(t)
	created := time.Now().UTC().Truncate(time.Second)
	fake := rpCovInstallFakeCommands(t)
	fake.write("api.body", rpCovHTTPBody(`{"object":{"sha":"`+releaseHead+`"}}`))
	fake.write("run-list.json", `[]`)
	fake.write("workflow.error", "gh: workflow is disabled\n")
	fake.write("npm.output", `"0.1.0"`+"\n")

	report, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: OSCommandRunner{}, Timeout: 15 * time.Second, PollInterval: time.Millisecond,
		Registry: "https://registry.example",
		Now:      func() time.Time { return created },
	})
	if err == nil || !strings.Contains(err.Error(), "dispatch release workflow") {
		t.Fatalf("error = %v, want the dispatch failure", err)
	}
	if report.Releases[0].Status != StatusDispatchFailed {
		t.Fatalf("receipt = %+v, want dispatch_failed", report.Releases[0])
	}
	if strings.Contains(fake.calls(), "npm view") {
		t.Fatalf("a failed dispatch still reached the registry: %s", fake.calls())
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
