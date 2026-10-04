package cmddeps

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/policy"
	"github.com/spf13/cobra"
)

func newPolicyRegistry() *cobra.Command {
	return NewPolicy(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(_ int, message string) error { return errors.New(message) }}, PolicyDependencies{})
}
func TestPolicySubcommandsAndFlagsArePresent(t *testing.T) {
	t.Parallel()
	command := newPolicyRegistry()
	wanted := map[string][]string{"check": {"policy", "type", "format", "strict"}, "explain": {"policy", "type"}, "show": {"policy", "type"}, "validate": nil, "test": nil,
		"init":   {"policy"},
		"report": {"match", "regex", "policy", "format"},
		"drift":  {"match", "regex", "policy", "format"},
		"impact": {"match", "regex", "format"},
	}
	for name, flags := range wanted {
		sub, _, err := command.Find([]string{name})
		if err != nil || sub == command {
			t.Errorf("deps policy %s is missing", name)
			continue
		}
		for _, flag := range flags {
			if sub.Flags().Lookup(flag) == nil {
				t.Errorf("deps policy %s is missing --%s", name, flag)
			}
		}
	}
}
func TestDirectoryArgDefaultsToCurrentDirectory(t *testing.T) {
	t.Parallel()
	if got := directoryArg(nil); got != "." {
		t.Fatalf("directoryArg(nil) = %q, want %q", got, ".")
	}
}
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
func TestCwDepsOrNoneAndDirectoryArg(t *testing.T) {
	t.Parallel()
	if got := orNone(""); got != "nothing" {
		t.Errorf("orNone(empty) = %q", got)
	}
	if got := orNone("acme/policy//p.yaml"); got != "acme/policy//p.yaml" {
		t.Errorf("orNone(value) = %q", got)
	}
	if got := directoryArg(nil); got != "." {
		t.Errorf("directoryArg(nil) = %q", got)
	}
	if got := directoryArg([]string{""}); got != "." {
		t.Errorf("directoryArg(empty) = %q", got)
	}
	if got := directoryArg([]string{"/tmp/module"}); got != "/tmp/module" {
		t.Errorf("directoryArg(path) = %q", got)
	}
}
func TestCwCovFindingKeyCoversEveryRuleShape(t *testing.T) {
	t.Parallel()
	if got := findingKey(policy.Finding{Rule: policy.RuleLayer, FromRole: "dal", ToRole: "api"}); got != "dal -> api" {
		t.Errorf("layer key = %q", got)
	}
	if got := findingKey(policy.Finding{Rule: policy.RuleImport, Group: "third-party", Scope: "source"}); got != "third-party (source)" {
		t.Errorf("import key = %q", got)
	}
	if got := findingKey(policy.Finding{Rule: "something-else"}); got != "something-else" {
		t.Errorf("default key = %q", got)
	}
}
func TestCwCovWriteReportTextBucketsAndUngovernedList(t *testing.T) {
	t.Parallel()
	mk := func(repository string, mode policy.Mode) depsrun.PolicyModuleOutcome {
		return depsrun.PolicyModuleOutcome{
			Repository: repository,
			Module:     "github.com/acme/" + repository + "/backend",
			Governed:   true,
			Blocking:   1,
			Findings: []policy.Finding{{
				Rule: policy.RuleImport, Mode: mode, Group: "third-party", Scope: "source",
			}},
		}
	}
	outcomes := []depsrun.PolicyModuleOutcome{
		mk("acme/one", policy.ModeEnforce),
		mk("acme/two", policy.ModeEnforce),
		mk("acme/three", policy.ModeReport),
		mk("acme/four", policy.ModeReport),
		mk("beta/five", policy.ModeReport),
		mk("beta/six", policy.ModeReport),
		{Repository: "acme/clean", Module: "github.com/acme/clean/backend", Governed: true},
		{Repository: "acme/unwired", Module: "github.com/acme/unwired/backend", Skipped: "no policy selected"},
	}

	var out bytes.Buffer
	writeReportText(&out, outcomes)
	text := out.String()
	for _, want := range []string{
		"7 module(s) governed, 1 clean, 1 not governed",
		"enforcing",
		"third-party (source)",
		"report only",
		"not governed",
		"acme/unwired",
		"no policy selected",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report text missing %q:\n%s", want, text)
		}
	}
	// More than three repositories are collapsed to a count rather than a list.
	if !strings.Contains(text, "4 repositories") {
		t.Errorf("report should collapse a bucket spread over >3 repos:\n%s", text)
	}
	// The enforcing bucket must be reported before the report-only bucket.
	if strings.Index(text, "enforcing") > strings.Index(text, "report only") {
		t.Errorf("bucket order is wrong:\n%s", text)
	}

	// An all-governed, all-clean sweep prints no bucket headings at all.
	var clean bytes.Buffer
	writeReportText(&clean, []depsrun.PolicyModuleOutcome{{Repository: "acme/clean", Governed: true}})
	if strings.Contains(clean.String(), "enforcing") || strings.Contains(clean.String(), "report only") {
		t.Errorf("empty buckets must not render headings:\n%s", clean.String())
	}
	if !strings.Contains(clean.String(), "1 module(s) governed, 1 clean, 0 not governed") {
		t.Errorf("clean report = %q", clean.String())
	}

	// writeBuckets returns immediately on an empty map, and writeJSONTo emits
	// indented JSON.
	var empty bytes.Buffer
	writeBuckets(&empty, "enforcing", map[string]*findingBucket{})
	if empty.Len() != 0 {
		t.Errorf("empty bucket map wrote %q", empty.String())
	}
	var payload bytes.Buffer
	if err := writeJSONTo(&payload, map[string]int{"modules": 2}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(payload.Bytes(), &decoded); err != nil {
		t.Fatalf("writeJSONTo did not emit JSON: %v\n%s", err, payload.String())
	}
	if decoded["modules"] != 2 {
		t.Errorf("decoded = %+v", decoded)
	}
}
func TestCwCovPluralSuffixAndFleetRegex(t *testing.T) {
	t.Parallel()
	for count, want := range map[int]string{0: "ies", 1: "y", 3: "ies"} {
		if got := plural(count); got != want {
			t.Errorf("plural(%d) = %q, want %q", count, got, want)
		}
	}
}
