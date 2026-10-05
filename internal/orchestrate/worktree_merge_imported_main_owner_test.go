package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/mergevalidation"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// Failure selection is per invocation; every positive result is native.
type importedMainOwnerRunner struct {
	runner.Runner
	dir           string
	args          []string
	seen, ordinal int
	cause         error
	after         func(context.Context, string, []string, runner.Result)
}

func (r *importedMainOwnerRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && dir == r.dir && reflect.DeepEqual(args, r.args) {
		r.seen++
		if r.seen == r.ordinal && r.cause != nil {
			return runner.Result{}, r.cause
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if err == nil && result.ExitCode == 0 && r.after != nil {
		r.after(ctx, dir, args, result)
	}
	return result, err
}

func TestImportedMainOwnerExactReportContracts(t *testing.T) {
	t.Parallel()
	failed := deadcodeFailureReport(mergevalidation.DeadcodeCommand, "native tool wire contract", "main.A", "main.B")
	passed := quality.VerificationReport{Results: []quality.VerificationEntry{{Language: "go", Check: quality.CheckLint, Command: mergevalidation.DeadcodeCommand, Status: quality.StatusPassed, Module: "."}}}
	for _, row := range []struct {
		name         string
		mutate       func(*quality.VerificationReport)
		valid, equal bool
	}{
		{"same", func(*quality.VerificationReport) {}, true, true},
		{"foreign ignored", func(r *quality.VerificationReport) {
			r.Results = append(r.Results, quality.VerificationEntry{Language: "other", Check: quality.CheckLint, Command: mergevalidation.DeadcodeCommand})
		}, true, true},
		{"duplicate", func(r *quality.VerificationReport) { r.Results = append(r.Results, r.Results[0]) }, false, false},
		{"missing", func(r *quality.VerificationReport) { r.Results = nil }, false, false},
		{"module", func(r *quality.VerificationReport) { r.Results[0].Module = "other" }, true, false},
		{"status", func(r *quality.VerificationReport) { r.Results[0].Status = quality.StatusSkipped }, false, false},
		{"incomplete", func(r *quality.VerificationReport) { r.Results[0].Deadcode.Complete = false }, false, false},
		{"count", func(r *quality.VerificationReport) { r.Results[0].Deadcode.Count++ }, false, false},
		{"different valid count", func(r *quality.VerificationReport) {
			r.Results[0].Deadcode.Identities = r.Results[0].Deadcode.Identities[:1]
			r.Results[0].Deadcode.Count = 1
		}, true, false},
		{"identity", func(r *quality.VerificationReport) { r.Results[0].Deadcode.Identities[0] = "main.C" }, true, false},
		{"reordered", func(r *quality.VerificationReport) {
			r.Results[0].Deadcode.Identities[0], r.Results[0].Deadcode.Identities[1] = r.Results[0].Deadcode.Identities[1], r.Results[0].Deadcode.Identities[0]
		}, true, true},
		{"duplicate identity", func(r *quality.VerificationReport) {
			r.Results[0].Deadcode.Identities[1] = r.Results[0].Deadcode.Identities[0]
		}, false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			current := failed
			current.Results = append([]quality.VerificationEntry(nil), failed.Results...)
			d := *failed.Results[0].Deadcode
			d.Identities = append([]string(nil), d.Identities...)
			current.Results[0].Deadcode = &d
			row.mutate(&current)
			if got := mergevalidation.ValidImportedMainDeadcodeReport(current); got != row.valid {
				t.Fatalf("valid=%t want=%t %+v", got, row.valid, current)
			}
			if got := sameImportedMainDeadcodeEvidence(failed, current); got != row.equal {
				t.Fatalf("equal=%t want=%t %+v", got, row.equal, current)
			}
		})
	}
	if !mergevalidation.ValidImportedMainDeadcodeReport(passed) || !sameImportedMainDeadcodeEvidence(passed, passed) {
		t.Fatal("exact passed entry rejected")
	}
	withDeadcode := passed
	withDeadcode.Results = append([]quality.VerificationEntry(nil), passed.Results...)
	withDeadcode.Results[0].Deadcode = failed.Results[0].Deadcode
	if !mergevalidation.ValidImportedMainDeadcodeReport(withDeadcode) || sameImportedMainDeadcodeEvidence(passed, withDeadcode) || sameImportedMainDeadcodeEvidence(withDeadcode, passed) {
		t.Fatal("passed validity and nil-deadcode equality contracts collapsed")
	}
	plainFailed := failed
	plainFailed.Results = append([]quality.VerificationEntry(nil), failed.Results...)
	plainFailed.Results[0].Deadcode = nil
	if sameImportedMainDeadcodeEvidence(plainFailed, failed) || sameImportedMainDeadcodeEvidence(failed, plainFailed) {
		t.Fatal("invalid failure compared equal")
	}
}

func TestImportedMainOwnerLookupAndNoEvidenceContracts(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ fetch, remote, want string }{{"", "", "malformed"}, {"a b", "a refs/heads/main", "malformed"}, {"a", "a refs/heads/other", "malformed"}, {"a", "b refs/heads/main", "moved"}, {"a\n", "a refs/heads/main\n", ""}} {
		got, err := matchedFetchedOriginMain(row.fetch, row.remote)
		if row.want == "" {
			if err != nil || got != "a" {
				t.Fatalf("matched=%q %v", got, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("error=%v want=%s", err, row.want)
		}
	}
	for _, pair := range [][2]string{{"", "a"}, {"a", " "}} {
		if err := requireGitAncestor(t.Context(), defaultRunner, t.TempDir(), pair[0], pair[1]); err == nil {
			t.Fatal("empty ancestry accepted")
		}
	}
	if err := recheckWorktreeMergeImportedMainDeadcode(t.Context(), WorktreeMergeReceipt{}, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("selected initial graph failure")
	run := &importedMainOwnerRunner{Runner: defaultRunner, dir: t.TempDir(), args: []string{"rev-list", "--parents", "-n", "1", "missing"}, ordinal: 1, cause: cause}
	r := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{SHA: "missing", Worktree: run.dir}, TargetSHA: "target"}
	if evidence, err := worktreeMergeImportedMainDeadcodeWithRunner(t.Context(), run, &r, 0, 0, 0); evidence != nil || !errors.Is(err, cause) || run.seen != 1 {
		t.Fatalf("evidence=%+v error=%v consumed=%d", evidence, err, run.seen)
	}
}
