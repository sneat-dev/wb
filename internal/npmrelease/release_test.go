package npmrelease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"gopkg.in/yaml.v3"
)

const releaseHead = "0123456789abcdef0123456789abcdef01234567"

type fakeRunner struct {
	calls []string
	steps []CommandResult
}

func (runner *fakeRunner) Run(_ context.Context, _ string, args ...string) CommandResult {
	runner.calls = append(runner.calls, strings.Join(args, " "))
	if len(runner.steps) == 0 {
		return CommandResult{Code: 2, Err: errors.New("unexpected command")}
	}
	result := runner.steps[0]
	runner.steps = runner.steps[1:]
	return result
}

type deadlineRunner struct {
	sawDeadline bool
}

func (runner *deadlineRunner) Run(ctx context.Context, _ string, _ ...string) CommandResult {
	_, runner.sawDeadline = ctx.Deadline()
	select {
	case <-ctx.Done():
		return CommandResult{Code: 1, Err: ctx.Err()}
	case <-time.After(250 * time.Millisecond):
		return CommandResult{Code: 1, Err: errors.New("publication command received no cancellation deadline")}
	}
}

func testRelease() Release {
	return Release{
		Repository: "sneat-co/assetus",
		Workflow:   "publish.yml",
		Package:    "@sneat/extension-assetus",
		Version:    "0.1.0",
		Ref:        "main",
		Inputs:     map[string]string{"approved": "true"},
	}
}

func workflowRunFixture(id, status, conclusion string, created time.Time) string {
	return fmt.Sprintf(`{"databaseId":%s,"headSha":%q,"status":%q,"conclusion":%q,"event":"workflow_dispatch","createdAt":%q,"updatedAt":%q,"url":%q}`,
		id, releaseHead, status, conclusion, created.UTC().Format(time.RFC3339), created.UTC().Add(time.Second).Format(time.RFC3339), "https://github.com/sneat-co/assetus/actions/runs/"+id)
}

func workflowRunList(values ...string) string {
	return "[" + strings.Join(values, ",") + "]"
}

func TestNormalizeRejectsDuplicatePublicationTuples(t *testing.T) {
	t.Parallel()
	_, err := Normalize([]Release{testRelease(), testRelease()}, "main")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Normalize error = %v, want duplicate tuple rejection", err)
	}
}

func TestOperationIDIgnoresWorkflowInputsButResumeRequiresFingerprint(t *testing.T) {
	runtime := testRelease()
	runtime.Inputs = map[string]string{"package": "runtime"}
	ui := runtime
	ui.Inputs = map[string]string{"package": "ui"}
	runtimeReleases, err := Normalize([]Release{runtime}, "main")
	if err != nil {
		t.Fatal(err)
	}
	uiReleases, err := Normalize([]Release{ui}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := OperationIDFor(uiReleases), OperationIDFor(runtimeReleases); got != want {
		t.Fatalf("operation identity changed with workflow input: got %q, want %q", got, want)
	}
	if got, want := PublicationClaimOperationIDs(uiReleases), PublicationClaimOperationIDs(runtimeReleases); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("publication claim identity changed with workflow input: got %v, want %v", got, want)
	}
	previous := plannedReport(runtimeReleases)
	runner := &fakeRunner{}
	_, err = Run(t.Context(), uiReleases, Options{Apply: true, Resume: true, Previous: &previous, Runner: runner, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "does not match the requested tuple") {
		t.Fatalf("changed-input resume error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("changed-input resume reached external commands: %v", runner.calls)
	}
}

func TestRunDryRunNeverInvokesGitHubOrNPM(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusPlanned || len(report.Releases) != 1 || report.Releases[0].Status != StatusPlanned {
		t.Fatalf("report = %+v", report)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run invoked external commands: %v", runner.calls)
	}
	if _, err := EventsFor(report); err == nil {
		t.Fatal("planned report unexpectedly produced release events")
	}
}

func TestRunBoundsEachExternalCommandWithTimeout(t *testing.T) {
	runner := &deadlineRunner{}
	started := time.Now()
	report, err := Run(t.Context(), []Release{testRelease()}, Options{
		Apply: true, Runner: runner, Timeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("timed external command error = %v", err)
	}
	if !runner.sawDeadline {
		t.Fatal("publication subprocess did not receive the per-command timeout deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed external command took %s", elapsed)
	}
	if report.Status != StatusFailed || report.Releases[0].Status != StatusFailed {
		t.Fatalf("timed external command report = %+v", report)
	}
}

func TestRunDispatchWaitAndRegistryEvidence(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead + "\n"},
		{Output: `[]`},
		{},
		{Output: workflowRunList(run)},
		{Output: run},
		{Output: `"0.1.0"`},
	}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: runner, Timeout: time.Second, PollInterval: 0,
		Now: func() time.Time { return created },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := report.Releases[0]
	if report.Status != StatusPublished || receipt.Status != StatusPublished || receipt.HeadSHA != releaseHead || receipt.RunID != "123" || receipt.RegistryVersion != "0.1.0" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Events) != 1 || report.Events[0] != (deps.ReleaseEvent{Dependency: "@sneat/extension-assetus", Version: "0.1.0", Source: "npm_workflow", CheckedAt: created}) {
		t.Fatalf("events = %+v", report.Events)
	}
	if len(runner.calls) != 6 || !strings.Contains(runner.calls[2], "workflow run publish.yml") || !strings.Contains(runner.calls[5], "npm view @sneat/extension-assetus@0.1.0") {
		t.Fatalf("commands = %v", runner.calls)
	}
	events, err := EventsFor(report)
	if err != nil || len(events) != 1 {
		t.Fatalf("EventsFor = %+v, %v", events, err)
	}
}

