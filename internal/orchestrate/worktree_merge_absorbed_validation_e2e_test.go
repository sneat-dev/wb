//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestE2EAbsorbedConflictDerivedPathsReadImmutableTargetObjects(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	repo := fixture.canonical
	commit := func(paths []string) string {
		t.Helper()
		for _, path := range paths {
			full := filepath.Join(repo, path)
			if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("generated index\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		runEngineGit(t, repo, "add", "-A")
		runEngineGit(t, repo, "commit", "-m", "add derived indexes")
		return strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	}
	plans, features, tasks := "spec/plans/README.md", "spec/features/README.md", "spec/tasks/README.md"
	oldTarget := commit([]string{plans, features})
	target := commit([]string{tasks})
	t.Cleanup(func() {
		if got := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD")); got != target {
			t.Errorf("HEAD=%s want frozen %s", got, target)
		}
		if got := runEngineGit(t, repo, "status", "--porcelain"); got != "" {
			t.Errorf("immutable corpus changed: %s", got)
		}
	})
	cases := []struct {
		name       string
		raw        []string
		revision   string
		want       []string
		nilSet     bool
		diagnostic string
	}{
		{name: "nil input", revision: target, nilSet: true},
		{name: "empty input", raw: []string{}, revision: target, nilSet: true},
		{name: "blank paths", raw: []string{"", " \t ", "./"}, revision: target},
		{name: "trim prefix and deduplicate", raw: []string{" ./" + plans + " \n", plans, "./" + plans}, revision: target, want: []string{plans}},
		{name: "sort complete set", raw: []string{tasks, plans, features, tasks}, revision: target, want: []string{features, plans, tasks}},
		{name: "disallowed docs", raw: []string{"docs/README.md"}, revision: target, diagnostic: "is not an allowed derived-index shape"},
		{name: "traversal", raw: []string{"../spec/plans/README.md"}, revision: target, diagnostic: "is not an allowed derived-index shape"},
		{name: "absolute", raw: []string{"/spec/plans/README.md"}, revision: target, diagnostic: "is not an allowed derived-index shape"},
		{name: "only one dot prefix removed", raw: []string{"././" + plans}, revision: target, diagnostic: "is not an allowed derived-index shape"},
		{name: "allowed missing path", raw: []string{"spec/absent/README.md"}, revision: target, diagnostic: "does not exist on the current target " + target},
		{name: "present checkout absent immutable target", raw: []string{tasks}, revision: oldTarget, diagnostic: "does not exist on the current target " + oldTarget},
		{name: "present new target", raw: []string{tasks}, revision: target, want: []string{tasks}},
		{name: "invalid revision collapses to absence", raw: []string{plans}, revision: "missing-target-object", diagnostic: "does not exist on the current target missing-target-object"},
		{name: "no partial results after absence", raw: []string{plans, "spec/absent/README.md"}, revision: target, diagnostic: "does not exist on the current target " + target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := slices.Clone(tc.raw)
			got, set, err := validateAbsorbedConflictDerivedPaths(context.Background(), repo, tc.revision, tc.raw)
			if !reflect.DeepEqual(tc.raw, before) {
				t.Fatalf("input changed: %q -> %q", before, tc.raw)
			}
			if tc.diagnostic != "" {
				if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
					t.Fatalf("error=%v want %q", err, tc.diagnostic)
				}
				if got != nil || set != nil {
					t.Fatalf("refusal leaked results %q %v", got, set)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths=%q want %q error=%v", got, tc.want, err)
			}
			if tc.nilSet {
				if set != nil {
					t.Fatalf("empty input set=%v want nil", set)
				}
				return
			}
			wantSet := make(map[string]bool, len(tc.want))
			for _, path := range tc.want {
				wantSet[path] = true
			}
			if !reflect.DeepEqual(set, wantSet) {
				t.Fatalf("set=%v want %v", set, wantSet)
			}
		})
	}
}
