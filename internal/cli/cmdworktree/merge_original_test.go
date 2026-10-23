package cmdworktree

import (
	"bytes"
	"encoding/json"
	"errors"
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorktreeMergeForcedProgressIsNewlineDelimited(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	writer := cliprogress.Output(&output, false)
	for _, text := range []string{"\rworktree merge: preparing", "\rworktree merge: waiting", "\n"} {
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "worktree merge: preparing\nworktree merge: waiting\n"; got != want {
		t.Fatalf("progress output = %q, want %q", got, want)
	}
}

func TestWorktreeMergeCommandExposesCombinedAndTwoPhaseJourney(t *testing.T) {
	t.Parallel()
	command := newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{}))
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("Use = %q", command.Use)
	}
	for _, flag := range []string{"target", "route", "cleanup", "on-failure", "allow-unfenced", "format", "progress", "prepare-timeout", "check-timeout", "shard-attempt-timeout"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("combined merge is missing --%s", flag)
		}
	}
	if command.Flags().Lookup("rebatch-receipt") != nil {
		t.Error("combined merge must not expose --rebatch-receipt; rebatching is a prepare-only transition")
	}
	prepare, _, err := command.Find([]string{"prepare"})
	if err != nil || prepare == nil || prepare.Flags().Lookup("rebatch-receipt") == nil {
		t.Fatalf("merge prepare must expose --rebatch-receipt: command=%v err=%v", prepare, err)
	}
	if prepare.Flags().Lookup("allow-unfenced") != nil {
		t.Fatal("merge prepare must not expose landing-only --allow-unfenced")
	}
	for _, flag := range []string{"prepare-timeout", "check-timeout", "shard-attempt-timeout"} {
		if prepare.Flags().Lookup(flag) == nil {
			t.Errorf("merge prepare is missing --%s", flag)
		}
	}
	if route := command.Flags().Lookup("route"); route == nil || route.DefValue != "auto" {
		t.Fatalf("--route = %#v, want auto", route)
	}
	if cleanup := command.Flags().Lookup("cleanup"); cleanup == nil || cleanup.DefValue != "false" {
		t.Fatalf("--cleanup = %#v, want false", cleanup)
	}
	for _, name := range []string{"prepare", "land", "resume", "revert", "acknowledge-landed-failed", "acknowledge-stranded-landing", "acknowledge-missing-cleanup", "acknowledge-retired-prepare-candidate", "acknowledge-receipt-collision", "adopt-published-candidate", "seal-validation-failed", "supersede-validation-failed", "correct-self-supersession", "prepare-published-forward-repair", "prepare-conflict-replacement"} {
		if child, _, err := command.Find([]string{name}); err != nil || child == nil || child.Name() != name {
			t.Errorf("merge command is missing %s: child=%v err=%v", name, child, err)
			continue
		}
		child, _, _ := command.Find([]string{name})
		if name != "acknowledge-landed-failed" && name != "acknowledge-stranded-landing" && name != "acknowledge-missing-cleanup" && name != "acknowledge-retired-prepare-candidate" && name != "acknowledge-receipt-collision" && name != "adopt-published-candidate" && name != "seal-validation-failed" && name != "supersede-validation-failed" && name != "correct-self-supersession" && name != "prepare-published-forward-repair" && child.Flags().Lookup("progress") == nil {
			t.Errorf("merge %s is missing --progress", name)
		}
	}
	resume, _, err := command.Find([]string{"resume"})
	if err != nil || resume == nil || resume.Flags().Lookup("stop-before-merge") == nil {
		t.Fatalf("merge resume must expose --stop-before-merge: command=%v err=%v", resume, err)
	}
	for _, flag := range []string{"prepare-timeout", "check-timeout", "shard-attempt-timeout"} {
		if resume.Flags().Lookup(flag) == nil {
			t.Errorf("merge resume is missing --%s", flag)
		}
	}
	if resume.Flags().Lookup("allow-unfenced") == nil {
		t.Fatal("merge resume must expose --allow-unfenced")
	}
	land, _, err := command.Find([]string{"land"})
	if err != nil || land == nil || land.Flags().Lookup("stop-before-merge") != nil {
		t.Fatalf("merge land must not expose resume-only --stop-before-merge: command=%v err=%v", land, err)
	}
	if land.Flags().Lookup("allow-unfenced") == nil {
		t.Fatal("merge land must expose --allow-unfenced")
	}
	for _, flag := range []string{"prepare-timeout", "check-timeout", "shard-attempt-timeout"} {
		if land.Flags().Lookup(flag) != nil {
			t.Errorf("merge land must not expose --%s", flag)
		}
	}
	ack, _, err := command.Find([]string{"acknowledge-landed-failed"})
	if err != nil || ack == nil || ack.Flags().Lookup("apply") == nil || ack.Flags().Lookup("actor") == nil || ack.Flags().Lookup("reason") == nil {
		t.Fatalf("acknowledge-landed-failed flags = %#v err=%v", ack, err)
	}
	seal, _, err := command.Find([]string{"seal-validation-failed"})
	if err != nil || seal == nil || seal.Flags().Lookup("apply") == nil || seal.Flags().Lookup("actor") == nil || seal.Flags().Lookup("reason") == nil || seal.Flags().Lookup("model") == nil {
		t.Fatalf("seal-validation-failed flags = %#v err=%v", seal, err)
	}
	supersede, _, err := command.Find([]string{"supersede-validation-failed"})
	if err != nil || supersede == nil || supersede.Flags().Lookup("apply") == nil || supersede.Flags().Lookup("actor") == nil || supersede.Flags().Lookup("reason") == nil {
		t.Fatalf("supersede-validation-failed flags = %#v err=%v", supersede, err)
	}
	stranded, _, err := command.Find([]string{"acknowledge-stranded-landing"})
	if err != nil || stranded == nil || stranded.Flags().Lookup("apply") == nil || stranded.Flags().Lookup("actor") == nil || stranded.Flags().Lookup("reason") == nil {
		t.Fatalf("acknowledge-stranded-landing flags = %#v err=%v", stranded, err)
	}
	for _, status := range []string{"conflict", "published", "checks_pending", "checks_failed"} {
		if !strings.Contains(stranded.Long, status) {
			t.Errorf("acknowledge-stranded-landing help does not mention supported %q receipts: %q", status, stranded.Long)
		}
	}
	missingCleanup, _, err := command.Find([]string{"acknowledge-missing-cleanup"})
	if err != nil || missingCleanup == nil || missingCleanup.Flags().Lookup("apply") == nil || missingCleanup.Flags().Lookup("actor") == nil || missingCleanup.Flags().Lookup("reason") == nil {
		t.Fatalf("acknowledge-missing-cleanup flags = %#v err=%v", missingCleanup, err)
	}
	retiredPrepare, _, err := command.Find([]string{"acknowledge-retired-prepare-candidate"})
	if err != nil || retiredPrepare == nil || retiredPrepare.Flags().Lookup("apply") == nil || retiredPrepare.Flags().Lookup("actor") == nil || retiredPrepare.Flags().Lookup("reason") == nil {
		t.Fatalf("acknowledge-retired-prepare-candidate flags = %#v err=%v", retiredPrepare, err)
	}
	adoption, _, err := command.Find([]string{"adopt-published-candidate"})
	if err != nil || adoption == nil || adoption.Flags().Lookup("apply") == nil || adoption.Flags().Lookup("actor") == nil || adoption.Flags().Lookup("reason") == nil {
		t.Fatalf("adopt-published-candidate flags = %#v err=%v", adoption, err)
	}
	correct, _, err := command.Find([]string{"correct-self-supersession"})
	if err != nil || correct == nil || correct.Flags().Lookup("apply") == nil || correct.Flags().Lookup("actor") == nil || correct.Flags().Lookup("reason") == nil || correct.Flags().Lookup("expected-supersession-sha256") == nil || correct.Flags().Lookup("expected-immutable-claim-sha256") == nil {
		t.Fatalf("correct-self-supersession flags = %#v err=%v", correct, err)
	}
	for _, phrase := range []string{"prepared locally, not landed", "never force-push", "exact remote target", "forward revert", "forward repair", "acknowledge-landed-failed", "acknowledge-stranded-landing", "acknowledge-retired-prepare-candidate", "never asserts source absorption", "seal-validation-failed", "supersede-validation-failed"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("merge help is missing %q", phrase)
		}
	}
}

