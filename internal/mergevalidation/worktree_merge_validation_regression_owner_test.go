package mergevalidation

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// These contracts exercise typed report policy, not native ownership or lineage.
func TestValidationRegressionOwnerExactEvidenceSelection(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"valid", "passed empty", "missing", "duplicate", "wrong language", "wrong module", "wrong command", "wrong check", "failed nil", "passed evidence", "incomplete", "count", "empty identity", "duplicate identity", "legacy same", "legacy changed", "first invalid", "first unknown", "unmatched baseline"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := deadcodeFailureReport(DeadcodeCommand, "same", "known.A").Results[0]
			baseline := deadcodeFailureReport(DeadcodeCommand, "same", "known.A", "known.B").Results
			wantSet, wantLegacy := false, false
			switch name {
			case "valid":
				wantSet, wantLegacy = true, true
			case "passed empty":
				baseline[0].Status, baseline[0].Deadcode = quality.StatusPassed, nil
				wantSet, wantLegacy = true, true // Legacy ignores status and matches the unchanged detail.
			case "missing":
				baseline = nil
			case "duplicate":
				baseline = append(baseline, baseline[0])
				wantLegacy = true // Legacy intentionally uses the first matching entry.
			case "wrong language":
				baseline[0].Language = "node"
			case "wrong module":
				baseline[0].Module = "other"
			case "wrong command":
				baseline[0].Command += " --other"
			case "wrong check":
				baseline[0].Check = quality.CheckBuild
			case "failed nil":
				baseline[0].Deadcode = nil
				wantLegacy = true
			case "passed evidence":
				baseline[0].Status = quality.StatusPassed
				wantLegacy = true // Caller filters failed entries; direct legacy matcher does not.
			case "incomplete":
				baseline[0].Deadcode.Complete = false
			case "count":
				baseline[0].Deadcode.Count++
			case "empty identity":
				baseline[0].Deadcode.Identities[0] = ""
			case "duplicate identity":
				baseline[0].Deadcode.Identities[1] = baseline[0].Deadcode.Identities[0]
			case "legacy same":
				baseline[0].Deadcode = nil
				wantLegacy = true
			case "legacy changed":
				baseline[0].Deadcode, baseline[0].Detail = nil, "different"
			case "first invalid":
				valid := deadcodeFailureReport(DeadcodeCommand, "same", "known.A").Results[0]
				baseline[0].Deadcode.Complete = false
				baseline = append(baseline, valid)
			case "first unknown":
				baseline[0] = deadcodeFailureReport(DeadcodeCommand, "same", "other.C").Results[0]
				wantSet = true
			case "unmatched baseline":
				other := baseline[0]
				other.Module = "other"
				baseline = append([]quality.VerificationEntry{other}, baseline...)
				wantSet, wantLegacy = true, true
			}
			set, ok := worktreeMergeDeadcodeIdentitySet(baseline, "go", quality.CheckLint, DeadcodeCommand, ".")
			if ok != wantSet || (!ok && set != nil) {
				t.Fatalf("exact selection = (%v, %t), want valid=%t", set, ok, wantSet)
			}
			if name == "passed empty" && len(set) != 0 {
				t.Fatalf("passed nil evidence must give empty identity set: %v", set)
			}
			if got := matchDeadcodeBaselineFailure(baseline, candidate); got != wantLegacy {
				t.Fatalf("legacy first-match = %t, want %t", got, wantLegacy)
			}
		})
	}
}

