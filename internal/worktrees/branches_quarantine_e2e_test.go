//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestQuarantineMetadataCommitAndAtomicRetirementRejectInvalidOrChangedRefs(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "feature/old")
	original := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	if _, err := quarantineMetadataCommit(context.Background(), fixture.canonical, "missing-parent", "retire: invalid test\n"); err == nil || !strings.Contains(err.Error(), "read preserved tree") {
		t.Fatalf("invalid metadata parent error = %v", err)
	}
	gitTest(t, fixture.canonical, "checkout", "main")
	competingOutput, err := gitTestRun(fixture.canonical, "commit-tree", original+"^{tree}", "-p", original, "-m", "competing source")
	if err != nil {
		t.Fatalf("create competing source: %v: %s", err, competingOutput)
	}
	competing := strings.TrimSpace(competingOutput)
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/feature/old", competing, original)
	if err := atomicLocalBranchRetirement(context.Background(), fixture.canonical, "feature/old", "retired/old", original, original); err == nil {
		t.Fatal("retirement CAS accepted a moved source")
	}
	if gitRefExists(fixture.canonical, "refs/heads/retired/old") || gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old") != competing {
		t.Fatal("failed retirement CAS changed either ref")
	}
}

func TestBranchQuarantineAnnotatesExactRetiredRefWithTreeIdenticalCommit(t *testing.T) {
	fixture := newGitFixture(t)
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	gitTest(t, fixture.canonical, "branch", "retired/old-analytics", original)
	reviewed := time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)
	options := BranchQuarantineOptions{ProjectsRoot: fixture.projectsRoot, Repository: "acme/app", Branch: "retired/old-analytics", SHA: original, Reason: "superseded by consent gating", Description: "obsolete analytics draft", Replacement: []string{"PR #113", "PR #117"}, Retention: "delete after verification", Annotate: true, Now: func() time.Time { return reviewed }}
	plan, err := BranchQuarantine(context.Background(), options)
	if err != nil || len(plan.Results) != 1 || plan.Results[0].Outcome != "planned" || plan.Results[0].SHA != original {
		t.Fatalf("annotation plan = %#v, %v", plan, err)
	}
	options.Apply, options.ReportDir = true, filepath.Join(t.TempDir(), "receipt")
	outcome, err := BranchQuarantine(context.Background(), options)
	if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "annotated" {
		t.Fatalf("annotation apply = %#v, %v", outcome, err)
	}
	result := outcome.Results[0]
	if result.SHA != original || result.OriginalSHA != original || result.RetiredSHA == original || result.ReceiptID == "" || result.ReviewedAt != reviewed {
		t.Fatalf("annotation identity = %#v", result)
	}
	if gitTestOutput(t, fixture.canonical, "rev-parse", "retired/old-analytics") != result.RetiredSHA || gitTestOutput(t, fixture.canonical, "rev-parse", result.RetiredSHA+"^") != original || gitTestOutput(t, fixture.canonical, "rev-parse", result.RetiredSHA+"^{tree}") != gitTestOutput(t, fixture.canonical, "rev-parse", original+"^{tree}") {
		t.Fatal("annotation did not retain the original parent and tree")
	}
	message := gitTestOutput(t, fixture.canonical, "show", "-s", "--format=%B", result.RetiredSHA)
	for _, text := range []string{"retire: obsolete analytics draft", "Reviewed at (UTC): 2026-10-21T12:30:00Z", "Superseded by: PR #113, PR #117", "Retention: delete after verification", "WB receipt: " + result.ReceiptID} {
		if !strings.Contains(message, text) {
			t.Fatalf("annotation message lacks %q: %s", text, message)
		}
	}
	var report BranchQuarantineOutcome
	raw, err := os.ReadFile(outcome.ReportPath)
	if err != nil || json.Unmarshal(raw, &report) != nil || report.Results[0].OriginalSHA != original || report.Results[0].RetiredSHA != result.RetiredSHA || report.Results[0].ReceiptID != result.ReceiptID {
		t.Fatalf("annotation report does not bind both SHAs and receipt: %+v, %v", report, err)
	}
}

//nolint:paralleltest // fixture owns Git configuration and a private gh executable.

func TestBranchQuarantineAnnotationCASFailureKeepsReceiptAndCompetingRef(t *testing.T) {
	fixture := newGitFixture(t)
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	ref := "retired/annotation-race"
	gitTest(t, fixture.canonical, "branch", ref, original)
	reviewed := time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC)
	options := BranchQuarantineOptions{ProjectsRoot: fixture.projectsRoot, Repository: "acme/app", Branch: ref, SHA: original, Reason: "replaced", Annotate: true, Apply: true, ReportDir: filepath.Join(t.TempDir(), "receipt"), Now: func() time.Time { return reviewed }}
	ops := realBranchQuarantineOps()
	competingOutput, err := gitTestRun(fixture.canonical, "commit-tree", original+"^{tree}", "-p", original, "-m", "competing annotation")
	if err != nil {
		t.Fatalf("create competing annotation: %v: %s", err, competingOutput)
	}
	competing := strings.TrimSpace(competingOutput)
	ops.annotate = func(ctx context.Context, path, branch, expected, replacement string) error {
		gitTest(t, path, "update-ref", "refs/heads/"+branch, competing, expected)
		return atomicLocalRetiredAnnotation(ctx, path, branch, expected, replacement)
	}
	outcome, err := branchQuarantineWithOps(context.Background(), options, ops, nil)
	if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "failed" || !strings.Contains(outcome.Results[0].Error, "local CAS annotation") {
		t.Fatalf("annotation race = %#v, %v", outcome, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", ref); got != competing {
		t.Fatalf("competing retired ref changed to %s, want %s", got, competing)
	}
	reportBytes, err := os.ReadFile(outcome.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report BranchQuarantineOutcome
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatal(err)
	}
	if report.Results[0].OriginalSHA != original || report.Results[0].RetiredSHA == "" || report.Results[0].RetiredSHA == competing || report.Results[0].ReceiptID == "" || report.Results[0].ReviewedAt != reviewed {
		t.Fatalf("CAS failure lost durable pre-mutation receipt: %+v", report.Results[0])
	}
}
