package cmdci

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

// TestValidateCIWaitInputsRejectsBadRepository drives validateCIWaitInputs'
// strings.Cut branch: a repository with no "owner/name" shape must be
// refused before any git subprocess runs.
func TestValidateCIWaitInputsRejectsBadRepository(t *testing.T) {
	t.Parallel()
	err := validateCIWaitInputs("not-a-repo-shape", "", "main", strings.Repeat("a", 40), time.Minute, time.Second, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "--repo must be owner/repository") {
		t.Fatalf("error = %v; want a --repo shape refusal", err)
	}
}

// TestValidateCIWaitInputsRejectsBlankTarget drives the target
// strings.TrimSpace branch: an empty or padded target is refused before any
// git subprocess runs.
func TestValidateCIWaitInputsRejectsBlankTarget(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"", "  main", "main  "} {
		err := validateCIWaitInputs("acme/app", "", target, strings.Repeat("a", 40), time.Minute, time.Second, func(string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "--target is required") {
			t.Fatalf("target=%q error = %v; want a --target refusal", target, err)
		}
	}
}

// TestPrintCIWaitRendersResumeArgsAndPullRequestPrefix drives printCIWait's
// PullRequest-prefix branch and its resume-args branch.
func TestPrintCIWaitRendersResumeArgsAndPullRequestPrefix(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	output := WaitOutput{
		PullRequestWaitResult: orchestrate.PullRequestWaitResult{
			Status: orchestrate.PullRequestWaitPending, Repository: "acme/app",
			PullRequest: "42", Target: "main", Head: strings.Repeat("a", 40), Reason: "not yet green",
		},
		ResumeArgs: []string{"wb", "ci", "wait", "--repo", "acme/app"},
	}
	if err := printCIWait(command, output); err != nil {
		t.Fatalf("printCIWait() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "PR 42 -> main@"+strings.Repeat("a", 40)) {
		t.Fatalf("output = %q; want the PR prefix in the identity", rendered)
	}
	if !strings.Contains(rendered, "resume: wb ci wait --repo acme/app") {
		t.Fatalf("output = %q; want a resume line", rendered)
	}
}

// TestPrintCIWaitRendersFailureDetails drives every failure-detail rendering
// branch: run URL, job URL, both annotation end-line cases, excerpt, and
// diagnostic reason. Only reachable when ResumeArgs is empty, since a
// non-empty ResumeArgs returns before this loop.
func TestPrintCIWaitRendersFailureDetails(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	output := WaitOutput{
		PullRequestWaitResult: orchestrate.PullRequestWaitResult{
			Status: orchestrate.PullRequestWaitFailed, Repository: "acme/app",
			Target: "main", Head: strings.Repeat("a", 40), Reason: "checks failed",
			FailureDetails: []orchestrate.CIFailureDetail{{
				Check:  "build",
				RunURL: "https://github.com/acme/app/actions/runs/1",
				JobURL: "https://github.com/acme/app/actions/runs/1/job/2",
				Annotations: []orchestrate.CIFailureAnnotation{
					{Path: "a.go", StartLine: 10, EndLine: 10, Message: "single line"},
					{Path: "b.go", StartLine: 5, EndLine: 8, Message: "multi line"},
				},
				Excerpt: "--- FAIL: TestThing",
				Reason:  "flaky retry exhausted",
			}},
		},
	}
	if err := printCIWait(command, output); err != nil {
		t.Fatalf("printCIWait() error = %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"failed build",
		"run: https://github.com/acme/app/actions/runs/1",
		"job: https://github.com/acme/app/actions/runs/1/job/2",
		"annotation: a.go:10: single line",
		"annotation: b.go:5-8: multi line",
		"failed-step tail:\n--- FAIL: TestThing",
		"diagnostic: flaky retry exhausted",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output = %q; want it to contain %q", rendered, want)
		}
	}
}

// failAfterNWriter succeeds on every Write except the failAt-th call, which
// lets a test drive one specific `if _, err := fmt.Fprintf(...); err != nil`
// branch among several sequential Fprintf calls in the same function.
type failAfterNWriter struct {
	writes int
	failAt int
}

func (w *failAfterNWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("write refused")
	}
	return len(p), nil
}