func TestWorktreeLandDefaultsToCleanup(t *testing.T) {
	t.Parallel()
	command := newWorktreeLandCmd(mergeTestInvocation(shared.Flags{}))
	if command.Name() != "land" {
		t.Fatalf("Name() = %q", command.Name())
	}
	for _, flag := range []string{"target", "route", "cleanup", "on-failure", "allow-unfenced", "format", "progress"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("land is missing --%s", flag)
		}
	}
	if cleanup := command.Flags().Lookup("cleanup"); cleanup == nil || cleanup.DefValue != "true" {
		t.Fatalf("land --cleanup = %#v, want default true", cleanup)
	}
}

func TestValidateWorktreeMergeFlagsStopBeforeMerge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   worktreeMergeFlags
		wantErr string
	}{
		{
			name:    "requires pull request route",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Second, stopBeforeMerge: true},
			wantErr: "requires --route pr",
		},
		{
			name:    "cannot clean before merge",
			flags:   worktreeMergeFlags{format: "text", route: "pr", onFailure: "stop", timeout: time.Second, cleanup: true, stopBeforeMerge: true},
			wantErr: "cannot be combined with --cleanup",
		},
		{
			name:  "valid PR handoff",
			flags: worktreeMergeFlags{format: "text", route: "pr", onFailure: "stop", timeout: time.Second, stopBeforeMerge: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateWorktreeMergeFlags(tt.flags)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateWorktreeMergeFlags() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestWorktreeMergeReceiptCollisionCommandRequiresExpectedEvidence(t *testing.T) {
	t.Parallel()
	command := newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{}))
	child, _, err := command.Find([]string{"acknowledge-receipt-collision"})
	if err != nil || child == nil {
		t.Fatalf("find collision acknowledgement command: child=%v err=%v", child, err)
	}
	for _, flag := range []string{"expected-receipt-sha256", "expected-immutable-claim-sha256", "expected-target", "expected-candidate", "expected-current-source", "expected-historical-refresh-source"} {
		if child.Flags().Lookup(flag) == nil {
			t.Errorf("collision acknowledgement is missing --%s", flag)
		}
	}
	command.SetArgs([]string{"acknowledge-receipt-collision", "receipt.json"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "all expected receipt, claim, target, candidate, current-source, and historical-source identities are required") {
		t.Fatalf("missing collision evidence error = %v", err)
	}
}

func TestWorktreeMergeCorrectSelfSupersessionCommandRequiresExpectedEvidence(t *testing.T) {
	t.Parallel()
	command := newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{}))
	command.SetArgs([]string{"correct-self-supersession", "receipt.json", "replacement-worktree"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--expected-supersession-sha256 and --expected-immutable-claim-sha256 are required") {
		t.Fatalf("missing self-supersession evidence error = %v", err)
	}
}

