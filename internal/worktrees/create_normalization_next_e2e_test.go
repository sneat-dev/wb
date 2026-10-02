//go:build e2e

package worktrees

import (
	"reflect"
	"strings"
	"testing"
)

func TestE2ECreateNormalizationPreservesExplicitBranchContracts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name    string
		options CreateOptions
		wantErr string
	}{
		{"missing root", CreateOptions{Operation: "task"}, "projects root is required"},
		{"unsafe operation", CreateOptions{ProjectsRoot: root, Operation: "../task"}, "one safe path segment"},
		{"padded prefix", CreateOptions{ProjectsRoot: root, Operation: "task", BranchPrefix: " feature/"}, "surrounding whitespace"},
		{"conflicting flags", CreateOptions{ProjectsRoot: root, Operation: "task", Branch: "feature/task", BranchPrefixChosen: true}, "cannot be used together"},
		{"explicit empty branch", CreateOptions{ProjectsRoot: root, Operation: "task", BranchChosen: true}, "must not be empty"},
		{"padded empty branch", CreateOptions{ProjectsRoot: root, Operation: "task", Branch: " "}, "must not be empty"},
		{"invalid base", CreateOptions{ProjectsRoot: root, Operation: "task", Base: "invalid base"}, "invalid base branch"},
		{"invalid feature", CreateOptions{ProjectsRoot: root, Operation: "task", Branch: "invalid feature"}, "invalid feature branch"},
		{"feature equals base", CreateOptions{ProjectsRoot: root, Operation: "task", Base: "main", Branch: "main"}, "must differ from base branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeCreateOptions(tc.options)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("normalize = %+v, %v; want refusal %q", got, err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, CreateOptions{}) {
				t.Fatalf("refused options escaped normalization: %+v", got)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		options CreateOptions
		want    CreateOptions
	}{
		{"defaults", CreateOptions{ProjectsRoot: root, Operation: " task "}, CreateOptions{ProjectsRoot: root, Operation: "task", Base: "main"}},
		{"legacy exact branch", CreateOptions{ProjectsRoot: root, Operation: "task", Base: " main ", Branch: " feature/task "}, CreateOptions{ProjectsRoot: root, Operation: "task", Base: "main", Branch: "feature/task", BranchChosen: true}},
		{"legacy prefix", CreateOptions{ProjectsRoot: root, Operation: "task", BranchPrefix: "feature/"}, CreateOptions{ProjectsRoot: root, Operation: "task", Base: "main", BranchPrefix: "feature/", BranchPrefixChosen: true}},
		{"explicit empty prefix", CreateOptions{ProjectsRoot: root, Operation: "task", BranchPrefixChosen: true}, CreateOptions{ProjectsRoot: root, Operation: "task", Base: "main", BranchPrefixChosen: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeCreateOptions(tc.options)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalize = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
