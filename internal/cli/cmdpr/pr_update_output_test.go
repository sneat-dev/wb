package cmdpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestPRUpdateReportsExactReceiptInTextAndJSON(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	result := orchestrate.PullRequestUpdateResult{
		Repository: "acme/app", PullRequest: "42", Status: "updated", BeforeSHA: "before", AfterSHA: "after",
		TargetBeforeSHA: "target-before", TargetCurrentSHA: "target-after", LocalSync: "fast-forwarded", ReceiptPath: "/receipts/42.json",
	}
	var requested orchestrate.PullRequestUpdateOptions
	deps.Update = func(_ context.Context, options orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
		requested = options
		return result, nil
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			command := NewUpdate(testRuntime(), deps)
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetArgs([]string{"acme/app#42", "--format=" + format})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if requested.Repository != "acme/app" || requested.PullRequest != "42" {
				t.Fatalf("options = %+v", requested)
			}
			if format == "json" {
				var decoded orchestrate.PullRequestUpdateResult
				if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded != result {
					t.Fatalf("decoded = %+v, want %+v", decoded, result)
				}
			} else if !strings.Contains(stdout.String(), "PR acme/app#42: updated; before -> after; target target-before -> target-after; local: fast-forwarded; receipt: /receipts/42.json") {
				t.Fatalf("text result = %q", stdout.String())
			}
		})
	}
}

func TestPRUpdateReturnsFindingsAfterPublishingPartialReceipt(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	for _, status := range []string{"updated_partial", "unverified"} {
		t.Run(status, func(t *testing.T) {
			deps.Update = func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
				return orchestrate.PullRequestUpdateResult{Repository: "acme/app", PullRequest: "42", Status: status, ReceiptPath: "/receipts/42.json"}, nil
			}
			command := NewUpdate(testRuntime(), deps)
			command.SilenceUsage, command.SilenceErrors = true, true
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetArgs([]string{"acme/app#42"})
			err := command.Execute()
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != exitFindings {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(stdout.String(), status) || !strings.Contains(stdout.String(), "/receipts/42.json") {
				t.Fatalf("missing durable receipt output: %q", stdout.String())
			}
		})
	}
}

func TestPRUpdateKeepsReceiptVisibleWhenUpdateFails(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	want := errors.New("target moved during verification")
	for _, receipt := range []string{"", "/receipts/42.json"} {
		t.Run(receipt, func(t *testing.T) {
			deps.Update = func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
				return orchestrate.PullRequestUpdateResult{Repository: "acme/app", PullRequest: "42", Status: "unverified", ReceiptPath: receipt}, want
			}
			command := NewUpdate(testRuntime(), deps)
			command.SilenceUsage, command.SilenceErrors = true, true
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetArgs([]string{"acme/app#42"})
			err := command.Execute()
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != exitFindings || !strings.Contains(exit.Error(), want.Error()) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(stdout.String(), "/receipts/42.json") != (receipt != "") {
				t.Fatalf("stdout = %q, receipt = %q", stdout.String(), receipt)
			}
		})
	}
}
