package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	err := validateCIWaitInputs("not-a-repo-shape", "", "main", strings.Repeat("a", 40), time.Minute, time.Second)
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
		err := validateCIWaitInputs("acme/app", "", target, strings.Repeat("a", 40), time.Minute, time.Second)
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
	output := ciWaitOutput{
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
	output := ciWaitOutput{
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
	output := ciWaitOutput{
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
// lines. Uses the package's stdout-capture helper (printCIAudit writes with
// fmt.Println/fmt.Printf directly), so this test is not parallel.
func TestPrintCIAuditRendersEveryBranch(t *testing.T) {
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
	output := cwCovCaptureStdout(t, func() { printCIAudit(reports) })
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

// TestRunCIAuditFleetModeFiltersAndReportsFindings drives runCIAudit's
// fleet-discovery loop (range + the filter's strings.Contains, both
// branches), the jsonOut branch, the findings-count range, and the
// strict-with-findings branch. Uses the package's stdout-capture helper, so
// this test is not parallel.
func TestRunCIAuditFleetModeFiltersAndReportsFindings(t *testing.T) {
	root := t.TempDir()
	writeRepo := func(owner, name string, withGoFile bool) {
		repoPath := filepath.Join(root, owner, name)
		if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if withGoFile {
			if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// "app" matches the --filter substring and carries a .go file with no CI
	// coverage threshold, so it must produce a "go-coverage-threshold"
	// finding; "skip" does not match the filter and must be excluded by the
	// strings.Contains branch's continue.
	writeRepo("acme", "app", true)
	writeRepo("acme", "skip", false)

	var code int
	var err error
	output := cwCovCaptureStdout(t, func() {
		code, err = runCIAudit(".", root, "app", "", true, true, false)
	})
	if err != nil {
		t.Fatalf("runCIAudit() error = %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d; want 1 for a strict run with findings", code)
	}
	if !strings.Contains(output, "acme/app") || strings.Contains(output, "acme/skip") {
		t.Fatalf("text output = %q; want only the filtered acme/app repository", output)
	}
	if !strings.Contains(output, "go-coverage-threshold") {
		t.Fatalf("text output = %q; want the go-coverage-threshold finding", output)
	}

	// The same fleet, non-strict: findings are still reported but the exit
	// code must stay 0.
	var nonStrictCode int
	_ = cwCovCaptureStdout(t, func() {
		nonStrictCode, err = runCIAudit(".", root, "app", "", true, false, false)
	})
	if err != nil {
		t.Fatalf("runCIAudit(non-strict) error = %v", err)
	}
	if nonStrictCode != 0 {
		t.Fatalf("non-strict code = %d; want 0", nonStrictCode)
	}

	// jsonOut writes the same reports as JSON to stdout instead of the text
	// renderer.
	var jsonCode int
	jsonOutput := cwCovCaptureStdout(t, func() {
		jsonCode, err = runCIAudit(".", root, "app", "", true, false, true)
	})
	if err != nil {
		t.Fatalf("runCIAudit(json) error = %v", err)
	}
	if jsonCode != 0 {
		t.Fatalf("json code = %d; want 0", jsonCode)
	}
	var reports []ciaudit.Report
	if err := json.Unmarshal([]byte(jsonOutput), &reports); err != nil {
		t.Fatalf("decode JSON reports: %v; body=%s", err, jsonOutput)
	}
	if len(reports) != 1 || !strings.HasSuffix(reports[0].Path, filepath.Join("acme", "app")) {
		t.Fatalf("reports = %+v; want exactly the filtered acme/app repository", reports)
	}
}

// TestAuditReportsSortsTargetFindingsAndReports drives auditReports' two
// sort.Slice comparators: the per-report findings tie-break on matching code
// (falling back to File) and the outer reports-by-path ordering.
func TestAuditReportsSortsTargetFindingsAndReports(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	// second/first: intentionally out of alphabetical Path order so the
	// outer sort.Slice comparator (reports[i].Path < reports[j].Path) has
	// something to swap.
	compare := func(root, target string) ([]ciaudit.Finding, error) {
		return []ciaudit.Finding{
			{Code: "z-code", Message: "z finding", File: "z.yml"},
			// Same code as above with a lexically earlier file: exercises the
			// tie-break branch (Findings[i].Code == Findings[j].Code).
			{Code: "z-code", Message: "z finding earlier file", File: "a.yml"},
		}, nil
	}
	reports, err := auditReports([]string{second, first}, "main", compare)
	if err != nil {
		t.Fatalf("auditReports() error = %v", err)
	}
	if len(reports) != 2 || reports[0].Path >= reports[1].Path {
		t.Fatalf("reports paths = [%q, %q]; want ascending order", reports[0].Path, reports[1].Path)
	}
	findings := reports[0].Findings
	if len(findings) != 2 || findings[0].File != "a.yml" || findings[1].File != "z.yml" {
		t.Fatalf("findings = %+v; want the same-code tie broken by File", findings)
	}
}

// TestCIAuditCmdAcceptsExplicitPathAndReportsFindingsAsExitError drives the
// RunE `len(args) == 1` branch (an explicit repository-path argument) and the
// `code != 0` branch that turns a strict finding into an *exitError.
func TestCIAuditCmdAcceptsExplicitPathAndReportsFindingsAsExitError(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newCIAuditCmd(&invocation{})
	command.SetArgs([]string{"--strict", repoPath})
	var err error
	cwCovCaptureStdout(t, func() { err = command.Execute() })
	var exit *exitError
	if err == nil || !errors.As(err, &exit) {
		t.Fatalf("Execute() error = %v; want an *exitError", err)
	}
	if exit.code != 1 {
		t.Fatalf("exit.code = %d; want 1", exit.code)
	}
}
