package cmdpr

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"strings"
	"testing"
)

func TestLegacyAdoptionCLIUsesExplicitApplyAndSafeProjection(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var got orchestrate.LegacyAdoptOptions
			deps := IntegrationDependencies{AdoptLegacy: func(_ context.Context, o orchestrate.LegacyAdoptOptions) (orchestrate.LegacyAdoptResult, error) {
				got = o
				return orchestrate.LegacyAdoptResult{Record: orchestrate.LegacyProvenance{CandidateSHA: "candidate"}, Binding: &orchestrate.LegacyBinding{URL: "record", Digest: "digest"}}, nil
			}}
			var out, errOut bytes.Buffer
			code := executeTest(NewIntegration(testRuntime(), deps), []string{"adopt-legacy", "acme/app", "integration", "--receipt", "/private/receipt", "--worktree", "/checkout", "--to", "main", "--apply", "--format", format}, &out, &errOut)
			if code != 0 || got.Repository != "acme/app" || got.Integration != "integration" || got.Receipt != "/private/receipt" || got.Worktree != "/checkout" || got.To != "main" || !got.Apply || !strings.Contains(out.String(), "candidate") {
				t.Fatal(code, got, out.String(), errOut.String())
			}
		})
	}
}
func TestLegacyAdoptionCLIRejectsMissingReceiptAndUnsupportedFormat(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"adopt-legacy", "acme/app", "integration"}, {"adopt-legacy", "acme/app", "integration", "--receipt", "x", "--format", "yaml"}, {"adopt-legacy", "acme/app"}} {
		var out, errOut bytes.Buffer
		if executeTest(NewIntegration(testRuntime(), IntegrationDependencies{}), args, &out, &errOut) == 0 {
			t.Fatal("accepted", args)
		}
	}
	deps := IntegrationDependencies{AdoptLegacy: func(context.Context, orchestrate.LegacyAdoptOptions) (orchestrate.LegacyAdoptResult, error) {
		return orchestrate.LegacyAdoptResult{}, errors.New("invalid receipt")
	}}
	var out, errOut bytes.Buffer
	if executeTest(NewIntegration(testRuntime(), deps), []string{"adopt-legacy", "acme/app", "integration", "--receipt", "x"}, &out, &errOut) == 0 {
		t.Fatal("ignored adoption error")
	}
}

func TestLegacyAdoptionCLIDryRunUsesCurrentCheckoutAndDefaultDriver(t *testing.T) {
	t.Parallel()
	var got orchestrate.LegacyAdoptOptions
	deps := IntegrationDependencies{AdoptLegacy: func(_ context.Context, o orchestrate.LegacyAdoptOptions) (orchestrate.LegacyAdoptResult, error) {
		got = o
		return orchestrate.LegacyAdoptResult{}, nil
	}}
	var out, errOut bytes.Buffer
	if code := executeTest(NewIntegration(testRuntime(), deps), []string{"adopt-legacy", "acme/app", "integration", "--receipt", "not-used"}, &out, &errOut); code != 0 || got.Apply || got.Worktree == "" {
		t.Fatal(code, got, errOut.String())
	}
	if code := executeTest(NewIntegration(testRuntime(), IntegrationDependencies{}), []string{"adopt-legacy", "invalid", "integration", "--receipt", "missing"}, &out, &errOut); code == 0 {
		t.Fatal("default driver did not validate")
	}
}

func TestLegacyAdoptionCLIReportsOutputFailures(t *testing.T) {
	t.Parallel()
	deps := IntegrationDependencies{AdoptLegacy: func(context.Context, orchestrate.LegacyAdoptOptions) (orchestrate.LegacyAdoptResult, error) {
		return orchestrate.LegacyAdoptResult{Binding: &orchestrate.LegacyBinding{}}, nil
	}}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			command := NewIntegration(testRuntime(), deps)
			command.SetOut(failingWriter{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs([]string{"adopt-legacy", "acme/app", "integration", "--receipt", "unused", "--format", format})
			if err := command.Execute(); err == nil {
				t.Fatal("output error lost")
			}
		})
	}
	command := NewIntegration(testRuntime(), deps)
	command.SetOut(&failAtCallWriter{failAt: 2})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"adopt-legacy", "acme/app", "integration", "--receipt", "unused"})
	if err := command.Execute(); err == nil {
		t.Fatal("binding output error lost")
	}
}

func TestLegacyRecordFlagsReachBothPromotionAndDelta(t *testing.T) {
	t.Parallel()
	var got orchestrate.PromoteOptions
	deps := IntegrationDependencies{Promote: func(_ context.Context, o orchestrate.PromoteOptions) (orchestrate.PromoteResult, error) {
		got = o
		return orchestrate.PromoteResult{Outcome: orchestrate.LandSuccess}, nil
	}}
	var out, errOut bytes.Buffer
	if code := executeTest(NewIntegration(testRuntime(), deps), []string{"promote", "acme/app", "integration", "--legacy-record", "https://record=sha256:digest"}, &out, &errOut); code != 0 || len(got.LegacyRecords) != 1 {
		t.Fatal(code, got, errOut.String())
	}
}
