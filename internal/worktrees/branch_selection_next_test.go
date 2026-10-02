package worktrees

import (
	"reflect"
	"testing"
)

func TestBranchNamingNormalizationPreservesPresenceAndValidationOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   branchNamingOptions
		want    branchNamingOptions
		wantErr string
	}{
		{name: "omitted"},
		{name: "legacy branch", input: branchNamingOptions{ExactBranch: " feature/task "}, want: branchNamingOptions{ExactBranch: "feature/task", ExactBranchChosen: true}},
		{name: "legacy prefix", input: branchNamingOptions{CLIPrefix: "feature/"}, want: branchNamingOptions{CLIPrefix: "feature/", CLIPrefixChosen: true}},
		{name: "explicit empty prefix", input: branchNamingOptions{CLIPrefixChosen: true}, want: branchNamingOptions{CLIPrefixChosen: true}},
		{name: "explicit empty branch", input: branchNamingOptions{ExactBranchChosen: true}, wantErr: "--branch must not be empty when explicitly provided"},
		{name: "legacy padded empty branch", input: branchNamingOptions{ExactBranch: " "}, wantErr: "--branch must not be empty when explicitly provided"},
		{name: "prefix before conflict", input: branchNamingOptions{ExactBranch: "feature/task", CLIPrefix: " feature/"}, wantErr: "branch prefix must not have surrounding whitespace"},
		{name: "conflict before empty branch", input: branchNamingOptions{ExactBranchChosen: true, CLIPrefixChosen: true}, wantErr: "--branch and --branch-prefix cannot be used together"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeBranchNamingOptions(tc.input)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || !reflect.DeepEqual(got, branchNamingOptions{}) {
					t.Fatalf("got=%+v err=%v want=%q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%+v err=%v want=%+v", got, err, tc.want)
			}
		})
	}
	// Selection does not rewrite the DTO's distinct derivation inputs.
	input := branchNamingOptions{Task: "task", Base: "main", BaseRevision: "tip", Canonical: &canonicalRepository{}}
	if got, err := normalizeBranchNamingOptions(input); err != nil || !reflect.DeepEqual(got, input) {
		t.Fatalf("derivation inputs changed=%+v err=%v", got, err)
	}
}