func TestRunPollsForDelayedWorkflowVisibility(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {},
		{Output: `[]`},
		{Output: workflowRunList(run)},
		{Output: run},
		{Output: `"0.1.0"`},
	}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: runner, Timeout: time.Second, PollInterval: time.Millisecond,
		Now: func() time.Time { return created },
	})
	if err != nil || report.Status != StatusPublished || report.Releases[0].RunID != "123" {
		t.Fatalf("report = %+v, err=%v", report, err)
	}
	if len(runner.calls) != 7 || strings.Contains(strings.Join(runner.calls[3:5], " "), "workflow run") {
		t.Fatalf("delayed workflow visibility calls = %v", runner.calls)
	}
}

func TestRunDispatchesTupleScopedInputsForSameWorkflow(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	runtimeRun := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	uiRun := workflowRunFixture("456", "completed", "success", created.Add(2*time.Second))
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {}, {Output: workflowRunList(runtimeRun)}, {Output: runtimeRun}, {Output: `"0.0.1"`},
		{Output: releaseHead}, {Output: workflowRunList(runtimeRun)}, {}, {Output: workflowRunList(runtimeRun, uiRun)}, {Output: uiRun}, {Output: `"0.0.1"`},
	}}
	releases := []Release{
		{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "runtime"}},
		{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius-ui", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "ui"}},
	}
	report, err := Run(context.Background(), releases, Options{
		Apply: true, Runner: runner, Timeout: time.Second, PollInterval: 0,
		Now: func() time.Time { return created },
	})
	if err != nil || report.Status != StatusPublished {
		t.Fatalf("report = %+v, err=%v", report, err)
	}
	if report.Releases[0].RunID != "123" || report.Releases[1].RunID != "456" || len(report.Releases[1].DispatchBaselineRunIDs) != 1 || report.Releases[1].DispatchBaselineRunIDs[0] != "123" {
		t.Fatalf("same-workflow baseline receipts = %+v", report.Releases)
	}
	if len(runner.calls) != 12 || !strings.Contains(runner.calls[2], "--field package=runtime") || strings.Contains(runner.calls[2], "package=ui") || !strings.Contains(runner.calls[8], "--field package=ui") || strings.Contains(runner.calls[8], "package=runtime") {
		t.Fatalf("tuple-scoped workflow dispatches = %v", runner.calls)
	}
}

func TestRunFailsClosedWhenObservedWorkflowHeadDiffers(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	otherHead := "fedcba9876543210fedcba9876543210fedcba98"
	mismatchedObservation := strings.Replace(run, releaseHead, otherHead, 1)
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {},
		{Output: workflowRunList(run)},
		{Output: mismatchedObservation},
	}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: runner, Timeout: time.Second, Now: func() time.Time { return created },
	})
	if err == nil || !strings.Contains(err.Error(), "does not match dispatched head") || report.Status != StatusFailed || report.Releases[0].RunID != "123" {
		t.Fatalf("head-mismatch report = %+v, err=%v", report, err)
	}
	if len(runner.calls) != 5 || strings.Contains(strings.Join(runner.calls, "\n"), "npm view") {
		t.Fatalf("head mismatch reached registry evidence: %v", runner.calls)
	}
}

