package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFailedJobLogExcerptAndActionsLink(t *testing.T) {
	t.Parallel()
	runID, jobID, ok := githubActionsRunAndJob("https://github.com/acme/app/actions/runs/123456/job/7890")
	if !ok || runID != "123456" || jobID != "7890" {
		t.Fatalf("actions link parsed as run=%q job=%q ok=%t", runID, jobID, ok)
	}
	if _, _, ok := githubActionsRunAndJob("https://github.com/acme/app/checks/1"); ok {
		t.Fatal("non-Actions check link was accepted")
	}

	lines := make([]string, 0, maxFailedJobLogLines+2)
	for index := 0; index < maxFailedJobLogLines+2; index++ {
		lines = append(lines, fmt.Sprintf("line %d", index))
	}
	lines = append(lines, "token ghp_secretTokenShouldNotAppear")
	excerpt := failedJobLogExcerpt(strings.Join(lines, "\n"), maxFailedJobLogLines)
	for _, want := range []string{"… earlier failed-job log lines omitted …", "line 2", "[REDACTED]"} {
		if !strings.Contains(excerpt, want) {
			t.Errorf("excerpt missing %q: %q", want, excerpt)
		}
	}
	if strings.Contains(excerpt, "ghp_secretTokenShouldNotAppear") {
		t.Fatalf("excerpt leaked token-shaped content: %q", excerpt)
	}
}

func TestSortRemoteChecksUsesProducerAsFinalDeterministicKey(t *testing.T) {
	checks := []RemoteCheck{
		{Name: "build", Bucket: "pass", Link: "https://example.test/build", AppID: 22},
		{Name: "build", Bucket: "pass", Link: "https://example.test/build", AppID: 11},
	}
	sortRemoteChecks(checks)
	if got := []int64{checks[0].AppID, checks[1].AppID}; !reflect.DeepEqual(got, []int64{11, 22}) {
		t.Fatalf("sorted producer IDs = %v", got)
	}
}

