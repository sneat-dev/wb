package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/spf13/cobra"
)

// validateParkedRemoteBundle refuses a bundle with no owned worktrees to
// resume, since remote resume has nothing to reconstruct.
func TestValidateParkedRemoteBundleRejectsEmptyWorktrees(t *testing.T) {
	t.Parallel()
	err := validateParkedRemoteBundle(sessionpark.Bundle{ParkedSessionID: "p1"}, "vm-2")
	if err == nil || !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("want empty-worktrees refusal, got %v", err)
	}
}

// A dirty worktree is not remotely reconstructable at an exact pushed
// commit, so validateParkedRemoteBundle refuses it by name.
func TestValidateParkedRemoteBundleRejectsDirtyWorktree(t *testing.T) {
	t.Parallel()
	err := validateParkedRemoteBundle(sessionpark.Bundle{
		ParkedSessionID: "p1",
		Worktrees: []sessionpark.Worktree{{
			Repository: "acme/app", WorktreeDir: "/wt/app", Head: "aaa", RemoteHead: "aaa",
			WorkLogReference: "wl-1", OwnerEventID: "ev-1", Dirty: true,
		}},
	}, "vm-2")
	if err == nil || !strings.Contains(err.Error(), "not remotely reconstructable") {
		t.Fatalf("want dirty-worktree refusal, got %v", err)
	}
}

// parkResumeDiagnosticDir must not point diagnostics anywhere when either
// input needed to build a safe, scoped path is missing.
func TestParkResumeDiagnosticDirEmptyInputsYieldEmptyPath(t *testing.T) {
	t.Parallel()
	if got := parkResumeDiagnosticDir("", "p1"); got != "" {
		t.Fatalf("want empty diagnostic dir for empty home, got %q", got)
	}
	if got := parkResumeDiagnosticDir("/home/x", ""); got != "" {
		t.Fatalf("want empty diagnostic dir for empty session id, got %q", got)
	}
}

func TestParkResumeDiagnosticDirJoinsHomeAndSession(t *testing.T) {
	t.Parallel()
	got := parkResumeDiagnosticDir("/home/x", "p1")
	if !strings.Contains(got, "p1") || !strings.Contains(got, "worklogs") {
		t.Fatalf("want a worklogs diagnostic path naming the session, got %q", got)
	}
}

func TestWriteSessionResumeOutputJSONEncodesFields(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	err := writeSessionResumeOutput(command, "json", sessionResumeOutput{ParkedSessionID: "p1", Status: "resumed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), `"parked_session_id": "p1"`) {
		t.Fatalf("want json output carrying parked_session_id, got %q", out.String())
	}
}

func TestWriteSessionResumeOutputTextReportsReplayed(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	err := writeSessionResumeOutput(command, "text", sessionResumeOutput{
		ParkedSessionID: "p1", SuccessorWBSessionID: "s2", TargetMachine: "vm-2", MemberCount: 3, Replay: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "replayed parked session p1 as successor s2 on vm-2 with 3 worktrees") {
		t.Fatalf("want a replayed text summary, got %q", out.String())
	}
}

func TestParkedRemoteSSHConfigRejectsMismatchedTarget(t *testing.T) {
	t.Parallel()
	route := &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteLocal, TargetMachine: "vm-1"}
	_, err := parkedRemoteSSHConfig(route, "vm-2", "", "")
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("want mismatched-target refusal, got %v", err)
	}
}

func TestReadParkContextReadsStdinOnDash(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("continuation text"))
	got, err := readParkContext(command, "-")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "continuation text" {
		t.Fatalf("want stdin contents returned verbatim, got %q", got)
	}
}

func TestReadSessionReceiveParkEnvelopeRejectsBlankInput(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("   "))
	_, err := readSessionReceiveParkEnvelope(command)
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("want empty-envelope refusal, got %v", err)
	}
}