func TestWorktreeMergePublishedForwardRepairCommandRequiresPinnedEvidence(t *testing.T) {
	t.Parallel()
	command := newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{}))
	child, _, err := command.Find([]string{"prepare-published-forward-repair"})
	if err != nil || child == nil {
		t.Fatalf("find published forward-repair command: child=%v err=%v", child, err)
	}
	for _, flag := range []string{"expected-receipt-sha256", "expected-immutable-claim-sha256", "expected-supersession-sha256", "expected-current-target", "expected-source-sha", "apply", "actor", "reason"} {
		if child.Flags().Lookup(flag) == nil {
			t.Errorf("published forward-repair is missing --%s", flag)
		}
	}
	for _, phrase := range []string{"historical ancestry root", "historical worktrees need not remain live", "current WB-managed worktree", "exact active claim"} {
		if !strings.Contains(child.Long, phrase) {
			t.Errorf("published forward-repair help is missing %q", phrase)
		}
	}
	command.SetArgs([]string{"prepare-published-forward-repair", "receipt.json", "source-worktree"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "expected receipt, immutable claim, self-supersession, current target, and one expected SHA per source") {
		t.Fatalf("missing published forward-repair evidence error = %v", err)
	}
}

func TestWorktreeMergePrepareForcesLocalValidationExceptExplicitPRRoute(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		route string
		want  bool
	}{
		{route: "auto", want: true},
		{route: "", want: true},
		{route: "direct", want: true},
		{route: "pr", want: false},
	} {
		if got := worktreeMergePrepareForcesLocalValidation(test.route); got != test.want {
			t.Errorf("worktreeMergePrepareForcesLocalValidation(%q) = %t, want %t", test.route, got, test.want)
		}
	}
}

func TestWorktreeMergeDirectCIDeferralFlagRequiresExplicitDirectRoute(t *testing.T) {
	t.Parallel()
	base := worktreeMergeFlags{route: "direct", directCIPullRequest: "773", timeout: time.Second, format: "text"}
	if err := validateWorktreeMergeFlags(base); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*worktreeMergeFlags)
	}{
		{"auto route", func(flags *worktreeMergeFlags) { flags.route = "auto" }},
		{"PR route", func(flags *worktreeMergeFlags) { flags.route = "pr" }},
		{"local validation", func(flags *worktreeMergeFlags) { flags.validateLocally = true }},
		{"unfenced", func(flags *worktreeMergeFlags) { flags.allowUnfenced = true }},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Parallel()
			flags := base
			change.apply(&flags)
			if err := validateWorktreeMergeFlags(flags); err == nil || !strings.Contains(err.Error(), "--defer-direct-ci-pr") {
				t.Fatalf("invalid direct CI flags accepted: %+v, err=%v", flags, err)
			}
		})
	}
}

