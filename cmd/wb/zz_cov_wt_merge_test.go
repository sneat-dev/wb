package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwWtMergeSaturateHost forces checkHostLoadAdmission to refuse: it pins a
// positive admission floor and replaces the load reader with one that always
// reports an absurdly high load, so the refusal is deterministic on every
// platform instead of depending on the actual machine.
func cwWtMergeSaturateHost(t *testing.T) {
	t.Helper()
	t.Setenv(hostload.EnvLoadFloor, "1")
	previous := hostload.System
	hostload.System = func() (float64, error) { return 10000, nil }
	t.Cleanup(func() { hostload.System = previous })
}

// cwWtMergeHostloadReader swaps the package-level load reader for the body of
// the test and restores it afterwards.
func cwWtMergeHostloadReader(t *testing.T, reader hostload.Reader) {
	t.Helper()
	previous := hostload.System
	hostload.System = reader
	t.Cleanup(func() { hostload.System = previous })
}

func TestCwWtMergeValidateWorktreeMergeFlagsBranches(t *testing.T) {
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

func TestCwWtMergeHostLoadAdmissionRecordBranches(t *testing.T) {
	cwWtMergeHostloadReader(t, func() (float64, error) { return 4.25, nil })
	record := hostLoadAdmissionRecord(3.5, "ci", true)
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
	if got := hostLoadAdmissionRecord(3.5, "", false); got != nil {
		t.Fatalf("hostLoadAdmissionRecord with a failing reader = %+v, want nil", got)
	}

	cwWtMergeHostloadReader(t, nil)
	if got := hostLoadAdmissionRecord(3.5, "", false); got != nil {
		t.Fatalf("hostLoadAdmissionRecord with no reader = %+v, want nil", got)
	}
}

func TestCwWtMergeCheckHostLoadAdmissionRefusal(t *testing.T) {
	cwWtMergeSaturateHost(t)
	record, err := checkHostLoadAdmission(worktreeMergeFlags{})
	if err == nil {
		t.Fatalf("checkHostLoadAdmission on a saturated host = %+v, nil; want a refusal", record)
	}
	if !strings.Contains(err.Error(), "wb worktree merge:") {
		t.Fatalf("refusal error = %q, want it prefixed with the command name", err)
	}

	admission, err := checkHostLoadAdmission(worktreeMergeFlags{allowSaturatedHost: true})
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
		t.Run(testCase.name, func(t *testing.T) {
			campaign := newWorktreeMergeProgress(command, worktreeMergeFlags{})
			finishWorktreeMergeProgress(campaign, testCase.receipt, testCase.err)
			campaign.finish("again") // idempotent: a second finish must not panic
		})
	}
}

func TestCwWtMergeWriteWorktreeMergeReceiptBranches(t *testing.T) {
	receipt := orchestrate.WorktreeMergeReceipt{
		Status: orchestrate.WorktreeMergeComplete, Repository: "acme/app", Target: "main",
		Candidate:   orchestrate.WorktreeMergeCandidate{SHA: strings.Repeat("a", 40)},
		ReceiptPath: "/tmp/receipt.json", ResumeArgs: []string{"worktree", "merge", "land", "/tmp/receipt.json"},
	}

	t.Run("json encodes the receipt", func(t *testing.T) {
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
		if err := writeWorktreeMergeReceipt(&cwWtFailWriter{}, "json", receipt); !errors.Is(err, errCwWtWrite) {
			t.Fatalf("writeWorktreeMergeReceipt json with a failing writer = %v, want the injected failure", err)
		}
	})

	t.Run("text names the resume command", func(t *testing.T) {
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
		if err := writeWorktreeMergeReceipt(&cwWtFailWriter{}, "text", receipt); !errors.Is(err, errCwWtWrite) {
			t.Fatalf("writeWorktreeMergeReceipt text with a failing writer = %v, want the injected failure", err)
		}
	})

	t.Run("text records a host load admission", func(t *testing.T) {
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
		withAdmission := receipt
		withAdmission.HostLoadAdmission = &orchestrate.WorktreeMergeHostLoadAdmission{Load: 1, Floor: 0, CheckedAt: time.Now().UTC()}
		writer := &cwWtFailWriter{Allow: 1}
		if err := writeWorktreeMergeReceipt(writer, "text", withAdmission); !errors.Is(err, errCwWtWrite) {
			t.Fatalf("writeWorktreeMergeReceipt admission write failure = %v, want the injected failure", err)
		}
	})
}

func TestCwWtMergeHostLoadCheckSkippableBranches(t *testing.T) {
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
			if got := hostLoadCheckSkippable(testCase.receipt, false, ""); got != testCase.want {
				t.Fatalf("hostLoadCheckSkippable(%+v) = %t, want %t", testCase.receipt, got, testCase.want)
			}
		})
	}
}