func TestWaitForCommitChecksRejectsSliceAboveForegroundCeiling(t *testing.T) {
	_, err := WaitForCommitChecks(context.Background(), PullRequestWaitOptions{
		Repository: "acme/app", Target: "main", Head: "0123456789012345678901234567890123456789",
		Slice: MaxForegroundCheckWaitSlice + time.Second, CheckPollInterval: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("overlong wait error = %v", err)
	}
}

func TestGitHubChecksPollIntervalDefaultsToQuotaAwareCadence(t *testing.T) {
	if got := githubChecksPollInterval(Options{}); got != DefaultCheckPollInterval {
		t.Fatalf("default GitHub check poll interval = %s, want %s", got, DefaultCheckPollInterval)
	}
	if DefaultCheckPollInterval != 30*time.Second {
		t.Fatalf("quota-aware default = %s, want 30s", DefaultCheckPollInterval)
	}
}

func TestStableRereadDelayNeverExceedsThePollInterval(t *testing.T) {
	if got := stableRereadDelay(DefaultCheckPollInterval, 0); got != DefaultStableRereadDelay {
		t.Fatalf("stable reread delay under the default cadence = %s, want %s", got, DefaultStableRereadDelay)
	}
	if got := stableRereadDelay(100*time.Millisecond, 0); got != 100*time.Millisecond {
		t.Fatalf("a poll interval shorter than the confirmation delay must win, got %s", got)
	}
	if got := stableRereadDelay(DefaultCheckPollInterval, 3*time.Second); got != 3*time.Second {
		t.Fatalf("a configured confirmation delay must win over the default, got %s", got)
	}
	if got := stableRereadDelay(time.Second, 3*time.Second); got != time.Second {
		t.Fatalf("a configured delay is still capped by the poll interval, got %s", got)
	}
	if DefaultStableRereadDelay >= DefaultCheckPollInterval {
		t.Fatalf("confirmation delay %s must undercut the quota-aware poll cadence %s", DefaultStableRereadDelay, DefaultCheckPollInterval)
	}
}

func TestTargetBranchRequiredChecksTreatsOnlyEmptyClassic404AsRulesetOnly(t *testing.T) {
	for _, test := range []struct {
		name          string
		branchSummary string
		classicDetail string
		classicExit   int
		rules         string
		wantChecks    []RequiredRemoteCheck
		wantFreshness string
		wantReason    string
	}{
		{
			name:          "empty classic 404 uses inherited and repository rulesets",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{}}}`,
			classicDetail: "gh: Not Found (HTTP 404)",
			classicExit:   1,
			rules: `[
{"type":"required_status_checks","ruleset_source_type":"Organization","ruleset_source":"acme","ruleset_id":3,"parameters":{"required_status_checks":[{"context":"Inherited","integration_id":7}]}},
{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"CI","integration_id":42}]}}
]`,
			wantChecks:    []RequiredRemoteCheck{{Name: "CI", IntegrationID: 42}, {Name: "Inherited", IntegrationID: 7}},
			wantFreshness: "strict required-status-check ruleset 7",
		},
		{
			name:          "empty classic 404 without ruleset leaves no strict fence",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{}}}`,
			classicDetail: "gh: Not Found (HTTP 404)",
			classicExit:   1,
			rules:         `[]`,
			wantChecks:    []RequiredRemoteCheck{},
		},
		{
			name:          "empty classic non-404 remains an authority error",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{}}}`,
			classicDetail: "gh: Forbidden (HTTP 403)",
			classicExit:   1,
			rules:         `[]`,
			wantReason:    "read authoritative required-status-check policy",
		},
		{
			name:          "populated classic contexts reject detail 404 before valid ruleset",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{"contexts":["Classic"]}}}`,
			classicDetail: "gh: Not Found (HTTP 404)",
			classicExit:   1,
			rules:         `[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"Ruleset CI","integration_id":42}]}}]`,
			wantReason:    "read authoritative required-status-check policy",
		},
		{
			name:          "populated App-pinned classic checks reject detail 404 before valid ruleset",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"Classic","app_id":99}]}}}`,
			classicDetail: "gh: Not Found (HTTP 404)",
			classicExit:   1,
			rules:         `[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"Ruleset CI","integration_id":42}]}}]`,
			wantReason:    "read authoritative required-status-check policy",
		},
		{
			name:          "populated classic policy remains authoritative",
			branchSummary: `{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"Summary","app_id":55}]}}}`,
			classicDetail: `{"strict":true,"contexts":[],"checks":[{"context":"Classic","app_id":99}]}`,
			rules:         `[]`,
			wantChecks:    []RequiredRemoteCheck{{Name: "Classic", IntegrationID: 99}},
			wantFreshness: "classic strict required-status-check policy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo "$WB_BRANCH_SUMMARY"; exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main/protection/required_status_checks' ]; then
  echo "$WB_CLASSIC_DETAIL"
  exit "$WB_CLASSIC_EXIT"
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  if echo "$*" | grep -Fq -- '--slurp'; then echo 'active rules must not use --slurp: gh 2.45 has no such flag' >&2; exit 31; fi
  echo "$WB_ACTIVE_RULES"; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
			path := filepath.Join(bin, "gh")
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("WB_BRANCH_SUMMARY", test.branchSummary)
			t.Setenv("WB_CLASSIC_DETAIL", test.classicDetail)
			t.Setenv("WB_CLASSIC_EXIT", fmt.Sprint(test.classicExit))
			t.Setenv("WB_ACTIVE_RULES", test.rules)

			checks, freshness, reason := targetBranchRequiredChecks(context.Background(), "acme/app", "main", true)
			if test.wantReason != "" {
				if !strings.Contains(reason, test.wantReason) {
					t.Fatalf("reason = %q, want %q", reason, test.wantReason)
				}
				return
			}
			if reason != "" || !reflect.DeepEqual(checks, test.wantChecks) || freshness != test.wantFreshness {
				encoded, _ := json.Marshal(checks)
				t.Fatalf("checks=%s freshness=%q reason=%q", encoded, freshness, reason)
			}
		})
	}
}