func TestCwWtMergeValidateWorktreeMergeFlagsBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		flags   worktreeMergeFlags
		wantErr string
	}{
		{
			name:    "unsupported output format",
			flags:   worktreeMergeFlags{format: "yaml", route: "auto", onFailure: "stop", timeout: time.Minute},
			wantErr: "unsupported format",
		},
		{
			name:    "unsupported route",
			flags:   worktreeMergeFlags{format: "text", route: "teleport", onFailure: "stop", timeout: time.Minute},
			wantErr: "unsupported --route",
		},
		{
			name:    "unsupported on-failure",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "explode", timeout: time.Minute},
			wantErr: "unsupported --on-failure",
		},
		{
			name:    "non-positive timeout",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: 0},
			wantErr: "--timeout must be positive",
		},
		{
			name:    "negative retry",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, retry: -1},
			wantErr: "--retry must not be negative",
		},
		{
			name:    "negative prepare timeout",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, prepareTimeout: -time.Second},
			wantErr: "must not be negative",
		},
		{
			name:    "negative check timeout",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, checkTimeout: -time.Second},
			wantErr: "must not be negative",
		},
		{
			name:    "negative shard attempt timeout",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, shardAttemptTimeout: -time.Second},
			wantErr: "must not be negative",
		},
		{
			name:    "take over lane without reason",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, takeOverLane: true, laneReason: "   "},
			wantErr: "--take-over-lane requires --lane-reason",
		},
		{
			name:    "stop before merge needs the pr route",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Minute, stopBeforeMerge: true},
			wantErr: "--stop-before-merge requires --route pr",
		},
		{
			name:    "stop before merge cannot combine with cleanup",
			flags:   worktreeMergeFlags{format: "text", route: "pr", onFailure: "stop", timeout: time.Minute, stopBeforeMerge: true, cleanup: true},
			wantErr: "--stop-before-merge cannot be combined with --cleanup",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := validateWorktreeMergeFlags(testCase.flags)
			if err == nil {
				t.Fatalf("validateWorktreeMergeFlags(%+v) = nil, want error containing %q", testCase.flags, testCase.wantErr)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("validateWorktreeMergeFlags error = %q, want it to contain %q", err, testCase.wantErr)
			}
		})
	}

	t.Run("valid combinations", func(t *testing.T) {
		t.Parallel()
		valid := []worktreeMergeFlags{
			{format: "text", route: "", timeout: time.Minute},
			{format: "json", route: "auto", timeout: time.Minute},
			{format: "text", route: "direct", onFailure: "revert", timeout: time.Minute},
			{format: "json", route: "pr", timeout: time.Minute, stopBeforeMerge: true},
			{format: "text", route: "auto", timeout: time.Minute, takeOverLane: true, laneReason: "taking over"},
			{format: "text", route: "auto", timeout: time.Minute, prepareTimeout: time.Second, checkTimeout: time.Second, shardAttemptTimeout: time.Second},
		}
		for _, flags := range valid {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				t.Fatalf("validateWorktreeMergeFlags(%+v) = %v, want nil", flags, err)
			}
		}
	})
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCwWtMergeHostLoadAdmissionRecordBranches(t *testing.T) {
	cwWtMergeHostloadReader(t, func() (float64, error) { return 4.25, nil })
	record := hostLoadAdmissionRecord(defaultMergeHostLoad(), 3.5, "ci", true)
	if record == nil {
		t.Fatal("hostLoadAdmissionRecord with a working reader = nil, want a record")
	}
	if record.Load != 4.25 || record.Floor != 3.5 || !record.Overridden || record.SkippedReason != "ci" {
		t.Fatalf("hostLoadAdmissionRecord = %+v, want load 4.25 floor 3.5 overridden ci", record)
	}
	if record.CheckedAt.IsZero() || record.CheckedAt.Location() != time.UTC {
		t.Fatalf("hostLoadAdmissionRecord checked_at = %v, want a non-zero UTC instant", record.CheckedAt)
	}

	cwWtMergeHostloadReader(t, func() (float64, error) { return 0, errors.New("no loadavg here") })
	if got := hostLoadAdmissionRecord(defaultMergeHostLoad(), 3.5, "", false); got != nil {
		t.Fatalf("hostLoadAdmissionRecord with a failing reader = %+v, want nil", got)
	}

	cwWtMergeHostloadReader(t, nil)
	if got := hostLoadAdmissionRecord(defaultMergeHostLoad(), 3.5, "", false); got != nil {
		t.Fatalf("hostLoadAdmissionRecord with no reader = %+v, want nil", got)
	}
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCwWtMergeCheckHostLoadAdmissionRefusal(t *testing.T) {
	cwWtMergeSaturateHost(t)
	record, err := checkHostLoadAdmission(defaultMergeHostLoad(), worktreeMergeFlags{})
	if err == nil {
		t.Fatalf("checkHostLoadAdmission on a saturated host = %+v, nil; want a refusal", record)
	}
	if !strings.Contains(err.Error(), "wb worktree merge:") {
		t.Fatalf("refusal error = %q, want it prefixed with the command name", err)
	}

	admission, err := checkHostLoadAdmission(defaultMergeHostLoad(), worktreeMergeFlags{allowSaturatedHost: true})
	if err != nil {
		t.Fatalf("checkHostLoadAdmission(--allow-saturated-host) = %v, want nil", err)
	}
	if admission == nil || !admission.Overridden {
		t.Fatalf("override admission record = %+v, want a record marked overridden", admission)
	}
	if admission.Floor != 1 {
		t.Fatalf("override admission floor = %v, want 1", admission.Floor)
	}
}

func TestCwWtMergeFinishWorktreeMergeProgressBranches(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "cwWtMergeProgress"}
	command.SetErr(&bytes.Buffer{})
	command.SetOut(&bytes.Buffer{})

	cases := []struct {
		name    string
		receipt orchestrate.WorktreeMergeReceipt
		err     error
	}{
		{name: "failure keeps the receipt status", receipt: orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeValidationFailed}, err: errors.New("boom")},
		{name: "failure without a status reports failed", receipt: orchestrate.WorktreeMergeReceipt{}, err: errors.New("boom")},
		{name: "success keeps the receipt status", receipt: orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}},
		{name: "success without a status reports completed", receipt: orchestrate.WorktreeMergeReceipt{}},
		{name: "whitespace-only failure status reports failed", receipt: orchestrate.WorktreeMergeReceipt{Status: "   "}, err: errors.New("boom")},
	}
	for _, testCase := range cases {
		//nolint:paralleltest // Rows share the Cobra command and its output buffers while campaigns finish; keep campaign writes sequential.
		t.Run(testCase.name, func(t *testing.T) {
			campaign := newWorktreeMergeProgress(mergeTestInvocation(shared.Flags{}), command, worktreeMergeFlags{})
			finishWorktreeMergeProgress(campaign, testCase.receipt, testCase.err)
			campaign.Finish("again") // idempotent: a second finish must not panic
		})
	}
}

func TestCwWtMergeWriteWorktreeMergeReceiptBranches(t *testing.T) {
	t.Parallel()
	receipt := orchestrate.WorktreeMergeReceipt{
		Status: orchestrate.WorktreeMergeComplete, Repository: "acme/app", Target: "main",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("a", 40)},
		ReceiptPath: "/tmp/receipt.json", ResumeArgs: []string{"worktree", "merge", "land", "/tmp/receipt.json"},
	}

	t.Run("json encodes the receipt", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		if err := writeWorktreeMergeReceipt(&out, "json", receipt); err != nil {
			t.Fatalf("writeWorktreeMergeReceipt json = %v, want nil", err)
		}
		var decoded orchestrate.WorktreeMergeReceipt
		if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
			t.Fatalf("decode json receipt output %q: %v", out.String(), err)
		}
		if decoded.Repository != "acme/app" || decoded.Candidate.SHA != receipt.Candidate.SHA {
			t.Fatalf("decoded receipt = %+v, want repository acme/app and the candidate SHA", decoded)
		}
	})

	t.Run("json reports an encode failure", func(t *testing.T) {
		t.Parallel()
		if err := writeWorktreeMergeReceipt(&mergeOriginalFailWriter{}, "json", receipt); !errors.Is(err, errMergeOriginalWrite) {
			t.Fatalf("writeWorktreeMergeReceipt json with a failing writer = %v, want the injected failure", err)
		}
	})

	t.Run("text names the resume command", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		if err := writeWorktreeMergeReceipt(&out, "text", receipt); err != nil {
			t.Fatalf("writeWorktreeMergeReceipt text = %v, want nil", err)
		}
		for _, want := range []string{"status: complete", "repository: acme/app", "target: main", "candidate: " + receipt.Candidate.SHA, "receipt: /tmp/receipt.json", "resume: wb worktree merge land /tmp/receipt.json"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("text receipt output %q missing %q", out.String(), want)
			}
		}
	})

	t.Run("text reports a write failure", func(t *testing.T) {
		t.Parallel()
		if err := writeWorktreeMergeReceipt(&mergeOriginalFailWriter{}, "text", receipt); !errors.Is(err, errMergeOriginalWrite) {
			t.Fatalf("writeWorktreeMergeReceipt text with a failing writer = %v, want the injected failure", err)
		}
	})

	t.Run("text records a host load admission", func(t *testing.T) {
		t.Parallel()
		withAdmission := receipt
		withAdmission.HostLoadAdmission = &orchestrate.WorktreeMergeHostLoadAdmission{
			Load: 9.5, Floor: 8, Overridden: true, CheckedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		}
		var out bytes.Buffer
		if err := writeWorktreeMergeReceipt(&out, "text", withAdmission); err != nil {
			t.Fatalf("writeWorktreeMergeReceipt with admission = %v, want nil", err)
		}
		want := "host_load_admission: load=9.50 floor=8.00 overridden=true checked_at=2026-01-02T03:04:05Z"
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text receipt output %q missing %q", out.String(), want)
		}
	})

	t.Run("text reports an admission write failure", func(t *testing.T) {
		t.Parallel()
		withAdmission := receipt
		withAdmission.HostLoadAdmission = &orchestrate.WorktreeMergeHostLoadAdmission{Load: 1, Floor: 0, CheckedAt: time.Now().UTC()}
		writer := &mergeOriginalFailWriter{Allow: 1}
		if err := writeWorktreeMergeReceipt(writer, "text", withAdmission); !errors.Is(err, errMergeOriginalWrite) {
			t.Fatalf("writeWorktreeMergeReceipt admission write failure = %v, want the injected failure", err)
		}
	})
}