func TestValidationRegressionOwnerImportedUnionAndMembership(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"union", "nil imported", "wrong language", "wrong check", "wrong command", "invalid candidate", "absent target", "invalid parent", "unknown", "ambiguous parent"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			target := deadcodeFailureReport(DeadcodeCommand, "target", "target.A")
			candidate := deadcodeFailureReport(DeadcodeCommand, "candidate", "target.A", "parent.B").Results[0]
			imported := &ImportedMainDeadcode{Validation: deadcodeFailureReport(DeadcodeCommand, "parent", "parent.B")}
			want := name == "union"
			switch name {
			case "nil imported":
				imported = nil
			case "wrong language":
				candidate.Language = "node"
			case "wrong check":
				candidate.Check = quality.CheckTest
			case "wrong command":
				candidate.Command += " --other"
			case "invalid candidate":
				candidate.Deadcode = nil
			case "absent target":
				target.Results = nil
			case "invalid parent":
				imported.Validation.Results[0].Deadcode.Count++
			case "unknown":
				candidate.Deadcode.Identities[1] = "unattested.C"
			case "ambiguous parent":
				imported.Validation.Results = append(imported.Validation.Results, imported.Validation.Results[0])
			}
			if got := matchImportedMainDeadcodeFailure(target.Results, candidate, imported); got != want {
				t.Fatalf("bounded union = %t, want %t", got, want)
			}
			if got := matchDeadcodeBaselineFailure(target.Results, candidate); got {
				t.Fatal("target alone must not admit parent-only identity or malformed metadata")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		ids   []string
		known map[string]bool
		want  bool
	}{
		{"empty", nil, nil, true},
		{"known", []string{"a", "b"}, map[string]bool{"a": true, "b": true}, true},
		{"missing", []string{"a", "b"}, map[string]bool{"a": true}, false},
		{"false value", []string{"a"}, map[string]bool{"a": false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := worktreeMergeDeadcodeIdentitiesKnown(tc.ids, tc.known); got != tc.want {
				t.Fatalf("membership=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestValidationRegressionOwnerMultisetAndDiagnosticOrder(t *testing.T) {
	t.Parallel()
	generic := quality.VerificationEntry{Language: "node", Module: "web", Check: quality.CheckBuild, Command: "build", Status: quality.StatusFailed, Detail: "failure 42"}
	for _, name := range []string{"equal", "duplicate candidate", "two baseline", "missing", "failed without entries", "passed aggregate with failed entry"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{generic}}
			candidate := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{generic}}
			want := ""
			switch name {
			case "duplicate candidate":
				candidate.Results = append(candidate.Results, generic)
				want = "candidate validation introduced or changed failure: node build build"
			case "two baseline":
				base.Results = append(base.Results, generic)
				candidate.Results = append(candidate.Results, generic)
			case "missing":
				base.Results = nil
				want = "candidate validation introduced or changed failure: node build build"
			case "failed without entries":
				candidate.Results = nil
				want = "candidate validation reported failure without failed check evidence"
			case "passed aggregate with failed entry":
				candidate.Status = quality.StatusPassed
			}
			err := worktreeMergeValidationRegression(base, candidate)
			if want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != want {
				t.Fatalf("regression = %v, want %q", err, want)
			}
		})
	}
	base := deadcodeFailureReport(DeadcodeCommand, "target", "known.A")
	candidate := deadcodeFailureReport(DeadcodeCommand, "candidate", "new.Z", "known.A", "new.B")
	delta, ok := worktreeMergeDeadcodeTargetDelta(base.Results, candidate.Results[0])
	if !ok || !reflect.DeepEqual(delta, []string{"new.Z", "new.B"}) {
		t.Fatalf("candidate-order delta=(%v,%t)", delta, ok)
	}
	if err := worktreeMergeValidationRegression(base, candidate); err == nil || err.Error() != "candidate validation has 2 deadcode finding(s) absent from exact target: new.Z, new.B" {
		t.Fatalf("delta diagnostic = %v", err)
	}
}

func TestValidationRegressionOwnerLazyAttestationPolicy(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"covered", "ordinary failure", "mixed failure", "error", "partial refusal", "success", "nil evidence"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := deadcodeFailureReport(DeadcodeCommand, "target", "target.A")
			candidate := deadcodeFailureReport(DeadcodeCommand, "candidate", "parent.B")
			proof := &ImportedMainDeadcode{Validation: deadcodeFailureReport(DeadcodeCommand, "parent", "parent.B")}
			ordinary := quality.VerificationEntry{Language: "node", Check: quality.CheckBuild, Command: "build", Status: quality.StatusFailed, Detail: "new"}
			sentinel := errors.New("controlled attestation refusal")
			calls := 0
			wantCalls := 1
			switch name {
			case "covered":
				candidate = deadcodeFailureReport(DeadcodeCommand, "candidate", "target.A")
				wantCalls = 0
			case "ordinary failure":
				candidate.Results = []quality.VerificationEntry{ordinary}
				wantCalls = 0
			case "mixed failure":
				candidate.Results = append(candidate.Results, ordinary)
				wantCalls = 0
			case "partial refusal":
				proof.Validation.Results[0].Deadcode.Complete = false
			case "nil evidence":
				proof = nil
			}
			before := append([]quality.VerificationEntry(nil), candidate.Results...)
			got, err := WithImportedMainAttestation(base, candidate, func() (*ImportedMainDeadcode, error) {
				calls++
				if name == "error" {
					return proof, sentinel
				}
				return proof, nil
			})
			if calls != wantCalls || !reflect.DeepEqual(before, candidate.Results) {
				t.Fatalf("lazy calls=%d want=%d, candidate mutated=%t", calls, wantCalls, !reflect.DeepEqual(before, candidate.Results))
			}
			switch name {
			case "covered":
				if got != nil || err != nil {
					t.Fatalf("covered=(%+v,%v)", got, err)
				}
			case "success":
				if got != proof || err != nil {
					t.Fatalf("success=(%+v,%v)", got, err)
				}
			case "error":
				if got != nil || !errors.Is(err, sentinel) || err.Error() != "attest imported main deadcode baseline: "+sentinel.Error() {
					t.Fatalf("wrapped error=(%+v,%v)", got, err)
				}
			case "partial refusal":
				if got != proof || err == nil {
					t.Fatalf("partial evidence=(%+v,%v)", got, err)
				}
			default:
				if got != nil || err == nil {
					t.Fatalf("refusal=(%+v,%v)", got, err)
				}
			}
		})
	}
}

