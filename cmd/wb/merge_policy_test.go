package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestApplySharedRulesetSourceTypeMustBeRepository proves the
// audit-only guard in applySharedRuleset's injected implementation: a
// non-Repository source type is refused before any GitHub read.
func TestApplySharedRulesetSourceTypeMustBeRepository(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, nil)
	err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Organization", Source: "acme", ID: 9})
	if err == nil || !strings.Contains(err.Error(), "unsupported and audit-only") {
		t.Fatalf("err = %v", err)
	}
}

// TestApplySharedRulesetRejectsRulesetWithoutRulesField proves the
// "rules" field type assertion failure branch: a ruleset body with no rules
// array is refused rather than silently doing nothing.
func TestApplySharedRulesetRejectsRulesetWithoutRulesField(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, func(_ int, endpoint string) []byte {
		if strings.Contains(endpoint, "/rulesets/") {
			return []byte(`{"name": "no rules here"}`)
		}
		return nil
	})
	err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7})
	if err == nil || !strings.Contains(err.Error(), "no rules") {
		t.Fatalf("err = %v", err)
	}
}

// TestApplySharedRulesetSkipsNonObjectRuleEntries proves a rule element
// that is not a JSON object (the `value.(map[string]any)` !ok branch) is
// passed through unchanged rather than rejected, while a sibling
// required_linear_history rule still drives the ruleset to a real change.
func TestApplySharedRulesetSkipsNonObjectRuleEntries(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, func(_ int, endpoint string) []byte {
		if strings.Contains(endpoint, "/rulesets/") {
			return []byte(`{"name": "mixed", "target": "branch", "rules": ["not-an-object", {"type": "required_linear_history"}]}`)
		}
		return nil
	})
	if err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7}); err != nil {
		t.Fatalf("applySharedRuleset: %v", err)
	}
}

// TestApplySharedRulesetInitializesMissingPullRequestParameters proves
// the `rule["parameters"].(map[string]any)` !ok branch: a pull_request rule
// with no parameters object still gets one created and allowed_merge_methods
// set on it.
func TestApplySharedRulesetInitializesMissingPullRequestParameters(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, func(_ int, endpoint string) []byte {
		if strings.Contains(endpoint, "/rulesets/") {
			return []byte(`{"name": "no-params", "target": "branch", "rules": [{"type": "pull_request"}]}`)
		}
		return nil
	})
	if err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7}); err != nil {
		t.Fatalf("applySharedRuleset: %v", err)
	}
}

// TestApplySharedRulesetRefusesWhenNothingToChange proves the terminal
// !changed guard: a ruleset with neither a required_linear_history nor a
// pull_request rule has nothing for this seam to change.
func TestApplySharedRulesetRefusesWhenNothingToChange(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, func(_ int, endpoint string) []byte {
		if strings.Contains(endpoint, "/rulesets/") {
			return []byte(`{"name": "unrelated", "target": "branch", "rules": [{"type": "deletion"}]}`)
		}
		return nil
	})
	err := applySharedRuleset(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7})
	if err == nil || !strings.Contains(err.Error(), "no required-linear-history or pull-request policy") {
		t.Fatalf("err = %v", err)
	}
}

// TestApplyMergePolicySkipsNonDriftRepositoriesInLeaseRecheck proves the
// `repo.Disposition != "drift"` continue branch in applyMergePolicy's
// pre-mutation lease recheck: a compliant repository is left untouched.
func TestApplyMergePolicySkipsNonDriftRepositoriesInLeaseRecheck(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, nil)
	report := mergePolicyReport{Repositories: []mergePolicyRepository{
		{Repository: "acme/clean", Disposition: "compliant"},
	}}
	var out bytes.Buffer
	if err := applyMergePolicy(context.Background(), &report, 0, &out); err != nil {
		t.Fatalf("applyMergePolicy: %v", err)
	}
	if report.Repositories[0].Disposition != "compliant" {
		t.Errorf("compliant repository was mutated: %+v", report.Repositories[0])
	}
}

// TestApplyMergePolicyNormalizesNonPositiveParallelism proves the
// `parallel < 1` normalization branch runs (and does not panic or deadlock)
// when a caller passes a non-positive worker count directly.
func TestApplyMergePolicyNormalizesNonPositiveParallelism(t *testing.T) {
	cwDepsStubMergePolicyGitHub(t, nil)
	report := mergePolicyReport{}
	var out bytes.Buffer
	if err := applyMergePolicy(context.Background(), &report, -3, &out); err != nil {
		t.Fatalf("applyMergePolicy: %v", err)
	}
}