// cwWtMergeAckConstructor describes one recovery verb so the shared error-path
// table can drive every constructor in-process.
type cwWtMergeAckConstructor struct {
	name  string
	build func() *cobra.Command
	args  func(receipt string) []string
}

func cwWtMergeAckConstructors() []cwWtMergeAckConstructor {
	return []cwWtMergeAckConstructor{
		{name: "acknowledge-missing-cleanup", build: newWorktreeMergeAcknowledgeMissingCleanupCmd, args: func(r string) []string { return []string{r} }},
		{name: "adopt-published-candidate", build: newWorktreeMergeAdoptPublishedCandidateCmd, args: func(r string) []string { return []string{r, "https://example.test/pr/1"} }},
		{name: "acknowledge-landed-failed", build: newWorktreeMergeAcknowledgeLandedFailedCmd, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-stranded-landing", build: newWorktreeMergeAcknowledgeStrandedLandingCmd, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-absorbed-conflict", build: newWorktreeMergeAcknowledgeAbsorbedConflictCmd, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-publication", build: newWorktreeMergeAcknowledgeRetiredPublicationCmd, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-unpublished-validation-failure", build: newWorktreeMergeAcknowledgeUnpublishedValidationFailureCmd, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-receipt-collision", build: newWorktreeMergeAcknowledgeReceiptCollisionCmd, args: func(r string) []string { return []string{r} }},
		{name: "supersede-validation-failed", build: newWorktreeMergeSupersedeValidationFailedCmd, args: func(r string) []string { return []string{r, r} }},
		{name: "correct-self-supersession", build: newWorktreeMergeCorrectSelfSupersessionCmd, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-published-forward-repair", build: newWorktreeMergePreparePublishedForwardRepairCmd, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-conflict-replacement", build: newWorktreeMergePrepareConflictReplacementCmd, args: func(r string) []string { return []string{r, r} }},
		{name: "seal-validation-failed", build: newWorktreeMergeSealValidationFailedCmd, args: func(r string) []string { return []string{r} }},
	}
}

func TestCwWtMergeRecoveryCommandsRejectBadFormat(t *testing.T) {
	projects := t.TempDir()
	receipt := filepath.Join(projects, "receipt.json")
	for _, constructor := range cwWtMergeAckConstructors() {
		t.Run(constructor.name, func(t *testing.T) {
			args := append(constructor.args(receipt), "--format", "yaml")
			stdout, _, err := cwCovExec(t, projects, constructor.build, args...)
			if err == nil {
				t.Fatalf("%s --format yaml = nil error (stdout %q), want a format refusal", constructor.name, stdout)
			}
			if !strings.Contains(err.Error(), "unsupported format") {
				t.Fatalf("%s --format yaml error = %q, want an unsupported-format refusal", constructor.name, err)
			}
		})
	}
}

func TestCwWtMergeRecoveryCommandsRejectBadAdmission(t *testing.T) {
	projects := t.TempDir()
	receipt := filepath.Join(projects, "receipt.json")
	for _, constructor := range cwWtMergeAckConstructors() {
		t.Run(constructor.name, func(t *testing.T) {
			args := append(constructor.args(receipt), "--apply", "--mode", "manual")
			stdout, _, err := cwCovExec(t, projects, constructor.build, args...)
			if err == nil {
				t.Fatalf("%s --apply --mode manual = nil error (stdout %q), want an admission refusal", constructor.name, stdout)
			}
			if !strings.Contains(err.Error(), "--initiator") {
				t.Fatalf("%s admission error = %q, want a manual-mode --initiator refusal", constructor.name, err)
			}
		})
	}
}

func TestCwWtMergeRecoveryCommandsFailOnMissingReceipt(t *testing.T) {
	projects := t.TempDir()
	receipt := filepath.Join(projects, "absent-receipt.json")
	for _, constructor := range cwWtMergeAckConstructors() {
		t.Run(constructor.name, func(t *testing.T) {
			stdout, _, err := cwCovExec(t, projects, constructor.build, constructor.args(receipt)...)
			if err == nil {
				t.Fatalf("%s with a missing receipt = nil error (stdout %q), want a refusal", constructor.name, stdout)
			}
			if strings.Contains(err.Error(), "unsupported format") || strings.Contains(err.Error(), "--initiator") {
				t.Fatalf("%s failed before reaching the backend: %v", constructor.name, err)
			}
		})
	}
}

func TestCwWtMergeCombinedCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()

	t.Run("bad format is refused before any work", func(t *testing.T) {
		_, _, err := cwCovExec(t, projects, newWorktreeMergeCmd, "--format", "yaml", filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("combined --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	t.Run("saturated host refuses the run", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		source := filepath.Join(projects, "nope")
		_, _, err := cwCovExec(t, projects, newWorktreeMergeCmd, source)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("combined on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	t.Run("unusable source fails the merge and writes no receipt", func(t *testing.T) {
		source := filepath.Join(projects, "definitely-absent")
		stdout, _, err := cwCovExec(t, projects, newWorktreeMergeCmd, source)
		if err == nil {
			t.Fatalf("combined with an absent source = nil error (stdout %q), want a refusal", stdout)
		}
		if !strings.Contains(stdout, "status:") {
			t.Fatalf("combined stdout = %q, want a status line even on failure", stdout)
		}
	})
}

func TestCwWtMergePrepareCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()

	t.Run("bad format is refused", func(t *testing.T) {
		_, _, err := cwCovExec(t, projects, newWorktreeMergePrepareCmd, "--format", "yaml", filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("prepare --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	t.Run("saturated host refuses the prepare", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		_, _, err := cwCovExec(t, projects, newWorktreeMergePrepareCmd, filepath.Join(projects, "nope"))
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("prepare on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	t.Run("absent source is refused", func(t *testing.T) {
		stdout, _, err := cwCovExec(t, projects, newWorktreeMergePrepareCmd, filepath.Join(projects, "definitely-absent"))
		if err == nil {
			t.Fatalf("prepare with an absent source = nil error (stdout %q), want a refusal", stdout)
		}
	})
}

func TestCwWtMergeLandAndRevertCommandErrorPaths(t *testing.T) {
	projects := t.TempDir()
	absent := filepath.Join(projects, "absent-receipt.json")

	t.Run("land rejects a bad format", func(t *testing.T) {
		_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMergeLandCmd("land") }, "--format", "yaml", absent)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("land --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	t.Run("land on a saturated host refuses", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		// A readable receipt that names no worktrees gets past the live-link
		// guard, so the host-load gate is the thing that refuses.
		readable := filepath.Join(t.TempDir(), "empty-receipt.json")
		if err := os.WriteFile(readable, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMergeLandCmd("land") }, readable)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("land on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	t.Run("land fails on a missing receipt", func(t *testing.T) {
		stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMergeLandCmd("land") }, absent)
		if err == nil {
			t.Fatalf("land with a missing receipt = nil error (stdout %q), want a refusal", stdout)
		}
	})

	t.Run("revert rejects a bad format", func(t *testing.T) {
		_, _, err := cwCovExec(t, projects, newWorktreeMergeRevertCmd, "--format", "yaml", absent)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("revert --format yaml error = %v, want an unsupported-format refusal", err)
		}
	})

	t.Run("revert on a saturated host refuses", func(t *testing.T) {
		cwWtMergeSaturateHost(t)
		_, _, err := cwCovExec(t, projects, newWorktreeMergeRevertCmd, absent)
		if err == nil || !strings.Contains(err.Error(), "refusing to admit more CPU-heavy work") {
			t.Fatalf("revert on a saturated host error = %v, want a host-load refusal", err)
		}
	})

	t.Run("revert fails on a missing receipt", func(t *testing.T) {
		stdout, _, err := cwCovExec(t, projects, newWorktreeMergeRevertCmd, absent)
		if err == nil {
			t.Fatalf("revert with a missing receipt = nil error (stdout %q), want a refusal", stdout)
		}
		if !strings.Contains(stdout, "status:") {
			t.Fatalf("revert stdout = %q, want a status line even on failure", stdout)
		}
	})
}

func TestCwWtMergePrepareMergeOptionsAndLandMergeOptions(t *testing.T) {
	previousRoot := projectsRoot
	projectsRoot = filepath.Join(t.TempDir(), "projects")
	t.Cleanup(func() { projectsRoot = previousRoot })

	flags := worktreeMergeFlags{
		target: "main", route: "pr", onFailure: "revert", format: "json", cleanup: true, allowUnfenced: true,
		model: "m", runtime: "r", agentID: "a", cli: "c", provider: "p", timeout: time.Minute, retry: 2,
		prepareTimeout: time.Second, checkTimeout: 2 * time.Second, shardAttemptTimeout: 3 * time.Second,
		interval: 4 * time.Second, rebatchReceipt: "/tmp/r.json", progress: true, stopBeforeMerge: true,
		takeOverLane: true, laneReason: "reason",
	}
	prepare := prepareMergeOptions(flags, []string{"/tmp/wt"}, nil, nil)
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

	land := landMergeOptions(flags, "/tmp/receipt.json", nil, nil, &bytes.Buffer{})
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

func TestCwWtMergeNewLandAliasMatchesWorktreeLand(t *testing.T) {
	alias := newLandCmd()
	direct := newWorktreeLandCmd()
	if alias.Use != direct.Use || alias.Short != direct.Short || alias.Long != direct.Long {
		t.Fatalf("wb land contract = %q/%q, want it identical to wb worktree land %q/%q", alias.Use, alias.Short, direct.Use, direct.Short)
	}
	if alias.Flags().Lookup("cleanup").DefValue != "true" {
		t.Fatalf("wb land --cleanup default = %q, want true", alias.Flags().Lookup("cleanup").DefValue)
	}
}

func TestCwWtMergeRootMergeCommandIsWired(t *testing.T) {
	command := newWorktreeMergeCmd()
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("merge Use = %q, want the documented form", command.Use)
	}
	names := map[string]bool{}
	for _, child := range command.Commands() {
		names[child.Name()] = true
	}
	for _, want := range []string{"prepare", "land", "resume", "revert", "acknowledge-landed-failed", "acknowledge-stranded-landing", "acknowledge-absorbed-conflict", "acknowledge-retired-publication", "seal-validation-failed", "supersede-validation-failed", "correct-self-supersession", "prepare-published-forward-repair", "prepare-conflict-replacement"} {
		if !names[want] {
			t.Fatalf("merge subcommands = %v, missing %q", names, want)
		}
	}
	if command.Flags().Lookup("take-over-lane") == nil {
		t.Fatal("merge is missing the shared lane takeover flag")
	}
}

// cwWtMergeRewriteReceipt persists a mutated receipt at its own path so a
// recovery verb can be aimed at one exact immutable shape. The fixture writes
// a prepare-shaped receipt; the recovery verbs each require a different
// terminal status, and rewriting is how this test reaches them without a real
// remote landing.
func cwWtMergeRewriteReceipt(t *testing.T, path string, receipt orchestrate.WorktreeMergeReceipt) {
	t.Helper()
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCwWtMergeAcknowledgeUnpublishedValidationFailureOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeUnpublishedValidationFailureCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: unpublished_validation_failure_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate-worktree: " + fixture.receipt.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".unpublished-validation-failure.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".unpublished-validation-failure.ack.json"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry run wrote an acknowledgement: %v", statErr)
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeUnpublishedValidationFailureCmd, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeUnpublishedValidationFailureAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode json acknowledgement %q: %v", jsonOut, err)
	}
	if decoded.ReceiptPath != fixture.receiptPath || decoded.Candidate.SHA != fixture.receipt.Candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want the fixture receipt identity", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeUnpublishedValidationFailureCmd,
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree merge prepare <preserved or different sources> --target main") {
		t.Fatalf("apply stdout %q missing the next-step hint", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".unpublished-validation-failure.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeLandedFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", fixture.receipt.Candidate.SHA)
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeLandedFailedCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-landed-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: landed_failure_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate: " + fixture.receipt.Candidate.SHA,
		"acknowledgement: " + fixture.receiptPath + ".landed-validation-failed.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeLandedFailedCmd,
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-landed-failed --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if !strings.Contains(applied, "current-target: ") {
		t.Fatalf("apply stdout %q missing the current target", applied)
	}
}

func TestCwWtMergeSupersedeValidationFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 2)
	for _, source := range fixture.sources {
		runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	}
	writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runCLIWorktreeGit(t, fixture.canonical, "add", "target.txt")
	runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: advance target for cwWt supersession")
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")
	replacement := createCLIWorktreeSource(t, fixture, "cw-wt-supersede-replacement", "feature/cw-wt-supersede-replacement", "replacement.txt", "replacement\n")
	runCLIWorktreeGit(t, replacement.WorktreeDir, "fetch", "origin")
	for _, source := range fixture.sources {
		runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeSupersedeValidationFailedCmd, fixture.receiptPath, replacement.WorktreeDir)
	if err != nil {
		t.Fatalf("supersede-validation-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_superseded",
		"receipt: " + fixture.receiptPath,
		"replacement: ",
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".validation-failed.superseded.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeSupersedeValidationFailedCmd,
		fixture.receiptPath, replacement.WorktreeDir, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("supersede-validation-failed --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".validation-failed.superseded.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the supersession acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeAbsorbedConflictOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	source := fixture.sources[0]
	// The source content must be reachable from the freshly fetched target,
	// and its worktree must be gone: this is exactly the absorbed-conflict
	// shape, where a later landing on the target carried the same content.
	runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	runCLIWorktreeGit(t, fixture.canonical, "fetch", "origin")
	runCLIWorktreeGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+source.Branch)
	// A generated spec index WB may be told to excuse, present on the target.
	writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "spec", "cw-wt", "README.md"), "generated index\n")
	runCLIWorktreeGit(t, fixture.canonical, "add", "spec/cw-wt/README.md")
	runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: add generated spec index for cwWt")
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt absorbed conflict"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeAbsorbedConflictCmd,
		fixture.receiptPath, "--derived-path", "spec/cw-wt/README.md")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: absorbed_conflict_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate-worktree: " + conflict.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".absorbed-conflict.ack.json",
		"excused derived paths: spec/cw-wt/README.md",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeAbsorbedConflictCmd,
		fixture.receiptPath, "--derived-path", "spec/cw-wt/README.md", "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeAbsorbedConflictAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode absorbed-conflict json %q: %v", jsonOut, err)
	}
	if len(decoded.ExcusedDerivedPaths) != 1 || decoded.ExcusedDerivedPaths[0] != "spec/cw-wt/README.md" {
		t.Fatalf("decoded acknowledgement excused paths = %v, want the audited spec index", decoded.ExcusedDerivedPaths)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeAbsorbedConflictCmd,
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree cleanup ") {
		t.Fatalf("apply stdout %q missing the cleanup hint", applied)
	}
	if !strings.Contains(applied, "status: ") {
		t.Fatalf("apply stdout %q missing the status line", applied)
	}
}

