package orchestrate

import (
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestFailureIdentityOwnerCoverageParsingAndPlacement(t *testing.T) {
	t.Parallel()
	const header = "WB coverage failure index:\n"
	for _, row := range []struct {
		name, detail string
		want         map[string]struct{}
	}{
		{"no header", "ordinary failure", map[string]struct{}{}},
		{"malformed and empty", header + "noise\n- [x]bad\n- [] TestX\n- [x] \n", map[string]struct{}{}},
		{"duplicates and shard", header + "- [pkg shard 1/4] TestA\n- [pkg shard 3/8] TestA\n- [unsharded packages] TestB\n", map[string]struct{}{"pkg\x00TestA": {}, "unsharded packages\x00TestB": {}}},
		{"raw colon", header + "- [pkg shard 1/4] TestA\nWB coverage raw output:\n[pkg shard 1/4]\ntimed out after 5s\n", map[string]struct{}{"pkg\x00timed out": {}}},
		{"raw no colon", header + "- [pkg] TestA\nWB coverage raw output\n[pkg]\ntimed out after 5s\n", map[string]struct{}{"pkg\x00timed out": {}}},
		{"source survives folding", header + "- [pkg] TestA (attempt timeout; elapsed 3s)\nWB coverage raw output:\n[pkg]\ntimed out after 5s\n", map[string]struct{}{"pkg\x00TestA (attempt timeout)": {}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got := goCoverageFailureIdentities(row.detail)
			if !reflect.DeepEqual(got, row.want) {
				t.Fatalf("identities=%q want %q", got, row.want)
			}
		})
	}
	got := goCoverageTimedOutPlacements("timed out after 1s\nnoise\n[unsharded packages]\nno failure\n[pkg shard 2/4]\ntimed out after 2s\n[pkg-other]\nordinary output\n")
	if !reflect.DeepEqual(got, map[string]struct{}{"pkg": {}}) {
		t.Fatalf("timeout placements=%q", got)
	}
	if got := normalizeGoCoverageCommand("go test ./... (4 process-isolated shards for ./cmd/wb)"); got != "go test ./... (<process-isolated shards>)" {
		t.Fatalf("command=%q", got)
	}
}

func TestFailureIdentityOwnerTimeoutSources(t *testing.T) {
	t.Parallel()
	for _, cause := range []string{"attempt timeout", "check timeout", "caller timeout", "caller-cancelled cancellation"} {
		t.Run(cause, func(t *testing.T) {
			t.Parallel()
			got, ok := goCoverageTimeoutFailureIdentity("TestA (" + cause + "; elapsed 1.25s)")
			if !ok || got != "TestA ("+cause+")" {
				t.Fatalf("identity=%q/%t", got, ok)
			}
		})
	}
	for _, bad := range []string{"TestA", "(attempt timeout; elapsed 1s)", "TestA(attempt timeout; elapsed 1s)", "TestA (attempt timeout)", "TestA (other cause; elapsed 1s)", "TestA (attempt timeout; elapsed nope)"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			got, ok := goCoverageTimeoutFailureIdentity(bad)
			if ok || got != "" {
				t.Fatalf("invalid timeout accepted=%q/%t", got, ok)
			}
		})
	}
}