func TestValidationRegressionOwnerFailureIdentityAndFiltering(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"same", "language", "module", "check", "command", "detail"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			base := quality.VerificationEntry{Language: "node", Module: "web", Check: quality.CheckBuild, Command: "build", Detail: "semantic 42"}
			candidate := base
			switch field {
			case "language":
				candidate.Language = "go"
			case "module":
				candidate.Module = "other"
			case "check":
				candidate.Check = quality.CheckTest
			case "command":
				candidate.Command = "other"
			case "detail":
				candidate.Detail = "semantic 43"
			}
			if got := sameWorktreeMergeFailure(base, candidate); got != (field == "same") {
				t.Fatalf("same=%t, field=%s", got, field)
			}
		})
	}
	dead := deadcodeFailureReport(DeadcodeCommand, "candidate", "new.A")
	passing := quality.VerificationEntry{Status: quality.StatusPassed, Language: "node", Command: "build"}
	dead.Results = append(dead.Results, passing)
	if !hasWorktreeMergeDeadcodeFailure(dead) || hasWorktreeMergeDeadcodeFailure(quality.VerificationReport{Results: []quality.VerificationEntry{passing}}) {
		t.Fatal("deadcode presence scan")
	}
	before := append([]quality.VerificationEntry(nil), dead.Results...)
	if err := worktreeMergeNonDeadcodeRegression(quality.VerificationReport{Status: quality.StatusPassed}, dead); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dead.Results) || dead.Status != quality.StatusFailed {
		t.Fatal("filter mutated caller report")
	}
	failed := passing
	failed.Status, failed.Detail = quality.StatusFailed, "new ordinary failure"
	dead.Results = append(dead.Results, failed)
	if err := worktreeMergeNonDeadcodeRegression(quality.VerificationReport{}, dead); err == nil {
		t.Fatal("ordinary failure lost during deadcode filtering")
	}
	entries := failedWorktreeMergeVerificationEntries(dead)
	if len(entries) != 2 || entries[0].Deadcode == nil || entries[1].Command != failed.Command {
		t.Fatalf("failed entry order=%+v", entries)
	}
}

func TestValidationRegressionOwnerNormalizationBoundaries(t *testing.T) {
	t.Parallel()
	absolute := filepath.Join(t.TempDir(), "tree", "app.go")
	for _, tc := range []struct{ name, input, want string }{
		{"quoted path", `"` + absolute + `",`, `"<workspace>",`},
		{"wrapped path", "(" + absolute + ");", "(<workspace>);"},
		{"relative path", "(tree/app.go);", "(tree/app.go);"},
		{"punctuation", "[]{}.,", "[]{}.,"},
		{"empty", "", ""},
		{"meaningful suffix", "tree/app.go:42", "tree/app.go:42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeWorktreeMergeFailureField(tc.input); got != tc.want {
				t.Fatalf("field=%q want=%q", got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, input, want string }{
		{"no newline", "raw", "raw"},
		{"empty line", "marker\n", "marker\n"},
		{"built suffix", "marker\nilt in 1.2ms", "marker\nbuilt in <duration>"},
		{"completed suffix", "marker\nleted in 4s", "marker\ncompleted in <duration>"},
		{"unrelated word", "marker\npainted in 4s", "marker\npainted in 4s"},
		{"semantic count", "marker\ncompleted in 42 records", "marker\ncompleted in 42 records"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeWorktreeMergeFailureTruncatedTail(tc.input); got != tc.want {
				t.Fatalf("tail=%q want=%q", got, tc.want)
			}
		})
	}
	first := "12:01:59 [vite] Generated 1µs built in 2ms Completed in 3s (+4s)\nsemantic timeout 5s records 42"
	second := "23:59:59 [vite] Generated 7µs built in 8ms Completed in 9s (+10s)\nsemantic timeout 5s records 42"
	if normalizeWorktreeMergeFailureDetail(first) != normalizeWorktreeMergeFailureDetail(second) {
		t.Fatal("volatile presentation did not normalize")
	}
	if normalizeWorktreeMergeFailureDetail(first) == normalizeWorktreeMergeFailureDetail(strings.ReplaceAll(second, "timeout 5s", "timeout 6s")) {
		t.Fatal("semantic timeout erased")
	}
	if got := normalizeWorktreeMergeFailureDetail("25:01:59 [vite] records 42"); got != "25:01:59 [vite] records 42" {
		t.Fatalf("invalid timestamp erased: %q", got)
	}
}