func TestCwWtMergeAcknowledgeRetiredPublicationOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	retired := fixture.receipt
	retired.Status = orchestrate.WorktreeMergeConflict
	retired.PullRequest = "https://example.test/acme/app/pull/7"
	retired.PublishedCandidateSHA = retired.Candidate.SHA
	retired.Failure = "cwWt retired publication"
	retired.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, retired)

	cwWtMergeFakeGHPullRequest(t, retired.PullRequest, `{"state":"CLOSED","closedAt":"2026-01-02T03:04:05Z","mergedAt":"","mergeCommit":{"oid":""},"headRefName":"`+retired.Candidate.Branch+`","headRefOid":"`+retired.Candidate.SHA+`"}`)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeRetiredPublicationCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-retired-publication dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: retired_publication_acknowledged",
		"receipt: " + fixture.receiptPath,
		"pull-request: " + retired.PullRequest + " (CLOSED)",
		"candidate-worktree: " + retired.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".retired-publication.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeRetiredPublicationCmd, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-retired-publication --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeRetiredPublicationAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode retired-publication json %q: %v", jsonOut, err)
	}
	if decoded.PullRequestState != "CLOSED" || decoded.CandidateSHA != retired.Candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want a CLOSED pull request bound to the candidate", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeRetiredPublicationCmd,
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-retired-publication --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree merge prepare <the same sources> --target main") {
		t.Fatalf("apply stdout %q missing the next-step hint", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".retired-publication.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the retired-publication acknowledgement: %v", statErr)
	}
}