func TestCwWtMergeHostLoadCheckSkippableBranches(t *testing.T) {
	t.Parallel()
	candidateSHA := strings.Repeat("b", 40)
	cases := []struct {
		name    string
		receipt orchestrate.WorktreeMergeReceipt
		want    bool
	}{
		{name: "complete", receipt: orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}, want: true},
		{name: "empty", receipt: orchestrate.WorktreeMergeReceipt{}, want: false},
		{
			name: "published and validated exact candidate",
			receipt: orchestrate.WorktreeMergeReceipt{
				Status: orchestrate.WorktreeMergeChecksPending, PullRequest: "https://example.test/pr/1",
				Candidate:          orchestrate.WorktreeMergeCandidate{SHA: candidateSHA},
				ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{CandidateSHA: candidateSHA},
				Validation:         quality.VerificationReport{Status: quality.StatusPassed},
			},
			want: true,
		},
		{
			name: "published but a different candidate SHA",
			receipt: orchestrate.WorktreeMergeReceipt{
				Status: orchestrate.WorktreeMergeChecksPending, PullRequest: "https://example.test/pr/1",
				Candidate:          orchestrate.WorktreeMergeCandidate{SHA: candidateSHA},
				ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{CandidateSHA: strings.Repeat("c", 40)},
				Validation:         quality.VerificationReport{Status: quality.StatusPassed},
			},
			want: false,
		},
		{
			name: "published but not yet passed",
			receipt: orchestrate.WorktreeMergeReceipt{
				Status: orchestrate.WorktreeMergeChecksPending, PullRequest: "https://example.test/pr/1",
				Candidate:          orchestrate.WorktreeMergeCandidate{SHA: candidateSHA},
				ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{CandidateSHA: candidateSHA},
				Validation:         quality.VerificationReport{Status: quality.StatusFailed},
			},
			want: false,
		},
		{
			name: "published without a validation identity",
			receipt: orchestrate.WorktreeMergeReceipt{
				Status: orchestrate.WorktreeMergeChecksPending, PullRequest: "https://example.test/pr/1",
				Candidate:  orchestrate.WorktreeMergeCandidate{SHA: candidateSHA},
				Validation: quality.VerificationReport{Status: quality.StatusPassed},
			},
			want: false,
		},
		{
			name: "published without a candidate SHA",
			receipt: orchestrate.WorktreeMergeReceipt{
				Status: orchestrate.WorktreeMergeChecksPending, PullRequest: "https://example.test/pr/1",
				ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{CandidateSHA: candidateSHA},
				Validation:         quality.VerificationReport{Status: quality.StatusPassed},
			},
			want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := hostLoadCheckSkippable(testCase.receipt, false, false, ""); got != testCase.want {
				t.Fatalf("hostLoadCheckSkippable(%+v) = %t, want %t", testCase.receipt, got, testCase.want)
			}
		})
	}
}

func TestCwWtMergeRecoveryCommandsRejectBadFormat(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	receipt := filepath.Join(projects, "receipt.json")
	for _, constructor := range cwWtMergeAckConstructors(projects) {
		t.Run(constructor.name, func(t *testing.T) {
			t.Parallel()
			args := append(constructor.args(receipt), "--format", "yaml")
			stdout, _, err := mergeExecuteOriginal(t, projects, constructor.build, args...)
			if err == nil {
				t.Fatalf("%s --format yaml = nil error (stdout %q), want a format refusal", constructor.name, stdout)
			}
			if !strings.Contains(err.Error(), "unsupported format") {
				t.Fatalf("%s --format yaml error = %q, want an unsupported-format refusal", constructor.name, err)
			}
		})
	}
}