func TestRunRefusesAmbiguousExactWorkflowRuns(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {},
		{Output: workflowRunList(workflowRunFixture("123", "queued", "", created.Add(time.Second)), workflowRunFixture("456", "queued", "", created.Add(2*time.Second)))},
	}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, Runner: runner, Timeout: time.Second, Now: func() time.Time { return created }})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || report.Status != StatusAwaitingRun {
		t.Fatalf("report = %+v, err=%v", report, err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("ambiguous run was observed or registry was queried: %v", runner.calls)
	}
}

func TestRunRefusesPotentiallyTruncatedWorkflowBaseline(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	runs := make([]string, exactWorkflowRunListLimit)
	for index := range runs {
		runs[index] = workflowRunFixture(fmt.Sprintf("%d", index+1), "completed", "success", created.Add(time.Duration(index)*time.Second))
	}
	runner := &fakeRunner{steps: []CommandResult{{Output: releaseHead}, {Output: workflowRunList(runs...)}}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, Runner: runner, Timeout: time.Second, Now: func() time.Time { return created }})
	if err == nil || !strings.Contains(err.Error(), "correlation limit") || report.Status != StatusFailed {
		t.Fatalf("truncated-baseline report = %+v, err=%v", report, err)
	}
	if len(runner.calls) != 2 || strings.Contains(strings.Join(runner.calls, "\n"), "workflow run publish.yml") {
		t.Fatalf("truncated baseline reached workflow dispatch: %v", runner.calls)
	}
}

func TestRunRegistryFailureResumesWithoutDispatchingAgain(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	first := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {},
		{Output: workflowRunList(run)},
		{Output: run},
		{Code: 1, Err: errors.New("not yet visible"), Output: "E404 package version not found"},
	}}
	failedReport, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: first, Timeout: time.Second, Now: func() time.Time { return created },
	})
	if err == nil || failedReport.Status != StatusAwaitingRegistry || failedReport.Releases[0].Status != StatusAwaitingRegistry {
		t.Fatalf("first report = %+v, err=%v", failedReport, err)
	}
	second := &fakeRunner{steps: []CommandResult{
		{Output: run},
		{Output: `"0.1.0"`},
	}}
	resumed, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Resume: true, Previous: &failedReport, Runner: second, Timeout: time.Second, Now: func() time.Time { return created.Add(time.Minute) },
	})
	if err != nil || resumed.Status != StatusPublished {
		t.Fatalf("resumed report = %+v, err=%v", resumed, err)
	}
	if len(second.calls) != 2 || strings.Contains(second.calls[0], "workflow run") || !strings.HasPrefix(second.calls[1], "npm view ") {
		t.Fatalf("resume dispatched or polled unexpectedly: %v", second.calls)
	}
}

func TestRunRejectsFailedWorkflowAndPreservesExactReceipt(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "completed", "failure", created.Add(time.Second))
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {},
		{Output: workflowRunList(run)},
		{Output: run},
	}}
	report, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, Runner: runner, Timeout: time.Second, Now: func() time.Time { return created }})
	if err == nil || report.Status != StatusFailed || report.Releases[0].RunID != "123" || report.Releases[0].RunConclusion != "failure" {
		t.Fatalf("report = %+v, err=%v", report, err)
	}
}

func TestRunPersistsPreDispatchBaselineBeforeWorkflowDispatch(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead},
		{Output: workflowRunList(workflowRunFixture("122", "completed", "success", created.Add(-time.Minute)))},
	}}
	stop := errors.New("stop after durable baseline")
	_, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: runner, Timeout: time.Second, Now: func() time.Time { return created },
		Persist: func(report Report) error {
			receipt := report.Releases[0]
			if receipt.Status != StatusDispatchUnknown {
				return nil
			}
			if len(runner.calls) != 2 || strings.Contains(strings.Join(runner.calls, "\n"), "workflow run publish.yml") {
				t.Fatalf("workflow dispatch occurred before baseline persistence: %v", runner.calls)
			}
			if receipt.HeadSHA != releaseHead || receipt.DispatchAt.IsZero() || receipt.DispatchBaselineAt.IsZero() || len(receipt.DispatchBaselineRunIDs) != 1 || receipt.DispatchBaselineRunIDs[0] != "122" {
				t.Fatalf("durable pre-dispatch receipt = %+v", receipt)
			}
			return stop
		},
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Run error = %v, want baseline persistence stop", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v, want head and baseline only", runner.calls)
	}
}