// cwWtMergeFakeGHPullRequest installs a fake `gh` on PATH whose `pr view`
// prints body, so a recovery verb that proves GitHub's own pull-request state
// can be driven without a network or a real GitHub account.
func cwWtMergeFakeGHPullRequest(t *testing.T, pullRequest, viewJSON string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"view\" ]; then\n" +
		"  printf '%s\\n' '" + viewJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' '{}'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if strings.TrimSpace(pullRequest) == "" {
		t.Fatal("cwWtMergeFakeGHPullRequest needs the pull request it stands in for")
	}
}

func TestCwWtMergeSealValidationFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeSealValidationFailedCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("seal-validation-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_seal_planned",
		"receipt: " + fixture.receiptPath,
		"current-target: ",
		"target-tree: ",
		"candidate: ",
		"dry-run only, pass --apply to create the ancestry seal candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeSealValidationFailedCmd, fixture.receiptPath,
		"--format", "json", "--model", "cwWt-model", "--agent-runtime", "cwWt-runtime", "--agent-id", "cwWt-agent", "--provider", "cwWt-provider", "--cli", "wb-cwwt")
	if err != nil {
		t.Fatalf("seal-validation-failed --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeValidationFailureSeal
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode seal json %q: %v", jsonOut, err)
	}
	if decoded.Status != "validation_failure_seal_planned" || decoded.CurrentTargetSHA == "" || decoded.TargetTreeSHA == "" {
		t.Fatalf("decoded seal = %+v, want a planned seal naming the target and its tree", decoded)
	}
}

func TestCwWtMergeAcknowledgeMissingCleanupOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	source := fixture.sources[0]

	landed := fixture.receipt
	landed.Phase = orchestrate.WorktreeMergePhaseLand
	landed.Status = orchestrate.WorktreeMergeLanded
	landed.Cleanup = true
	landed.LandingSHA = landed.Candidate.SHA
	landed.CanonicalSync = "not_checked_out"
	landed.Checks = orchestrate.PullRequestWaitResult{Status: orchestrate.PullRequestWaitPassed}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, landed)

	// Land the exact candidate on the remote target so the landing proof holds.
	runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", landed.Candidate.SHA)
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	// Legacy shape: the cleanup ran, but no terminal Work Log evidence remains.
	// Every receipted worktree and branch must therefore already be gone.
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(landed.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, fixture.canonical, "worktree", "prune")
	runCLIWorktreeGit(t, fixture.canonical, "branch", "-D", source.Branch)
	runCLIWorktreeGit(t, fixture.canonical, "branch", "-D", landed.Candidate.Branch)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeMissingCleanupCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-missing-cleanup dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: missing_cleanup_acknowledged",
		"receipt: " + fixture.receiptPath,
		"landing: " + landed.LandingSHA,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".missing-cleanup.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeMissingCleanupCmd, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-missing-cleanup --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeMissingCleanupAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode missing-cleanup json %q: %v", jsonOut, err)
	}
	if decoded.LandingSHA != landed.LandingSHA || decoded.CurrentTargetSHA == "" {
		t.Fatalf("decoded missing-cleanup ack = %+v, want the receipted landing and a current target", decoded)
	}
}

