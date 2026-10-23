package cmdpr

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestPRLandPassesBaselineAndAggregateWaivers(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	var got orchestrate.PullRequestLandOptions
	deps.Land = func(_ context.Context, options orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
		got = options
		return orchestrate.PullRequestLandResult{Outcome: orchestrate.LandSuccess}, nil
	}
	var out, errOut bytes.Buffer
	code := executeTest(NewLand(testRuntime(), deps), []string{"acme/app#7",
		"--waive-check", "e2e", "--waive-aggregate", "deploy-gate", "--waive-baseline", "abc1234", "--waive-reason", "red on public-demo",
		"--non-interactive"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.WaiveBaseline != "abc1234" || strings.Join(got.WaiveAggregates, ",") != "deploy-gate" || strings.Join(got.WaiveChecks, ",") != "e2e" {
		t.Fatalf("options = baseline %q aggregates %v checks %v", got.WaiveBaseline, got.WaiveAggregates, got.WaiveChecks)
	}
}

func TestPRLandRejectsBaselineWithoutWaivedCheck(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := executeTest(NewLand(testRuntime(), testDependencies()), []string{"acme/app#7",
		"--waive-aggregate", "deploy-gate", "--waive-baseline", "abc1234", "--waive-reason", "r"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "--waive-baseline requires --waive-check") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestPRLandAggregateWaiverNeedsAReason(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := executeTest(NewLand(testRuntime(), testDependencies()), []string{"acme/app#7", "--waive-aggregate", "deploy-gate"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "requires a non-empty --waive-reason") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestWaivedCheckBasisNamesBaselineOrAggregation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		waived orchestrate.WaivedCheck
		want   string
	}{
		{orchestrate.WaivedCheck{BaselineSHA: "1111111111111111", BaselineIsTip: true}, ""},
		{orchestrate.WaivedCheck{BaselineSHA: "2222222222222222"}, ", baseline 222222222222"},
		{orchestrate.WaivedCheck{WaivedBy: "aggregation", Inputs: []string{"build", "e2e"}}, ", waived by aggregation of build, e2e"},
	}
	for _, tc := range cases {
		if got := waivedCheckBasis(tc.waived); got != tc.want {
			t.Errorf("basis = %q, want %q", got, tc.want)
		}
	}
}

func TestPRLandRejectsANonSHABaseline(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := executeTest(NewLand(testRuntime(), testDependencies()), []string{"acme/app#7",
		"--waive-check", "e2e", "--waive-baseline", "main; rm -rf", "--waive-reason", "r"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "7-40 character hex") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}