func TestRunResumesAfterCrashWithoutRedispatch(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	crash := errors.New("simulated crash after dispatch")
	persistCalls := 0
	first := &fakeRunner{steps: []CommandResult{{Output: releaseHead}, {Output: `[]`}, {}}}
	failedReport, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Runner: first, Timeout: time.Second, Now: func() time.Time { return created },
		Persist: func(Report) error {
			persistCalls++
			if persistCalls == 3 {
				return crash
			}
			return nil
		},
	})
	if !errors.Is(err, crash) || failedReport.Releases[0].Status != StatusAwaitingRun || failedReport.Releases[0].DispatchBaselineAt.IsZero() {
		t.Fatalf("post-dispatch crash report = %+v, err=%v", failedReport, err)
	}
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	resumedRunner := &fakeRunner{steps: []CommandResult{{Output: workflowRunList(run)}, {Output: run}, {Output: `"0.1.0"`}}}
	resumed, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Resume: true, Previous: &failedReport, Runner: resumedRunner, Timeout: time.Second,
		Now: func() time.Time { return created.Add(time.Minute) },
	})
	if err != nil || resumed.Status != StatusPublished {
		t.Fatalf("resumed report = %+v, err=%v", resumed, err)
	}
	if strings.Contains(strings.Join(resumedRunner.calls, "\n"), "workflow run publish.yml") {
		t.Fatalf("resume redispatched workflow: %v", resumedRunner.calls)
	}
}

func TestRunResumeDispatchesOnlyTheUnreceiptedLaterTuple(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	directory := t.TempDir()
	runtime := Release{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "runtime"}}
	ui := Release{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius-ui", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "ui"}}
	runtimeRun := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	crash := errors.New("simulated crash after first tuple")
	first := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {}, {Output: workflowRunList(runtimeRun)}, {Output: runtimeRun}, {Output: `"0.0.1"`},
	}}
	partial, err := Run(context.Background(), []Release{runtime, ui}, Options{
		Apply: true, Runner: first, Timeout: time.Second, Now: func() time.Time { return created },
		Persist: func(report Report) error {
			if err := WriteReport(directory, report); err != nil {
				return err
			}
			if report.Releases[0].Status == StatusPublished && report.Releases[1].Status == StatusPlanned {
				return crash
			}
			return nil
		},
	})
	if !errors.Is(err, crash) || partial.Releases[0].Status != StatusPublished || partial.Releases[0].DispatchAt.IsZero() || partial.Releases[1].Status != StatusPlanned || !partial.Releases[1].DispatchAt.IsZero() {
		t.Fatalf("partial tuple report = %+v, err=%v", partial, err)
	}
	if len(first.calls) != 6 {
		t.Fatalf("first tuple calls = %v", first.calls)
	}
	previous, err := LoadReport(directory)
	if err != nil {
		t.Fatalf("load durable partial publication receipt: %v", err)
	}
	if previous.Releases[0].Status != StatusPublished || previous.Releases[0].DispatchAt.IsZero() || previous.Releases[1].Status != StatusPlanned || !previous.Releases[1].DispatchAt.IsZero() {
		t.Fatalf("durable partial tuple report = %+v", previous)
	}
	uiRun := workflowRunFixture("456", "completed", "success", created.Add(2*time.Second))
	resumedRunner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: workflowRunList(runtimeRun)}, {}, {Output: workflowRunList(runtimeRun, uiRun)}, {Output: uiRun}, {Output: `"0.0.1"`},
	}}
	resumed, err := Run(context.Background(), []Release{runtime, ui}, Options{
		Apply: true, Resume: true, Previous: &previous, Runner: resumedRunner, Timeout: time.Second,
		Now: func() time.Time { return created.Add(time.Minute) },
	})
	if err != nil || resumed.Status != StatusPublished || resumed.Releases[0].RunID != "123" || resumed.Releases[1].RunID != "456" {
		t.Fatalf("resumed multi-tuple report = %+v, err=%v", resumed, err)
	}
	if len(resumedRunner.calls) != 6 || strings.Contains(strings.Join(resumedRunner.calls, "\n"), "--field package=runtime") || !strings.Contains(strings.Join(resumedRunner.calls, "\n"), "--field package=ui") {
		t.Fatalf("resume redispatched an already receipted tuple: %v", resumedRunner.calls)
	}
}