func TestCwWtMergeAcknowledgeStrandedLandingOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidateSHA := fixture.receipt.Candidate.SHA
	mergeCommitSHA := strings.Repeat("c", 40)
	pullRequestHeadSHA := strings.Repeat("b", 40)
	treeSHA := strings.Repeat("d", 40)

	stranded := fixture.receipt
	stranded.Phase = orchestrate.WorktreeMergePhaseLand
	stranded.Status = orchestrate.WorktreeMergeChecksPending
	stranded.PublishedCandidateSHA = candidateSHA
	stranded.PullRequest = "https://example.test/acme/app/pull/9"
	stranded.Failure = ""
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, stranded)

	view := `{"state":"MERGED","mergedAt":"2026-01-02T03:04:05Z","mergeCommit":{"oid":"` + mergeCommitSHA +
		`"},"headRefOid":"` + pullRequestHeadSHA + `","baseRefName":"` + stranded.Target + `"}`
	compareDiverged := candidateSHA + "..." + mergeCommitSHA
	cwWtMergeFakeGH(t, strings.Join([]string{
		"#!/bin/sh",
		`if [ "$1" = "pr" ] && [ "$2" = "view" ]; then`,
		`  printf '%s\n' '` + view + `'`,
		"  exit 0",
		"fi",
		`if [ "$1" = "api" ]; then`,
		`  ep="$2"`,
		`  case "$ep" in`,
		`    *compare/` + compareDiverged + `)`,
		`      printf 'HTTP/2 200 OK\n\n{"status":"diverged","base_commit":{"sha":"` + mergeCommitSHA + `"},"merge_base_commit":{"sha":"` + mergeCommitSHA + `"}}\n'`,
		"      ;;",
		`    *compare/*)`,
		`      rest="${ep#*compare/}"`,
		`      left="${rest%%...*}"`,
		`      printf 'HTTP/2 200 OK\n\n{"status":"ahead","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$left" "$left"`,
		"      ;;",
		`    */git/ref/heads/*)`,
		`      printf 'HTTP/2 200 OK\n\n{"object":{"sha":"` + mergeCommitSHA + `"}}\n'`,
		"      ;;",
		`    */git/commits/*)`,
		`      printf 'HTTP/2 200 OK\n\n{"tree":{"sha":"` + treeSHA + `"}}\n'`,
		"      ;;",
		"    *)",
		`      printf 'HTTP/2 200 OK\n\n{}\n'`,
		"      ;;",
		"  esac",
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")+"\n")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeStrandedLandingCmd, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-stranded-landing dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: stranded_landing_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate: " + candidateSHA,
		"pull-request-head: " + pullRequestHeadSHA,
		"candidate-landing: tree-identical (tree " + treeSHA + ")",
		"proved-landing: " + mergeCommitSHA,
		"current-target: " + mergeCommitSHA,
		"acknowledgement: " + fixture.receiptPath + ".stranded-landing.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeStrandedLandingCmd, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-stranded-landing --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeStrandedLandingAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode stranded-landing json %q: %v", jsonOut, err)
	}
	if decoded.PullRequestHeadSHA != pullRequestHeadSHA || decoded.ProvedLandingSHA != mergeCommitSHA || decoded.CandidateLandingTreeSHA != treeSHA {
		t.Fatalf("decoded acknowledgement = %+v, want the proved pull-request landing evidence", decoded)
	}
}

