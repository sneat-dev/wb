package sessionlaunch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestHarnessSpecUsesFixedSameAndCrossHarnessArgv(t *testing.T) {
	request := launchTestRequest()

	codex, err := harnessSpec(request, "/target/worktree")
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := []string{"-C", "/target/worktree", "-m", "gpt-5", launchPrompt(request)}
	if codex.Runtime != RuntimeCodex || codex.Executable != "codex" || codex.Model != "gpt-5" || !reflect.DeepEqual(codex.Args, wantCodex) {
		t.Fatalf("codex spec = %#v, want argv %#v", codex, wantCodex)
	}

	request.RequestedHarness = RuntimeClaudeCode
	claude, err := harnessSpec(request, "/target/worktree")
	if err != nil {
		t.Fatal(err)
	}
	wantClaude := []string{"--name", request.SuccessorWBSessionID, launchPrompt(request)}
	if claude.Runtime != RuntimeClaudeCode || claude.Executable != "claude" || claude.Model != "" || !reflect.DeepEqual(claude.Args, wantClaude) {
		t.Fatalf("cross-harness spec = %#v, want argv %#v", claude, wantClaude)
	}
	if strings.Contains(strings.Join(claude.Args, "\x00"), "gpt-5") {
		t.Fatal("cross-harness argv reused the source harness model")
	}
}

func TestHarnessSpecRejectsUnsupportedHarness(t *testing.T) {
	request := launchTestRequest()
	request.RequestedHarness = "shell"
	if _, err := harnessSpec(request, "/target/worktree"); err == nil || !strings.Contains(err.Error(), "supported") {
		t.Fatalf("error = %v, want supported-harness refusal", err)
	}
}

func TestPrivateContinuationNeverAppearsInHarnessArgv(t *testing.T) {
	secretPath := "/private/park-resumes/resume-secret/successor-context.md"
	secretBody := "private continuation marker that must remain out of argv"
	authority := sessionauthority.Launch{
		AggregateID: "resume-secret", SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceRuntime: RuntimeCodex, SourceModel: "gpt-5", ContinuationKind: sessionauthority.ContinuationPrivate,
		ContinuationPath: secretPath, ContinuationDigest: string(sessionmove.DigestBytes([]byte(secretBody))),
	}
	spec, err := harnessSpecForAuthority(authority, "/target/worktree")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, "\x00")
	if strings.Contains(joined, secretPath) || strings.Contains(joined, secretBody) ||
		!strings.Contains(joined, sessionauthority.ContinuationEnvironment) {
		t.Fatalf("private harness argv violates continuation contract: %#v", spec.Args)
	}
}

func launchTestRequest() sessionmove.Request {
	request := sessionmove.Request{
		HandoffID: "handoff-123", SuccessorWBSessionID: "wbs-successor",
		PredecessorWBSessionID: "wbs-source", SourceMachine: "source", TargetMachine: "hetzner-vm1",
		SourceRuntime: RuntimeCodex, SourceModel: "gpt-5", HandoverPath: ".wb/handoffs/handoff-123.md",
		WorkLogReference:   "worklog:session-move/session-move-run/" + strings.Repeat("a", 64),
		SourceOfferMessage: "Session handoff offered", SourceOfferNextAction: "Continue from .wb/handoffs/handoff-123.md",
	}
	request.SourceOfferDigest = sessionmove.DigestSourceOffer(request.SourceOfferMessage, request.SourceOfferNextAction)
	return request
}