// TestPrintCIWaitPropagatesEveryWriteFailure drives every `return err`
// branch in printCIWait by failing exactly one Fprintf call at a time, in a
// fixture that reaches all of them: the status line, the failed-check line,
// the run URL, the job URL, the annotation line, the excerpt, and the
// diagnostic reason.
func TestPrintCIWaitPropagatesEveryWriteFailure(t *testing.T) {
	t.Parallel()
	output := WaitOutput{
		PullRequestWaitResult: orchestrate.PullRequestWaitResult{
			Status: orchestrate.PullRequestWaitFailed, Repository: "acme/app",
			Target: "main", Head: strings.Repeat("a", 40), Reason: "checks failed",
			FailureDetails: []orchestrate.CIFailureDetail{{
				Check:  "build",
				RunURL: "https://github.com/acme/app/actions/runs/1",
				JobURL: "https://github.com/acme/app/actions/runs/1/job/2",
				Annotations: []orchestrate.CIFailureAnnotation{
					{Path: "a.go", StartLine: 1, EndLine: 1, Message: "m"},
				},
				Excerpt: "boom",
				Reason:  "flaky",
			}},
		},
	}
	// Call order: 1=status line, 2=failed check, 3=run URL, 4=job URL,
	// 5=annotation, 6=excerpt, 7=diagnostic reason.
	for callIndex := 1; callIndex <= 7; callIndex++ {
		writer := &failAfterNWriter{failAt: callIndex}
		command := &cobra.Command{}
		command.SetOut(writer)
		err := printCIWait(command, output)
		if err == nil || !strings.Contains(err.Error(), "write refused") {
			t.Fatalf("call %d: printCIWait() error = %v; want the write failure to propagate", callIndex, err)
		}
	}
}

// TestPrintCIAuditRendersEveryBranch drives every rendering branch of
// printCIAudit: the no-policy-applies short circuit, each of the three
// threshold checkmarks, and both the with-file and without-file finding
// lines. A local buffer verifies the command output without process-global state.
func TestPrintCIAuditRendersEveryBranch(t *testing.T) {
	t.Parallel()
	reports := []ciaudit.Report{
		{Path: "acme/no-policy"},
		{
			Path:  "acme/full-policy",
			HasGo: true, GoCoverageThreshold: true,
			HasFrontend: true, FrontendCoverageThreshold: true,
			HasDeploy: true, ArtifactPromotion: true,
			Findings: []ciaudit.Finding{
				{Code: "F1", Message: "missing floor", File: "go.yml"},
				{Code: "F2", Message: "no file context"},
			},
		},
	}
	var out bytes.Buffer
	if err := printCIAudit(&out, reports); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"acme/no-policy",
		"– no Go/frontend/deploy CI policy applies",
		"acme/full-policy",
		"✓ Go coverage threshold",
		"✓ frontend coverage threshold",
		"✓ deploys promote verified build artifacts",
		"✗ F1: missing floor (go.yml)",
		"✗ F2: no file context\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q; want it to contain %q", output, want)
		}
	}
}

func TestPrintCIWaitShellQuotesResumeArguments(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := printCIWait(command, WaitOutput{
		PullRequestWaitResult: orchestrate.PullRequestWaitResult{
			Status:     orchestrate.PullRequestWaitPending,
			Repository: "acme/app",
			Target:     "feature/$(touch-pwned)",
			Head:       testHead,
			Reason:     "resume",
		},
		ResumeArgs: []string{"wb", "ci", "wait", "--target", "feature/$(touch-pwned)", "--pr", "https://example.test/pr/1?x='y'"},
	}); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, quoted := range []string{`'feature/$(touch-pwned)'`, `'https://example.test/pr/1?x='"'"'y'"'"''`} {
		if !strings.Contains(got, quoted) {
			t.Fatalf("human resume command is not shell-safe; missing %q in %q", quoted, got)
		}
	}
}

func TestPrintCIWaitIncludesFailureDiagnosticLinksAndExcerpt(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	err := printCIWait(command, WaitOutput{PullRequestWaitResult: orchestrate.PullRequestWaitResult{
		Status:     orchestrate.PullRequestWaitFailed,
		Repository: "acme/app",
		Target:     "main",
		Head:       testHead,
		Reason:     "check failed",
		FailureDetails: []orchestrate.CIFailureDetail{{
			Check: "check-run:test", RunURL: "https://github.com/acme/app/actions/runs/123",
			JobURL:      "https://github.com/acme/app/actions/runs/123/job/456",
			Annotations: []orchestrate.CIFailureAnnotation{{Path: "cmd/wb/ci.go", StartLine: 17, Message: "unchecked error"}},
			Excerpt:     "compile failed",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed check-run:test", "run: https://github.com/acme/app/actions/runs/123", "job: https://github.com/acme/app/actions/runs/123/job/456", "annotation: cmd/wb/ci.go:17: unchecked error", "failed-step tail:\ncompile failed"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("failure output missing %q: %s", want, output.String())
		}
	}
}