// cwWtMergeFakeGH installs script as a `gh` executable at the front of PATH.
func cwWtMergeFakeGH(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCwWtMergeAdoptPublishedCandidateOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt published externally"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	// The candidate branch really is published at the exact receipted SHA.
	runCLIWorktreeGit(t, candidate.Worktree, "push", "origin", candidate.Branch)

	view := `{"number":7,"state":"open","base":{"ref":"` + conflict.Target + `","repo":{"full_name":"` + conflict.Repository +
		`"}},"head":{"ref":"` + candidate.Branch + `","sha":"` + candidate.SHA + `","repo":{"full_name":"` + conflict.Repository + `"}}}`
	cwWtMergeFakeGH(t, strings.Join([]string{
		"#!/bin/sh",
		`if [ "$1" = "api" ]; then`,
		`  printf 'HTTP/2 200 OK\n\n` + view + `\n'`,
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")+"\n")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAdoptPublishedCandidateCmd, fixture.receiptPath, "7")
	if err != nil {
		t.Fatalf("adopt-published-candidate dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: published_candidate_adopted",
		"receipt: " + fixture.receiptPath,
		"pull-request: 7",
		"candidate: " + candidate.SHA,
		"acknowledgement: " + fixture.receiptPath + ".published-candidate.adopted.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAdoptPublishedCandidateCmd, fixture.receiptPath, "7", "--format", "json")
	if err != nil {
		t.Fatalf("adopt-published-candidate --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergePublishedCandidateAdoption
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode adoption json %q: %v", jsonOut, err)
	}
	if decoded.PullRequest != "7" || decoded.Candidate.SHA != candidate.SHA {
		t.Fatalf("decoded adoption = %+v, want pull request 7 bound to the receipted candidate", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAdoptPublishedCandidateCmd,
		fixture.receiptPath, "7", "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("adopt-published-candidate --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".published-candidate.adopted.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the adoption acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeReceiptCollisionOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	collision := fixture.receipt
	collision.Status = orchestrate.WorktreeMergePreparing
	collision.SourceRefreshes = []orchestrate.WorktreeMergeSourceRefresh{{
		RecordedAt: time.Now().UTC(),
		Sources:    append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
	}}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, collision)

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" {
		t.Fatalf("candidate Work Log view has no active claim: %+v", view.Claim)
	}
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	sourceSHA := collision.Sources[0].SHA
	args := []string{
		fixture.receiptPath,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-target", collision.TargetSHA,
		"--expected-candidate", candidate.SHA,
		"--expected-current-source", sourceSHA,
		"--expected-historical-refresh-source", sourceSHA,
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeReceiptCollisionCmd, args...)
	if err != nil {
		t.Fatalf("acknowledge-receipt-collision dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: receipt_collision_acknowledged",
		"receipt: " + fixture.receiptPath,
		"acknowledgement: " + fixture.receiptPath + ".receipt-collision.ack.json",
		"candidate: " + candidate.SHA,
		"current-target: " + collision.TargetSHA,
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeAcknowledgeReceiptCollisionCmd,
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("acknowledge-receipt-collision --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeReceiptCollisionAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode receipt-collision json %q: %v", jsonOut, err)
	}
	if decoded.ExpectedTargetSHA != collision.TargetSHA || decoded.ExpectedCandidateSHA != candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want the pinned target and candidate", decoded)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".receipt-collision.ack.json"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry run wrote an acknowledgement: %v", statErr)
	}
}

func TestCwWtMergePrepareConflictReplacementOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 2)
	candidate := fixture.receipt.Candidate

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt observed conflict"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	args := []string{
		fixture.receiptPath,
	}
	expectedSources := make([]string, 0, len(fixture.sources))
	for _, source := range conflict.Sources {
		args = append(args, source.Worktree)
		expectedSources = append(expectedSources, source.SHA)
	}
	args = append(args,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-current-target", conflict.TargetSHA,
		"--expected-source-sha", strings.Join(expectedSources, ","),
	)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergePrepareConflictReplacementCmd, args...)
	if err != nil {
		t.Fatalf("prepare-conflict-replacement dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"conflict_candidate_refresh_planned",
		"conflict receipt:",
		"current target:",
		"dry-run only, pass --apply to create the receipt-bound replacement candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergePrepareConflictReplacementCmd,
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("prepare-conflict-replacement --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeConflictCandidateRefresh
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode conflict replacement json %q: %v", jsonOut, err)
	}
	if decoded.Status != "conflict_candidate_refresh_planned" || decoded.CurrentTargetSHA != conflict.TargetSHA {
		t.Fatalf("decoded replacement plan = %+v, want a planned refresh at the current target", decoded)
	}
}

func TestCwWtMergePreparePublishedForwardRepairOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" || view.Claim.BaseSHA == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	// The historical self-supersession this repair verb exists to resolve: an
	// older writer bound the failed candidate as its own replacement.
	supersessionPath := fixture.receiptPath + ".validation-failed.superseded.ack.json"
	supersession := orchestrate.WorktreeMergeValidationFailureSupersession{
		SchemaVersion: 1, Status: "validation_failure_superseded",
		ReceiptPath: fixture.receiptPath, AcknowledgementPath: supersessionPath,
		ReceiptID: fixture.receipt.ID, ReceiptSHA256: hex.EncodeToString(receiptDigest[:]),
		ReceiptStatus: fixture.receipt.Status, Lane: fixture.receipt.Lane,
		Repository: fixture.receipt.Repository, Target: fixture.receipt.Target, ReceiptTargetSHA: fixture.receipt.TargetSHA,
		CurrentTargetSHA:  fixture.receipt.TargetSHA,
		OriginalCandidate: candidate, OriginalClaimBaseSHA: view.Claim.BaseSHA,
		Replacement: candidate, ReplacementClaimBaseSHA: view.Claim.BaseSHA,
		Sources: append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
		Actor:   "historical operator", Reason: "historical self-supersession", RecordedAt: time.Now().UTC(),
	}
	supersession.ID = cwWtMergeSelfSupersessionID(supersession)
	cwWtMergeWriteJSON(t, supersessionPath, supersession)
	supersessionBytes, err := os.ReadFile(supersessionPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionDigest := sha256.Sum256(supersessionBytes)

	source := fixture.receipt.Sources[0]
	args := []string{
		fixture.receiptPath, source.Worktree,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-supersession-sha256", hex.EncodeToString(supersessionDigest[:]),
		"--expected-current-target", fixture.receipt.TargetSHA,
		"--expected-source-sha", source.SHA,
		"--model", "cwWt-model", "--agent-runtime", "cwWt-runtime", "--agent-id", "cwWt-agent",
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergePreparePublishedForwardRepairCmd, args...)
	if err != nil {
		t.Fatalf("prepare-published-forward-repair dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: published_forward_repair_planned",
		"failed-receipt: " + fixture.receiptPath,
		"candidate: ",
		"current-target: " + fixture.receipt.TargetSHA,
		"dry-run only, pass --apply to create the distinct forward-repair candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergePreparePublishedForwardRepairCmd,
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("prepare-published-forward-repair --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergePublishedForwardRepair
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode forward-repair json %q: %v", jsonOut, err)
	}
	if decoded.Status != "published_forward_repair_planned" || decoded.SupersessionPath != supersessionPath {
		t.Fatalf("decoded forward-repair plan = %+v, want a plan bound to the historical supersession", decoded)
	}
}

// cwWtMergeWriteJSON persists any receipt-shaped artifact as indented JSON,
// so a test can stand in for a historical file WB would have written.
func cwWtMergeWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// cwWtMergeSelfSupersessionID reproduces the immutable identity hash the
// engine binds onto a supersession acknowledgement, so a test can stand in for
// the historical artifact an older writer left behind.
func cwWtMergeSelfSupersessionID(ack orchestrate.WorktreeMergeValidationFailureSupersession) string {
	hash := sha256.New()
	write := func(value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptStatus), ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.OriginalClaimBaseSHA, ack.ReplacementClaimBaseSHA, ack.Replacement.Task, ack.Replacement.Worktree, ack.Replacement.Branch, ack.Replacement.SHA} {
		write(value)
	}
	if ack.ObservedCandidateDescendantSHA != "" {
		write("observed_candidate_descendant_sha")
		write(ack.ObservedCandidateDescendantSHA)
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			write(value)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestCwWtMergeCorrectSelfSupersessionOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" || view.Claim.BaseSHA == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	supersessionPath := fixture.receiptPath + ".validation-failed.superseded.ack.json"
	supersession := orchestrate.WorktreeMergeValidationFailureSupersession{
		SchemaVersion: 1, Status: "validation_failure_superseded",
		ReceiptPath: fixture.receiptPath, AcknowledgementPath: supersessionPath,
		ReceiptID: fixture.receipt.ID, ReceiptSHA256: hex.EncodeToString(receiptDigest[:]),
		ReceiptStatus: fixture.receipt.Status, Lane: fixture.receipt.Lane,
		Repository: fixture.receipt.Repository, Target: fixture.receipt.Target, ReceiptTargetSHA: fixture.receipt.TargetSHA,
		CurrentTargetSHA:  fixture.receipt.TargetSHA,
		OriginalCandidate: candidate, OriginalClaimBaseSHA: view.Claim.BaseSHA,
		Replacement: candidate, ReplacementClaimBaseSHA: view.Claim.BaseSHA,
		Sources: append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
		Actor:   "historical operator", Reason: "historical self-supersession", RecordedAt: time.Now().UTC(),
	}
	supersession.ID = cwWtMergeSelfSupersessionID(supersession)
	cwWtMergeWriteJSON(t, supersessionPath, supersession)
	supersessionBytes, err := os.ReadFile(supersessionPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionDigest := sha256.Sum256(supersessionBytes)

	// The corrected replacement is a distinct clean claimed candidate that
	// still contains every immutable receipted source.
	replacement := createCLIWorktreeSource(t, fixture, "cw-wt-correct-replacement", "feature/cw-wt-correct-replacement", "replacement.txt", "replacement\n")
	for _, source := range fixture.receipt.Sources {
		runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", source.Branch)
	}

	args := []string{
		fixture.receiptPath, replacement.WorktreeDir,
		"--expected-supersession-sha256", hex.EncodeToString(supersessionDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeCorrectSelfSupersessionCmd, args...)
	if err != nil {
		t.Fatalf("correct-self-supersession dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_self_supersession_corrected",
		"receipt: " + fixture.receiptPath,
		"replacement: ",
		"correction: " + fixture.receiptPath + ".validation-failed.self-supersession.corrected.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, newWorktreeMergeCorrectSelfSupersessionCmd,
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("correct-self-supersession --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeSelfSupersessionCorrection
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode correction json %q: %v", jsonOut, err)
	}
	if decoded.Status != "validation_failure_self_supersession_corrected" || decoded.SupersessionPath != supersessionPath {
		t.Fatalf("decoded correction = %+v, want a correction bound to the historical supersession", decoded)
	}
}
