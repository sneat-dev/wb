package locallink

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
)

// lgCovVerifier is a Verifier whose every method can be made to fail, which is
// what verifyConsumers' error branches need.
type lgCovVerifier struct {
	verifyErr   error
	baselineErr error
	linked      VerificationRun
	baseline    VerificationRun
	envSeen     []string
}

func (verifier *lgCovVerifier) Verify(_ context.Context, _ string, env []string) (VerificationRun, error) {
	verifier.envSeen = env
	return verifier.linked, verifier.verifyErr
}

func (verifier *lgCovVerifier) BuildAndVet(context.Context, string) (VerificationRun, error) {
	return verifier.baseline, verifier.baselineErr
}

func lgCovLinkedResult(consumers ...ConsumerResult) Result {
	return Result{Library: "/library", LibraryRepository: "acme/library", ContentHash: "hash", Consumers: consumers}
}

func TestLgCovVerifyConsumersWithoutAVerifier(t *testing.T) {
	engine := &Engine{}
	result := lgCovLinkedResult(
		ConsumerResult{Consumer: t.TempDir(), Links: []streams.Link{lgCovGoWorkLink("/library")}},
		ConsumerResult{Consumer: t.TempDir(), Skipped: true},
		ConsumerResult{Consumer: t.TempDir()},
	)
	engine.verifyConsumers(context.Background(), Options{}, &result)
	if len(result.Consumers[0].Errors) != 1 || result.Consumers[0].Errors[0] != "no verifier available" {
		t.Fatalf("errors = %#v, want the missing-verifier report", result.Consumers[0].Errors)
	}
	if len(result.Consumers[1].Errors) != 0 || len(result.Consumers[2].Errors) != 0 {
		t.Fatalf("consumers = %#v, want skipped and unlinked consumers left alone", result.Consumers)
	}
}

func TestLgCovVerifyConsumersReportsBothFailures(t *testing.T) {
	verifier := &lgCovVerifier{
		verifyErr:   errors.New("lint failed to run"),
		baselineErr: errors.New("vet failed to run"),
	}
	engine := &Engine{Verifier: verifier}
	result := lgCovLinkedResult(ConsumerResult{Consumer: t.TempDir(), Links: []streams.Link{lgCovGoWorkLink("/library")}})
	engine.verifyConsumers(context.Background(), Options{}, &result)

	if len(result.Consumers[0].Errors) != 2 {
		t.Fatalf("errors = %#v, want both verifier failures reported", result.Consumers[0].Errors)
	}
	if !containsSubstring(result.Consumers[0].Errors, "lint failed to run") || !containsSubstring(result.Consumers[0].Errors, "vet failed to run") {
		t.Fatalf("errors = %#v, want both messages", result.Consumers[0].Errors)
	}
	if result.Consumers[0].Verification == nil || result.Consumers[0].Verification.Passed {
		t.Fatalf("verification = %#v, want a recorded non-passing run", result.Consumers[0].Verification)
	}
	if len(verifier.envSeen) == 0 {
		t.Fatal("the single-worker environment was not passed to Verify")
	}
}

func TestLgCovVerifyConsumersSkipsUnlinkedConsumers(t *testing.T) {
	verifier := &lgCovVerifier{
		linked:   VerificationRun{Passed: true, Command: "go test -p 1 ./..."},
		baseline: VerificationRun{Passed: true, Command: "GOWORK=off go build ./..."},
	}
	engine := &Engine{Verifier: verifier}
	result := lgCovLinkedResult(
		ConsumerResult{Consumer: t.TempDir(), Skipped: true},
		ConsumerResult{Consumer: t.TempDir()},
		ConsumerResult{Consumer: t.TempDir(), Links: []streams.Link{lgCovGoWorkLink("/library")}},
	)
	engine.verifyConsumers(context.Background(), Options{}, &result)
	if result.Consumers[0].Verification != nil || result.Consumers[1].Verification != nil {
		t.Fatalf("consumers = %#v, want skipped and unlinked consumers left unverified", result.Consumers)
	}
	if len(result.Consumers[0].Errors) != 0 || len(result.Consumers[1].Errors) != 0 {
		t.Fatalf("consumers = %#v, want no errors attributed to work never attempted", result.Consumers)
	}
	if result.Consumers[2].Verification == nil || !result.Consumers[2].Verification.Passed {
		t.Fatalf("consumers[2] = %#v, want the linked consumer verified", result.Consumers[2])
	}
}