func TestCwWtMergeRecoveryCommandsFailOnMissingReceipt(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	receipt := filepath.Join(projects, "absent-receipt.json")
	for _, constructor := range cwWtMergeAckConstructors(projects) {
		t.Run(constructor.name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := mergeExecuteOriginal(t, projects, constructor.build, constructor.args(receipt)...)
			if err == nil {
				t.Fatalf("%s with a missing receipt = nil error (stdout %q), want a refusal", constructor.name, stdout)
			}
			if strings.Contains(err.Error(), "unsupported format") || strings.Contains(err.Error(), "--initiator") {
				t.Fatalf("%s failed before reaching the backend: %v", constructor.name, err)
			}
		})
	}
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCwWtMergeCombinedCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("bad format is refused before any work", func(t *testing.T) {
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, "--format", "yaml", filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("combined --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("saturated host refuses the run", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		source := filepath.Join(projects, "nope")
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, source)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("combined on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("unusable source fails the merge and writes no receipt", func(t *testing.T) {
		source := filepath.Join(projects, "definitely-absent")
		stdout, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, source)
		if err == nil {
			t.Fatalf("combined with an absent source = nil error (stdout %q), want a refusal", stdout)
		}
		if !strings.Contains(stdout, "status:") {
			t.Fatalf("combined stdout = %q, want a status line even on failure", stdout)
		}
	})
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCwWtMergePrepareCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("bad format is refused", func(t *testing.T) {
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergePrepareCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, "--format", "yaml", filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("prepare --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("saturated host refuses the prepare", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergePrepareCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("prepare on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("absent source is refused", func(t *testing.T) {
		stdout, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergePrepareCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, filepath.Join(projects, "definitely-absent"))
		if err == nil {
			t.Fatalf("prepare with an absent source = nil error (stdout %q), want a refusal", stdout)
		}
	})
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCwWtMergeLandAndRevertCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()
	absent := filepath.Join(projects, "absent-receipt.json")

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("land rejects a bad format", func(t *testing.T) {
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeLandCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}), "land")
		}, "--format", "yaml", absent)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("land --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("land on a saturated host refuses", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		// A readable receipt that names no worktrees gets past the live-link
		// guard, so the host-load gate is the thing that refuses.
		readable := filepath.Join(t.TempDir(), "empty-receipt.json")
		if err := os.WriteFile(readable, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeLandCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}), "land")
		}, readable)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("land on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("land fails on a missing receipt", func(t *testing.T) {
		stdout, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeLandCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}), "land")
		}, absent)
		if err == nil {
			t.Fatalf("land with a missing receipt = nil error (stdout %q), want a refusal", stdout)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("revert rejects a bad format", func(t *testing.T) {
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeRevertCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, "--format", "yaml", absent)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("revert --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("revert on a saturated host refuses", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		_, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeRevertCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, absent)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("revert on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	//nolint:paralleltest // Process-wide environment changes in cwWtMergeSaturateHost; these rows share their parent environment and remain sequential.
	t.Run("revert fails on a missing receipt", func(t *testing.T) {
		stdout, _, err := mergeExecuteOriginal(t, projects, func() *cobra.Command {
			return newWorktreeMergeRevertCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, absent)
		if err == nil {
			t.Fatalf("revert with a missing receipt = nil error (stdout %q), want a refusal", stdout)
		}
		if !strings.Contains(stdout, "status:") {
			t.Fatalf("revert stdout = %q, want a status line even on failure", stdout)
		}
	})
}

func TestCwWtMergePrepareMergeOptionsAndLandMergeOptions(t *testing.T) {
	t.Parallel()
	projectsRoot := filepath.Join(t.TempDir(), "projects")

	flags := worktreeMergeFlags{
		target: "main", route: "pr", onFailure: "revert", format: "json", cleanup: true, allowUnfenced: true,
		model: "m", runtime: "r", agentID: "a", cli: "c", provider: "p", timeout: time.Minute, retry: 2,
		prepareTimeout: time.Second, checkTimeout: 2 * time.Second, shardAttemptTimeout: 3 * time.Second,
		interval: 4 * time.Second, rebatchReceipt: "/tmp/r.json", progress: true, stopBeforeMerge: true,
		takeOverLane: true, laneReason: "reason",
	}
	prepare := prepareMergeOptions(mergeTestInvocation(shared.Flags{ProjectsRoot: projectsRoot}), flags, []string{"/tmp/wt"}, nil, nil, nil)
	if prepare.ProjectsRoot != projectsRoot || prepare.Target != "main" || prepare.Model != "m" || prepare.Retry != 2 {
		t.Fatalf("prepareMergeOptions = %+v, want the flags carried through", prepare)
	}
	if len(prepare.Sources) != 1 || prepare.Sources[0] != "/tmp/wt" {
		t.Fatalf("prepareMergeOptions sources = %v, want the source list carried through", prepare.Sources)
	}
	if prepare.RebatchReceipt != "/tmp/r.json" || prepare.ProgressRequested != true || prepare.ShardAttemptTimeout != 3*time.Second {
		t.Fatalf("prepareMergeOptions = %+v, want the prepare-only flags carried through", prepare)
	}
	if !prepare.Lane.TakeOver || prepare.Lane.TakeoverReason != "reason" {
		t.Fatalf("prepareMergeOptions lane = %+v, want the takeover request carried through", prepare.Lane)
	}

	land := landMergeOptions(mergeTestInvocation(shared.Flags{ProjectsRoot: projectsRoot}), flags, "/tmp/receipt.json", nil, nil, &bytes.Buffer{})
	if land.Receipt != "/tmp/receipt.json" || !land.Cleanup || !land.AllowUnfenced || land.OnFailure != "revert" {
		t.Fatalf("landMergeOptions = %+v, want the flags carried through", land)
	}
	if !land.StopBeforeMerge || land.CheckPollInterval != 4*time.Second || land.ShardAttemptTimeout != 3*time.Second {
		t.Fatalf("landMergeOptions = %+v, want the land-only flags carried through", land)
	}
	if !land.Lane.TakeOver || land.Lane.TakeoverReason != "reason" {
		t.Fatalf("landMergeOptions lane = %+v, want the takeover request carried through", land.Lane)
	}
}

func TestCwWtMergeBindFlagsCoverBothJourneys(t *testing.T) {
	t.Parallel()
	var combined worktreeMergeFlags
	combinedCmd := &cobra.Command{Use: "cwWtMergeCombined"}
	bindWorktreeMergeFlags(combinedCmd, &combined, true, true, false)
	for _, name := range []string{"target", "model", "agent-runtime", "cleanup", "route", "allow-unfenced", "on-failure", "check-interval", "timeout", "retry", "format", "progress", "allow-saturated-host", "take-over-lane", "lane-reason"} {
		if combinedCmd.Flags().Lookup(name) == nil {
			t.Fatalf("combined merge flags are missing --%s", name)
		}
	}
	if combinedCmd.Flags().Lookup("cleanup").DefValue != "false" {
		t.Fatalf("combined --cleanup default = %q, want false", combinedCmd.Flags().Lookup("cleanup").DefValue)
	}

	var landOnly worktreeMergeFlags
	landCmd := &cobra.Command{Use: "cwWtMergeLand"}
	bindWorktreeMergeFlags(landCmd, &landOnly, false, true, true)
	if landCmd.Flags().Lookup("cleanup").DefValue != "true" {
		t.Fatalf("land --cleanup default = %q, want true", landCmd.Flags().Lookup("cleanup").DefValue)
	}
	if landCmd.Flags().Lookup("target") != nil {
		t.Fatal("land-only command unexpectedly exposes the prepare --target flag")
	}
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCheckHostLoadAdmissionRefusesWorktreeMergeCandidateValidation(t *testing.T) {
	withHostLoad(t, 999.0)
	admission, err := checkHostLoadAdmission(defaultMergeHostLoad(), worktreeMergeFlags{})
	if err == nil {
		t.Fatal("checkHostLoadAdmission(saturated host) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "--allow-saturated-host") {
		t.Errorf("refusal %q does not mention the override flag", err)
	}
	if admission != nil {
		t.Errorf("checkHostLoadAdmission(saturated host) admission = %+v, want nil on refusal", admission)
	}
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCheckHostLoadAdmissionAllowSaturatedHostOverrides(t *testing.T) {
	withHostLoad(t, 999.0)
	admission, err := checkHostLoadAdmission(defaultMergeHostLoad(), worktreeMergeFlags{allowSaturatedHost: true})
	if err != nil {
		t.Fatalf("checkHostLoadAdmission with --allow-saturated-host = %v, want nil", err)
	}
	if admission == nil {
		t.Fatal("checkHostLoadAdmission with --allow-saturated-host recorded no admission")
	}
	if !admission.Overridden {
		t.Errorf("admission.Overridden = false, want true for --allow-saturated-host")
	}
	if admission.Load != 999.0 {
		t.Errorf("admission.Load = %v, want 999.0", admission.Load)
	}
	if admission.Floor <= 0 {
		t.Errorf("admission.Floor = %v, want > 0", admission.Floor)
	}
	if admission.CheckedAt.IsZero() {
		t.Error("admission.CheckedAt is zero")
	}
}

//nolint:paralleltest // Actual host-load/environment observations are process-wide.
func TestCheckHostLoadAdmissionAdmitsBelowFloor(t *testing.T) {
	withHostLoad(t, 0.01)
	admission, err := checkHostLoadAdmission(defaultMergeHostLoad(), worktreeMergeFlags{})
	if err != nil {
		t.Fatalf("checkHostLoadAdmission(quiet host) = %v, want nil", err)
	}
	if admission == nil {
		t.Fatal("checkHostLoadAdmission(quiet host) recorded no admission")
	}
	if admission.Overridden {
		t.Error("admission.Overridden = true, want false without --allow-saturated-host")
	}
	if admission.Load != 0.01 {
		t.Errorf("admission.Load = %v, want 0.01", admission.Load)
	}
}

func TestHostLoadCheckSkippableCoversTheDocumentedShapes(t *testing.T) {
	t.Parallel()
	validated := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("a", 40)},
		ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{
			CandidateSHA: strings.Repeat("a", 40),
		},
		Validation: quality.VerificationReport{Status: quality.StatusPassed},
	}
	if !hostLoadCheckSkippable(validated, false, false, "") {
		t.Error("published + validated-for-exact-candidate receipt must skip the host-load check")
	}

	complete := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}
	if !hostLoadCheckSkippable(complete, false, false, "") {
		t.Error("complete receipt must skip the host-load check")
	}

	validationFailed := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeValidationFailed}
	if hostLoadCheckSkippable(validationFailed, false, false, "") {
		t.Error("validation_failed receipt must still be gated by the host-load check")
	}

	preparing := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergePreparing}
	if hostLoadCheckSkippable(preparing, false, false, "") {
		t.Error("preparing receipt must still be gated by the host-load check")
	}

	stalePublished := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("b", 40)},
		ValidationIdentity: &orchestrate.WorktreeMergeValidationIdentity{
			CandidateSHA: strings.Repeat("a", 40), // stale: does not match the current candidate
		},
		Validation: quality.VerificationReport{Status: quality.StatusPassed},
	}
	if hostLoadCheckSkippable(stalePublished, false, false, "") {
		t.Error("published receipt whose validation identity no longer matches the candidate must still be gated")
	}

	// sneat-dev/wb#591: a receipt whose exact candidate SHA was deferred to
	// the pull-request route's authoritative CI (never validated locally at
	// all) has nothing left to run locally either.
	deferred := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("c", 40)},
		Validation:  quality.VerificationReport{Status: quality.StatusSkipped},
		ValidationDeferral: &orchestrate.WorktreeMergeValidationDeferral{
			Route: orchestrate.WorktreeMergeRoutePullRequest, CandidateSHA: strings.Repeat("c", 40),
		},
	}
	if !hostLoadCheckSkippable(deferred, false, false, "") {
		t.Error("PR-route-deferred receipt must skip the host-load check")
	}

	staleDeferred := deferred
	staleDeferred.Candidate = orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("d", 40)}
	if hostLoadCheckSkippable(staleDeferred, false, false, "") {
		t.Error("a deferral for a candidate SHA that no longer matches must still be gated")
	}

	// A receipt just advanced by an engine-driven server-side update-branch
	// merge (TargetRefreshes) has already had its published head moved to
	// the current candidate SHA by GitHub; only remote observation/merge is
	// left, so it must also skip the check.
	engineUpdated := orchestrate.WorktreeMergeReceipt{
		Status:                orchestrate.WorktreeMergeChecksPending,
		PullRequest:           "https://github.com/acme/app/pull/1",
		Candidate:             orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("e", 40)},
		PublishedCandidateSHA: strings.Repeat("e", 40),
		TargetRefreshes:       []orchestrate.WorktreeMergeTargetRefresh{{NewCandidateSHA: strings.Repeat("e", 40)}},
	}
	if !hostLoadCheckSkippable(engineUpdated, false, false, "") {
		t.Error("engine-updated receipt whose published head matches the candidate must skip the host-load check")
	}

	// Minor finding (sneat-dev/wb#591 red-team follow-up): --validate-locally
	// or --route direct on THIS call always runs a heavy local validation
	// pass next, regardless of any deferral or already-validated identity
	// recorded on the receipt, so it must never be skippable.
	if hostLoadCheckSkippable(deferred, true, false, "") {
		t.Error("--validate-locally on this call must never be skippable, even for an otherwise-deferred receipt")
	}
	if hostLoadCheckSkippable(deferred, false, false, orchestrate.WorktreeMergeRouteDirect) {
		t.Error("--route direct on this call must never be skippable, even for an otherwise-deferred receipt")
	}
	if hostLoadCheckSkippable(validated, true, false, "") {
		t.Error("--validate-locally on this call must never be skippable, even for an already-validated receipt")
	}
	if !hostLoadCheckSkippable(validated, false, false, orchestrate.WorktreeMergeRouteAuto) {
		t.Error("an explicit --route auto (the CLI default) must not change the otherwise-skippable outcome")
	}
}

