package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

// TestSweepAppliesStrictConfigPromotingReportFindings covers sweep's
// context.config.Strict branch: a repository whose .wb-deps-policy.yaml
// sets strict: true must have its report-mode findings promoted to
// blocking, not just left as report-only.
func TestSweepAppliesStrictConfigPromotingReportFindings(t *testing.T) {
	t.Parallel()
	root := violatingModule(t)
	policyPath := writeTestPolicy(t)
	config := "policy: " + policyPath + "\nstrict: true\n"
	if err := os.WriteFile(filepath.Join(root, ".wb-deps-policy.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{}
	outcomes := sweep(inv, []deps.Repository{{Slug: "acme/cal", Path: root}}, "")
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want exactly one module", outcomes)
	}
	outcome := outcomes[0]
	if !outcome.Governed {
		t.Fatalf("outcome not governed: %+v", outcome)
	}
	if outcome.Blocking == 0 {
		t.Fatalf("strict config did not promote any finding to blocking: %+v", outcome)
	}
}

// TestSweepSortsOutcomesByRepositoryThenDirectory covers sweep's
// sort.Slice directory tiebreak: two modules under the same repository
// slug must land ordered by their module directory.
func TestSweepSortsOutcomesByRepositoryThenDirectory(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	// rootA/rootB sort however t.TempDir() names them; force a known order
	// by nesting one further so its absolute path always sorts first.
	first, second := rootA, rootB
	if first > second {
		first, second = second, first
	}
	for _, root := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/dup\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inv := &invocation{}
	// Neither module has a policy declaration, so each is recorded with
	// Skipped set — sweep never needs a real policy fixture to populate
	// Repository and Directory, which is all this sort exercises.
	outcomes := sweep(inv, []deps.Repository{
		{Slug: "acme/dup", Path: second},
		{Slug: "acme/dup", Path: first},
	}, "")
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want two modules", outcomes)
	}
	if outcomes[0].Directory != first || outcomes[1].Directory != second {
		t.Fatalf("outcomes not sorted by directory: %+v", outcomes)
	}
}

// TestWriteBucketsSortsByCountThenKey covers writeBuckets' sort
// comparator on both sides: a count difference (descending) and, for two
// buckets with an EQUAL count, the key tiebreak.
func TestWriteBucketsSortsByCountThenKey(t *testing.T) {
	t.Parallel()
	buckets := map[string]*findingBucket{
		"b-rule": {count: 2, repos: map[string]bool{"acme/b": true}},
		"a-rule": {count: 2, repos: map[string]bool{"acme/a": true}},
		"z-rule": {count: 5, repos: map[string]bool{"acme/z": true}},
	}
	var out bytes.Buffer
	writeBuckets(&out, "enforcing", buckets)
	text := out.String()
	zIndex := strings.Index(text, "z-rule")
	aIndex := strings.Index(text, "a-rule")
	bIndex := strings.Index(text, "b-rule")
	if zIndex < 0 || aIndex < 0 || bIndex < 0 {
		t.Fatalf("writeBuckets output missing a bucket:\n%s", text)
	}
	if zIndex >= aIndex || aIndex >= bIndex {
		t.Fatalf("writeBuckets did not sort by count desc then key asc:\n%s", text)
	}
}