func TestRunResumeReordersReceiptedTuplesWithoutRedispatch(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	runtime := Release{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "runtime"}}
	ui := Release{Repository: "sneat-co/eventius", Workflow: "release-frontend.yml", Package: "@sneat/extension-eventius-ui", Version: "0.0.1", Ref: "main", Inputs: map[string]string{"package": "ui"}}
	normalized, err := Normalize([]Release{runtime, ui}, "main")
	if err != nil {
		t.Fatal(err)
	}
	previous := plannedReport(normalized)
	for index := range previous.Releases {
		previous.Releases[index].Status = StatusPublished
		previous.Releases[index].DispatchAt = created
		previous.Releases[index].DispatchBaselineAt = created
		previous.Releases[index].HeadSHA = releaseHead
		previous.Releases[index].DispatchBaselineRunIDs = []string{fmt.Sprintf("%d", index+1)}
		previous.Releases[index].RunID = fmt.Sprintf("%d", index+1)
		previous.Releases[index].RegistryVersion = "0.0.1"
	}
	runner := &fakeRunner{}
	resumed, err := Run(t.Context(), []Release{ui, runtime}, Options{
		Apply: true, Resume: true, Previous: &previous, Runner: runner, Timeout: time.Second,
	})
	if err != nil || resumed.Status != StatusPublished {
		t.Fatalf("reordered resume = %+v, err=%v", resumed, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("reordered resume redispatched or re-polled a receipt: %v", runner.calls)
	}
	if resumed.Releases[0].Package != ui.Package || resumed.Releases[0].RunID != "2" || resumed.Releases[1].Package != runtime.Package || resumed.Releases[1].RunID != "1" {
		t.Fatalf("reordered receipt mapping = %+v", resumed.Releases)
	}
}

func TestRunDispatchFailureRemainsActionableWithoutRedispatch(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	first := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {Code: 1, Err: errors.New("failed"), Output: "npm_token=not-a-real-secret"},
	}}
	failedReport, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, Runner: first, Timeout: time.Second, Now: func() time.Time { return created }})
	if err == nil || strings.Contains(err.Error(), "not-a-real-secret") || failedReport.Releases[0].Status != StatusDispatchFailed {
		t.Fatalf("dispatch failure report = %+v, err=%v", failedReport, err)
	}
	resumedRunner := &fakeRunner{steps: []CommandResult{{Output: `[]`}}}
	resumed, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Resume: true, Previous: &failedReport, Runner: resumedRunner, Timeout: time.Nanosecond,
		Now: func() time.Time { return created.Add(time.Minute) },
	})
	if err == nil || resumed.Releases[0].Status != StatusDispatchFailed || strings.Contains(strings.Join(resumedRunner.calls, "\n"), "workflow run publish.yml") {
		t.Fatalf("dispatch-failed resume report = %+v, calls=%v, err=%v", resumed, resumedRunner.calls, err)
	}
}

func TestRunRejectsSecretLikeInputsAndMalformedNpmNamesBeforeExternalCalls(t *testing.T) {
	tests := []Release{
		func() Release {
			value := testRelease()
			value.Inputs = map[string]string{"npm_token": "must-not-leak"}
			return value
		}(),
		func() Release {
			value := testRelease()
			value.Inputs = map[string]string{"Authorization": "must-not-leak"}
			return value
		}(),
		func() Release { value := testRelease(); value.Package = "@Sneat/uppercase"; return value }(),
		func() Release { value := testRelease(); value.Package = "@sneat/has space"; return value }(),
		func() Release { value := testRelease(); value.Package = "@sneat"; return value }(),
	}
	for _, release := range tests {
		runner := &fakeRunner{}
		_, err := Run(context.Background(), []Release{release}, Options{Apply: true, Runner: runner})
		if err == nil || len(runner.calls) != 0 {
			t.Fatalf("release %+v error=%v calls=%v; want validation before external call", release, err, runner.calls)
		}
		if strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("validation leaked input value: %v", err)
		}
	}
}

func TestValidateOptionsRejectsCredentialBearingRegistryURLsWithoutLeakingValues(t *testing.T) {
	for _, registry := range []string{
		"https://user:must-not-leak@registry.example",
		"https://registry.example/?token=must-not-leak",
		"https://registry.example/#access_token=must-not-leak",
	} {
		err := ValidateOptions(Options{Apply: true, Registry: registry})
		if err == nil || !strings.Contains(err.Error(), "must not contain") || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("registry %q error = %v", registry, err)
		}
	}
}