func TestHostLoadCheckSkippableAllowUnfencedNeverSkippable(t *testing.T) {
	t.Parallel()
	deferred := orchestrate.WorktreeMergeReceipt{
		Status:      orchestrate.WorktreeMergePublished,
		PullRequest: "https://github.com/acme/app/pull/1",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("c", 40)},
		Validation:  quality.VerificationReport{Status: quality.StatusSkipped},
		ValidationDeferral: &orchestrate.WorktreeMergeValidationDeferral{
			Route: orchestrate.WorktreeMergeRoutePullRequest, CandidateSHA: strings.Repeat("c", 40),
		},
	}
	if hostLoadCheckSkippable(deferred, false, true, "") {
		t.Error("--allow-unfenced on this call must never be skippable, even for an otherwise-deferred receipt")
	}
}

func TestWorktreeMergeProgressIsSilentUnderQuiet(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		quiet bool
	}{{"default reports", false}, {"quiet reports nothing", true}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			command := &cobra.Command{}
			command.SetErr(&stderr)
			campaign := newWorktreeMergeProgress(mergeTestInvocation(shared.Flags{Quiet: test.quiet}), command, worktreeMergeFlags{progress: true})
			if reporter := campaign.Reporter(); (reporter != nil) == test.quiet {
				t.Fatalf("reporter present = %t under quiet = %t", reporter != nil, test.quiet)
			}
			finishWorktreeMergeProgress(campaign, orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}, nil)
			if test.quiet && stderr.Len() > 0 {
				t.Fatalf("quiet worktree merge wrote to stderr: %q", stderr.String())
			}
		})
	}
}