func TestLgCovLibraryNameStatementAndActiveLinks(t *testing.T) {
	if got := (Result{Library: "/library"}).libraryName(); got != "/library" {
		t.Fatalf("libraryName = %q, want the library path when no repository is known", got)
	}
	if got := (Result{Library: "/library", LibraryRepository: "acme/library"}).libraryName(); got != "acme/library" {
		t.Fatalf("libraryName = %q, want the repository name", got)
	}

	if got := LocalGateStatement("acme/library", "hash", false); !strings.HasSuffix(got, "(clean)") {
		t.Fatalf("statement = %q, want a clean-tree statement", got)
	}
	if got := LocalGateStatement("acme/library", "hash", true); !strings.HasSuffix(got, "(dirty)") {
		t.Fatalf("statement = %q, want a dirty-tree statement", got)
	}

	rendered := renderActiveLinks([]streams.Link{
		{Identity: "@acme/core", Mechanism: streams.MechanismPnpmLink, PreviousVersion: "1.2.3", ContentHash: "hash"},
		{Identity: "github.com/acme/library", Mechanism: streams.MechanismGoWork, ContentHash: "hash"},
	})
	if len(rendered) != 2 {
		t.Fatalf("rendered = %#v, want one line per link", rendered)
	}
	if !strings.Contains(rendered[0], "replaces 1.2.3") {
		t.Fatalf("rendered[0] = %q, want the recorded previous version", rendered[0])
	}
	if !strings.Contains(rendered[1], "replaces (none declared)") {
		t.Fatalf("rendered[1] = %q, want the explicit no-version marker", rendered[1])
	}
}

func TestLgCovSummarizeRendersCommandsAndDetails(t *testing.T) {
	report := quality.VerificationReport{
		Status: quality.StatusFailed,
		Results: []quality.VerificationEntry{
			{Module: "backend", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusPassed},
			{Module: "backend", Check: quality.CheckLint, Command: "go vet ./...", Status: quality.StatusFailed, Detail: "vet found issues"},
			{Module: "frontend", Check: quality.CheckTest, Status: quality.StatusSkipped},
			{Module: "backend", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusPassed},
		},
	}
	run := summarize(report)
	if run.Passed {
		t.Fatal("a failed report summarised as passing")
	}
	if run.Command != "go build ./...; go vet ./..." {
		t.Fatalf("command = %q, want deduped commands in order", run.Command)
	}
	if len(run.Details) != 1 || !strings.Contains(run.Details[0], "vet found issues") {
		t.Fatalf("details = %#v, want only the failed check's detail", run.Details)
	}

	passing := summarize(quality.VerificationReport{Status: quality.StatusPassed})
	if !passing.Passed || passing.Command != "" || len(passing.Details) != 0 {
		t.Fatalf("passing summary = %#v, want a clean pass", passing)
	}
}

// The production verifier wraps quality.VerifyWithOptions; a directory with no
// recognised module or manifest exercises its request construction and
// summarisation without touching the network.
func TestLgCovQualityVerifierRequestsBothProfiles(t *testing.T) {
	dir := t.TempDir()
	lgCovWriteFile(t, filepath.Join(dir, "backend", "go.mod"), "module example.test/backend\n\ngo 1.27\n")
	lgCovWriteFile(t, filepath.Join(dir, "backend", "backend.go"), "package backend\n")

	verifier := QualityVerifier{}
	ctx := context.Background()

	run, err := verifier.Verify(ctx, dir, []string{"WB_LGCOV_MARKER=1"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if run.Command == "" && !run.Passed {
		t.Fatalf("run = %#v, want a summarised result", run)
	}

	baseline, err := verifier.BuildAndVet(ctx, dir)
	if err != nil {
		t.Fatalf("BuildAndVet: %v", err)
	}
	if !strings.HasPrefix(baseline.Command, "GOWORK=off ") && baseline.Command != "GOWORK=off " {
		t.Fatalf("baseline command = %q, want the GOWORK=off prefix", baseline.Command)
	}
}
