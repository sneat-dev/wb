package streamrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/quality"
)

func TestBatchVerifierMapsChecksAndFailureEvidence(t *testing.T) {
	t.Parallel()
	called := false
	verifier := batchVerifier{timeout: 3 * time.Second, verify: func(_ context.Context, repository, path string, checks []quality.Check, options quality.RunOptions) quality.VerificationReport {
		called = true
		if repository != "/fixture" || path != "/fixture" || !reflect.DeepEqual(checks, []quality.Check{quality.CheckLint, quality.CheckBuild, quality.CheckTest}) || options.Timeout != 3*time.Second || !options.SingleWorker || !containsString(options.Env, "CI=1") {
			t.Errorf("verification inputs: repository=%q path=%q checks=%v options=%+v", repository, path, checks, options)
		}
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{Module: "app", Check: quality.CheckLint, Command: "go vet ./...", Status: quality.StatusPassed}, {Module: "app", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusFailed, Detail: "compiler error"}, {Module: "app", Check: quality.CheckTest, Command: "", Status: quality.StatusSkipped}}}
	}}
	run, err := verifier.Verify(context.Background(), "/fixture")
	if err != nil || !called || run.Passed || run.Command != "go vet ./...; go build ./..." ||
		!reflect.DeepEqual(run.Details, []string{"app build: compiler error"}) ||
		!reflect.DeepEqual(run.Skipped, []string{"-race"}) || run.Duration < 0 {
		t.Fatalf("verification run = %+v, called=%t, error=%v", run, called, err)
	}
}
func TestBatchVerifierTreatsNonfailedReportAsPassed(t *testing.T) {
	t.Parallel()
	verifier := batchVerifier{verify: func(context.Context, string, string, []quality.Check, quality.RunOptions) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusSkipped, Results: []quality.VerificationEntry{{Status: quality.StatusSkipped, Command: "unused"}}}
	}}
	run, err := verifier.Verify(context.Background(), t.TempDir())
	if err != nil || !run.Passed || run.Command != "" || len(run.Details) != 0 {
		t.Fatalf("skipped verification = %+v, error=%v", run, err)
	}
}
func TestVerifierDefaultAndWorkflowEvidenceErrorBoundaries(t *testing.T) {
	t.Parallel()
	if _, err := (batchVerifier{timeout: time.Second}).Verify(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("workflow read failed")
	adapter := workflowMechanisms{concurrency: func(string) ([]ciaudit.Concurrency, error) { return nil, failure }}
	if _, _, err := adapter.Present("/fixture"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	adapter.concurrency = func(string) ([]ciaudit.Concurrency, error) {
		return []ciaudit.Concurrency{{Workflow: "push.yml"}, {Workflow: "pr.yml", PullRequest: true}}, nil
	}
	adapter.mechanisms = func(_ string, workflow string) (map[string]bool, bool, error) {
		if workflow != "pr.yml" {
			t.Fatal("push evidence consumed")
		}
		return nil, false, failure
	}
	if _, _, err := adapter.Present("/fixture"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	adapter.mechanisms = func(string, string) (map[string]bool, bool, error) {
		return map[string]bool{"go test": true}, true, nil
	}
	present, opaque, err := adapter.Present("/fixture")
	if err != nil || !opaque || !present["go test"] {
		t.Fatal(present, opaque, err)
	}
}