func cwWtMergeAckConstructors(projects string) []cwWtMergeAckConstructor {
	return []cwWtMergeAckConstructor{
		{name: "acknowledge-missing-cleanup", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeMissingCleanupCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "adopt-published-candidate", build: func() *cobra.Command {
			return newWorktreeMergeAdoptPublishedCandidateCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r, "https://example.test/pr/1"} }},
		{name: "acknowledge-landed-failed", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeLandedFailedCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-stranded-landing", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeStrandedLandingCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-prepare-candidate", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeRetiredPrepareCandidateCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-absorbed-conflict", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeAbsorbedConflictCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-publication", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeRetiredPublicationCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-unpublished-validation-failure", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeUnpublishedValidationFailureCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-receipt-collision", build: func() *cobra.Command {
			return newWorktreeMergeAcknowledgeReceiptCollisionCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
		{name: "supersede-validation-failed", build: func() *cobra.Command {
			return newWorktreeMergeSupersedeValidationFailedCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "correct-self-supersession", build: func() *cobra.Command {
			return newWorktreeMergeCorrectSelfSupersessionCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-published-forward-repair", build: func() *cobra.Command {
			return newWorktreeMergePreparePublishedForwardRepairCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-conflict-replacement", build: func() *cobra.Command {
			return newWorktreeMergePrepareConflictReplacementCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "seal-validation-failed", build: func() *cobra.Command {
			return newWorktreeMergeSealValidationFailedCmd(mergeTestInvocation(shared.Flags{ProjectsRoot: projects}))
		}, args: func(r string) []string { return []string{r} }},
	}
}

func TestWriteWorktreeMergeReceiptFindings(t *testing.T) {
	t.Parallel()
	receipt := orchestrate.WorktreeMergeReceipt{
		Repository: "sneat-dev/wb",
		Target:     "main",
		Findings: []orchestrate.WorktreeMergeFinding{
			{Code: "c1", Message: "m1"},
		},
	}
	buf := &bytes.Buffer{}
	if err := writeWorktreeMergeReceipt(buf, "text", receipt); err != nil {
		t.Fatalf("writeWorktreeMergeReceipt: %v", err)
	}
	if !strings.Contains(buf.String(), "finding: c1: m1") {
		t.Fatalf("expected finding line, got %q", buf.String())
	}
}
