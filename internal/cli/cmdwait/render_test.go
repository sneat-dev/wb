package cmdwait

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/spf13/cobra"
	"strings"
	"testing"
	"time"
)

func TestWaitPRResumeLineQuotesSelectorsForAShell(t *testing.T) {
	t.Parallel()
	output := waitrun.Output{Targets: []waitrun.Target{{Selector: "acme/app#5", Status: waitrun.Pending}}}
	args := waitResumeArgs(output, waitrun.ChecksSettled, time.Minute, time.Second, true)
	command := &cobra.Command{}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	output.ResumeArgs = args
	if err := printWaitOutput(command.OutOrStdout(), output); err != nil {
		t.Fatal(err)
	}
	// '#' starts a shell comment, so an unquoted selector would truncate the
	// resume command at the first target.
	if !strings.Contains(stdout.String(), "'acme/app#5'") {
		t.Errorf("resume line did not quote the selector: %s", stdout.String())
	}
	if !strings.Contains(strings.Join(args, " "), "--json") {
		t.Errorf("resume args dropped --json: %v", args)
	}
}

func TestWaitPRPrintsTheFailingLineNotJustTheCheckName(t *testing.T) {
	t.Parallel()
	target := waitrun.Target{
		Selector: "acme/app#3",
		Status:   waitrun.Settled,
		Failed:   []string{"Tests"},
		Failures: []orchestrate.CIFailureDetail{{
			Check: "Tests",
			Annotations: []orchestrate.CIFailureAnnotation{{
				Path: "cmd/wb/daemon_file_bridge_test.go", StartLine: 149,
				Message: "old daemon operation count = 0, <nil>",
			}},
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// The whole reason to wake an agent for a red head is that it arrives
	// knowing the assertion, not a link to go and read one.
	for _, want := range []string{"cmd/wb/daemon_file_bridge_test.go:149", "old daemon operation count = 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure output missing %q; got %q", want, got)
		}
	}
}

func TestWaitPRFallsBackToAnExcerptWhenGitHubAnnotatedNothing(t *testing.T) {
	t.Parallel()
	target := waitrun.Target{
		Failures: []orchestrate.CIFailureDetail{{
			Check:   "Build",
			Excerpt: "undefined: waitForThing\nexit status 2",
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "undefined: waitForThing") {
		t.Errorf("excerpt not surfaced: %q", out.String())
	}
	if !strings.Contains(out.String(), "exit status 2") {
		t.Errorf("excerpt truncated to one line: %q", out.String())
	}
}

func TestWaitPRReportsTheReasonWhenThereIsNeitherAnnotationNorExcerpt(t *testing.T) {
	t.Parallel()
	target := waitrun.Target{
		Failures: []orchestrate.CIFailureDetail{{
			Check:  "Lint",
			Reason: "GitHub Actions run/job identifiers were not available for this check",
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "identifiers were not available") {
		t.Errorf("reason not surfaced: %q", out.String())
	}
}

func TestWaitPRPrintsWhyABlockedHeadCannotMerge(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	err := printWaitOutput(command.OutOrStdout(), waitrun.Output{Targets: []waitrun.Target{{
		Selector: "acme/app#12", Status: waitrun.Settled, State: "open",
		Checks: map[string]int{"pass": 1}, Blocked: []string{"build"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "blocked=build") {
		t.Errorf("summary line did not flag the block: %q", got)
	}
	if !strings.Contains(got, "cannot merge until something produces it") {
		t.Errorf("output did not explain the block: %q", got)
	}
}

func TestWaitOutputShowsAnnotationsExcerptsAndMissingRequiredChecks(t *testing.T) {
	t.Parallel()
	output := waitrun.Output{Targets: []waitrun.Target{{
		Selector: "acme/app#42", Status: waitrun.Settled, State: "open", Checks: map[string]int{"passed": 2},
		Failed: []string{"unit", "lint"}, Blocked: []string{"security"}, Reason: "checks failed",
		Failures: []orchestrate.CIFailureDetail{
			{Check: "unit", Annotations: []orchestrate.CIFailureAnnotation{{Path: "cmd/app.go", StartLine: 27, Message: "wrong value"}, {Path: "cmd/other.go", Message: "missing import"}}},
			{Check: "lint", Excerpt: "first finding\nsecond finding\n"},
			{Check: "build", Reason: "compiler stopped"},
		},
	}}, ResumeArgs: []string{"wb", "wait", "pr", "acme/app#42", "--until", "checks-settled"}}
	command := &cobra.Command{}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := printWaitOutput(command.OutOrStdout(), output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"acme/app#42 settled state=open", "failed=unit,lint", "blocked=security", "(checks failed)",
		"unit: cmd/app.go:27: wrong value", "unit: cmd/other.go: missing import", "lint: first finding", "lint: second finding",
		"build: compiler stopped", `required check "security" has no passing result`, "resume: wb wait pr 'acme/app#42' --until checks-settled",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output %q does not contain %q", stdout.String(), want)
		}
	}
}
func TestRenderingReturnsEachWriterFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("writer refused")
	output := waitrun.Output{Targets: []waitrun.Target{{Selector: "acme/app#1", Status: waitrun.Pending, Failures: []orchestrate.CIFailureDetail{{Check: "one", Annotations: []orchestrate.CIFailureAnnotation{{Path: "go.mod", Message: "bad"}}}, {Check: "two", Excerpt: "failure"}, {Check: "three"}}, Blocked: []string{"security"}}}, ResumeArgs: []string{"wb", "wait", "pr", "acme/app#1"}}
	for failAt := 1; failAt <= 5; failAt++ {
		writer := &recordingWriter{failAt: failAt, err: failure}
		if err := printWaitOutput(writer, output); err != failure {
			t.Fatal(failAt, err)
		}
	}
}
