package cmdstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

func TestStreamStatusReportsMissingMemberPullRequestRecovery(t *testing.T) {
	t.Parallel()
	command := newStreamStatusCmd(testRuntime(), testDependencies())
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	failureAt := time.Date(2026, 9, 12, 11, 17, 40, 0, time.UTC)
	status := streams.Status{Stream: "recovery", Phase: streams.PhaseOpen, Branch: "stream/recovery", Members: []streams.MemberStatus{{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: "/tmp/acme-library", Branch: "stream/recovery", Base: "main", PullRequestMissing: "no open pull request is recorded or currently discoverable", LastPublicationError: &streams.PublicationFailure{Detail: "push rejected as non-fast-forward\nfull historical git transcript",
		OccurredAt: &failureAt,
	},
	}},
	}

	err := streamStatusOutput(testRuntime(), command, "text", status)
	exit, ok := err.(*exitError)
	if !ok || exit.code != exitFindings {
		t.Fatalf("status error = %#v, want exit findings", err)
	}
	if output := stdout.String(); !strings.Contains(output, "wb stream join recovery acme/library") {
		t.Fatalf("status output = %q, want the sanctioned recovery command", output)
	}
	if output := stdout.String(); !strings.Contains(output, "last publication attempt failed at 2026-09-12T11:17:40Z") ||
		strings.Contains(output, "full historical git transcript") {
		t.Fatalf("status output = %q, want timestamped historical summary without a live-looking transcript", output)
	}

	jsonCommand := newStreamStatusCmd(testRuntime(), testDependencies())
	var jsonOut bytes.Buffer
	jsonCommand.SetOut(&jsonOut)
	status.Members[0].PullRequestRecovery = "wb stream join recovery acme/library"
	err = streamStatusOutput(testRuntime(), jsonCommand, "json", status)
	exit, ok = err.(*exitError)
	if !ok || exit.code != exitFindings {
		t.Fatalf("JSON status error = %#v, want exit findings", err)
	}
	var envelope struct {
		Outcome  string `json:"outcome"`
		Evidence struct {
			Members []streams.MemberStatus `json:"members"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &envelope); err != nil {
		t.Fatalf("parse JSON status %q: %v", jsonOut.String(), err)
	}
	if envelope.Outcome != outcomeFindings || len(envelope.Evidence.Members) != 1 ||
		envelope.Evidence.Members[0].PullRequestRecovery != "wb stream join recovery acme/library" {
		t.Fatalf("JSON status envelope = %#v, want a finding with the exact recovery verb", envelope)
	}
	jsonMember := envelope.Evidence.Members[0]
	if jsonMember.PullRequestMissing != "no open pull request is recorded or currently discoverable" ||
		jsonMember.LastPublicationError == nil || jsonMember.LastPublicationError.OccurredAt == nil ||
		!jsonMember.LastPublicationError.OccurredAt.Equal(failureAt) {
		t.Fatalf("JSON member status = %#v, want separate current and timestamped historical findings", jsonMember)
	}

	blockedCommand := newStreamStatusCmd(testRuntime(), testDependencies())
	var blockedOut bytes.Buffer
	blockedCommand.SetOut(&blockedOut)
	status.Members[0].PullRequestRecovery = ""
	status.Members[0].PullRequestBlocked = "stream branch diverged: owner decision required"
	if err := streamStatusOutput(testRuntime(), blockedCommand, "text", status); err == nil {
		t.Fatal("blocked status returned success")
	}
	if output := blockedOut.String(); !strings.Contains(output, "blocked:") || strings.Contains(output, "recover: wb stream join") {
		t.Fatalf("blocked status output = %q, want the owner-decision block without a retry loop", output)
	}

	unrecordedCommand := newStreamStatusCmd(testRuntime(), testDependencies())
	var unrecordedOut bytes.Buffer
	unrecordedCommand.SetOut(&unrecordedOut)
	status.Members[0].PullRequest = 242
	status.Members[0].PullRequestURL = "https://example.test/pull/242"
	status.Members[0].PullRequestMissing = ""
	status.Members[0].PullRequestBlocked = ""
	status.Members[0].PullRequestRecovery = "wb stream join recovery acme/library"
	status.Members[0].PullRequestUnrecorded = true
	if err := streamStatusOutput(testRuntime(), unrecordedCommand, "text", status); err == nil {
		t.Fatal("unrecorded remote PR status returned success")
	}
	if output := unrecordedOut.String(); !strings.Contains(output, "open pull request #242 exists") ||
		!strings.Contains(output, "recover: wb stream join recovery acme/library") || strings.Contains(output, "no draft pull request") {
		t.Fatalf("unrecorded PR output = %q, want discovered PR identity and persistence recovery", output)
	}
}

func TestStreamStartRejectsAnInvalidName(t *testing.T) {
	t.Parallel()
	prompt := "/never-read/prompt.txt"
	var stdout, stderr bytes.Buffer
	code := executeFamily([]string{
		"stream", "start", "not a name", "acme/app",
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt, "--format", "json",
	}, &stdout, &stderr)
	// An ambiguous invocation is exit 2, not exit 1: 1 means "the work is
	// broken", and a caller that branches on the contract must be able to
	// tell a bad invocation from a real failure.
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (usage); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must start with a letter or digit") {
		t.Errorf("stderr = %q, want the name rule", stderr.String())
	}
	// A caller that asked for JSON must never get an empty stdout.
	var envelope struct {
		Verb        string `json:"verb"`
		Outcome     string `json:"outcome"`
		RefusalCode string `json:"refusal_code"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.Outcome != "refused" || envelope.RefusalCode != streams.RefusalUsage {
		t.Fatalf("envelope = %#v, want a usage refusal", envelope)
	}
}

func TestStreamJoinRejectsAnUnsupportedRoleWithTheUsageEnvelope(t *testing.T) {
	t.Parallel()
	prompt := "/never-read/prompt.txt"
	var stdout, stderr bytes.Buffer
	code := executeFamily([]string{
		"stream", "join", "somename", "acme/app", "--role", "bogus",
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt, "--format", "json",
	}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitUsage, stderr.String())
	}
	var envelope struct {
		RefusalCode        string   `json:"refusal_code"`
		SanctionedCommand  string   `json:"sanctioned_command"`
		SanctionedCommands []string `json:"sanctioned_commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.RefusalCode != streams.RefusalUsage {
		t.Fatalf("refusal_code = %q", envelope.RefusalCode)
	}
	// The singular field must be one runnable command, not a joined string.
	if !strings.HasPrefix(envelope.SanctionedCommand, "wb stream join") || strings.Contains(envelope.SanctionedCommand, "||") {
		t.Errorf("sanctioned_command = %q, want one runnable command", envelope.SanctionedCommand)
	}
	if len(envelope.SanctionedCommands) == 0 {
		t.Error("sanctioned_commands is empty")
	}
}