func TestFailureIdentityOwnerFormatSpecificBaselineContracts(t *testing.T) {
	t.Parallel()
	coverage := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Detail: detail}
	}
	spec := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "specscore", Module: ".", Check: quality.CheckSpec, Command: "specscore spec lint", Detail: detail}
	}
	const one = "WB coverage failure index:\n- [pkg] TestA\n"
	const two = "WB coverage failure index:\n- [pkg] TestA\n- [pkg] TestB\n"
	for _, row := range []struct {
		name      string
		baseline  []quality.VerificationEntry
		candidate quality.VerificationEntry
		want      bool
	}{
		{"coverage empty candidate", []quality.VerificationEntry{coverage(one)}, coverage("ordinary error"), false},
		{"coverage empty baseline", []quality.VerificationEntry{coverage("ordinary error")}, coverage(one), false},
		{"coverage subset", []quality.VerificationEntry{coverage(two)}, coverage(one), true},
		{"coverage new identity", []quality.VerificationEntry{coverage(one)}, coverage(two), false},
		{"coverage later baseline", []quality.VerificationEntry{coverage("ordinary error"), coverage(two)}, coverage(one), true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := matchGoCoverageBaselineFailure(row.baseline, row.candidate); got != row.want {
				t.Fatalf("match=%t want %t", got, row.want)
			}
		})
	}
	for _, field := range []string{"language", "module", "check", "command"} {
		t.Run("coverage metadata "+field, func(t *testing.T) {
			t.Parallel()
			base := coverage(one)
			switch field {
			case "language":
				base.Language = "node"
			case "module":
				base.Module = "other"
			case "check":
				base.Check = quality.CheckLint
			case "command":
				base.Command = "go test -race ./..."
			}
			if matchGoCoverageBaselineFailure([]quality.VerificationEntry{base}, coverage(one)) {
				t.Fatal("different metadata accepted")
			}
		})
	}
	const sone = "spec/features/a.md:12 rule-a: missing value\n"
	const stwo = sone + "spec/features/b.md:14-16 rule-b: missing value\n"
	for _, row := range []struct {
		name      string
		baseline  []quality.VerificationEntry
		candidate quality.VerificationEntry
		want      bool
	}{
		{"spec subset", []quality.VerificationEntry{spec(stwo)}, spec(sone), true},
		{"spec new rule", []quality.VerificationEntry{spec(sone)}, spec(stwo), false},
		{"spec empty both equal", []quality.VerificationEntry{spec("missing config")}, spec("missing config"), true},
		{"spec empty both changed", []quality.VerificationEntry{spec("missing config")}, spec("invalid config"), false},
		{"spec empty candidate", []quality.VerificationEntry{spec(sone)}, spec("missing config"), false},
		{"spec empty baseline", []quality.VerificationEntry{spec("missing config")}, spec(sone), false},
		{"spec later baseline", []quality.VerificationEntry{spec("missing config"), spec(stwo)}, spec(sone), true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := matchSpecScoreBaselineFailure(row.baseline, row.candidate); got != row.want {
				t.Fatalf("match=%t want %t", got, row.want)
			}
		})
	}
	for _, field := range []string{"language", "module", "check", "command"} {
		t.Run("spec metadata "+field, func(t *testing.T) {
			t.Parallel()
			base := spec(sone)
			switch field {
			case "language":
				base.Language = "go"
			case "module":
				base.Module = "other"
			case "check":
				base.Check = quality.CheckLint
			case "command":
				base.Command = "specscore spec verify"
			}
			if matchSpecScoreBaselineFailure([]quality.VerificationEntry{base}, spec(sone)) {
				t.Fatal("different metadata accepted")
			}
		})
	}
}

func TestFailureIdentityOwnerSpecParsingAndConcreteSubset(t *testing.T) {
	t.Parallel()
	detail := "noise\nspec/a.md:1 rule-a: detail\nspec/a.md:2-4 rule-a: changed\nspec/b.md:3 rule-b: value\n :3 rule-c: empty path\nspec/c.md:4  : empty rule\n"
	got := specScoreViolationIdentities(detail)
	want := map[string]struct{}{"spec/a.md rule-a": {}, "spec/b.md rule-b": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spec identities=%q want %q", got, want)
	}
	if !worktreeMergeFailureIdentitySubset(map[string]struct{}{"a": {}}, map[string]struct{}{"a": {}, "b": {}}) {
		t.Fatal("actual subset refused")
	}
	if worktreeMergeFailureIdentitySubset(map[string]struct{}{"new": {}}, map[string]struct{}{"a": {}}) {
		t.Fatal("unknown identity accepted")
	}
}