func TestCommandErrorRedactsCredentialLookingDetails(t *testing.T) {
	err := commandError("dispatch", CommandResult{Output: "npm_token=not-a-real-secret Authorization: Bearer not-a-real-bearer"})
	if strings.Contains(err.Error(), "not-a-real-secret") || strings.Contains(err.Error(), "not-a-real-bearer") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("sanitized error = %v", err)
	}
}

func TestWriteReportRoundTripsBothFormats(t *testing.T) {
	directory := t.TempDir()
	release := testRelease()
	// Values are deliberately not heuristically classified: an innocently named
	// workflow input can legitimately carry an opaque value that resembles a
	// token. The contract is key-name rejection plus strict omission from every
	// durable representation.
	release.Inputs = map[string]string{"package": "token-looking-value-must-not-be-persisted"}
	normalized, err := Normalize([]Release{release}, "main")
	if err != nil {
		t.Fatal(err)
	}
	report := plannedReport(normalized)
	if err := WriteReport(directory, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReport(directory)
	if err != nil || loaded.Operation != report.Operation || loaded.SchemaVersion != SchemaVersion || loaded.Generation == "" || loaded.Generation != reportGeneration(loaded) {
		t.Fatalf("LoadReport = %+v, %v", loaded, err)
	}
	if len(loaded.Releases[0].Inputs) != 0 || loaded.Releases[0].InputFingerprint == "" {
		t.Fatalf("loaded report leaked inputs or lost its resume fingerprint: %+v", loaded.Releases[0])
	}
	for _, name := range []string{"npm-publish.yaml", "npm-publish.json"} {
		contents, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(contents), "token-looking-value-must-not-be-persisted") || strings.Contains(string(contents), "inputs:") || strings.Contains(string(contents), `"inputs"`) {
			t.Fatalf("%s persisted a raw workflow input: %s", name, contents)
		}
	}
	contents, err := os.ReadFile(filepath.Join(directory, "npm-publish.json"))
	if err != nil {
		t.Fatal(err)
	}
	var jsonReport Report
	if err := yaml.Unmarshal(contents, &jsonReport); err != nil || jsonReport.Operation != report.Operation || jsonReport.Generation != loaded.Generation {
		t.Fatalf("JSON report = %+v, %v", jsonReport, err)
	}
}

func TestResumeHydratesWorkflowInputsWithoutPersistingValues(t *testing.T) {
	directory := t.TempDir()
	created := time.Now().UTC().Truncate(time.Second)
	release := testRelease()
	release.Inputs = map[string]string{"package": "runtime"}
	plan, err := Run(t.Context(), []Release{release}, Options{DryRun: true, Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(directory, plan); err != nil {
		t.Fatal(err)
	}
	previous, err := LoadReport(directory)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowRunFixture("123", "completed", "success", created.Add(time.Second))
	runner := &fakeRunner{steps: []CommandResult{
		{Output: releaseHead}, {Output: `[]`}, {}, {Output: workflowRunList(run)}, {Output: run}, {Output: `"0.1.0"`},
	}}
	resumed, err := Run(t.Context(), []Release{release}, Options{
		Apply: true, Resume: true, Previous: &previous, Runner: runner, Timeout: time.Second,
		Now: func() time.Time { return created },
	})
	if err != nil || resumed.Status != StatusPublished {
		t.Fatalf("hydrated-input resume = %+v, err=%v", resumed, err)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "--field package=runtime") {
		t.Fatalf("resume did not hydrate its workflow input: %v", runner.calls)
	}
}

func TestPersistCallbackNeverReceivesWorkflowInputValues(t *testing.T) {
	release := testRelease()
	release.Inputs = map[string]string{"package": "must-not-reach-persist-callback"}
	_, err := Run(t.Context(), []Release{release}, Options{
		DryRun: true,
		Persist: func(report Report) error {
			if len(report.Releases) != 1 || len(report.Releases[0].Inputs) != 0 || strings.Contains(fmt.Sprintf("%+v", report), "must-not-reach-persist-callback") {
				t.Fatalf("persist callback received workflow values: %+v", report)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadReportRefusesMismatchedAtomicGenerations(t *testing.T) {
	directory := t.TempDir()
	report := plannedReport([]Release{testRelease()})
	if err := WriteReport(directory, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReport(directory)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "npm-publish.json")
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(strings.Replace(string(contents), loaded.Generation, strings.Repeat("0", len(loaded.Generation)), 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(directory); err == nil || !strings.Contains(err.Error(), "generations are inconsistent") {
		t.Fatalf("mismatched atomic pair error = %v", err)
	}
}
