// Package sessionlaunch owns the fixed target-side harness launch boundary.
// Request data selects only one closed harness specification; it never becomes
// a shell program or executable name.
package sessionlaunch

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

const (
	RuntimeCodex      = "codex"
	RuntimeClaudeCode = "claude-code"
)

type HarnessSpec struct {
	Runtime    string
	Model      string
	Executable string
	Args       []string
}

func ValidateHarnessSelection(sourceRuntime, requested string) error {
	runtime := strings.TrimSpace(requested)
	if runtime == "" {
		runtime = strings.TrimSpace(sourceRuntime)
	}
	if runtime != RuntimeCodex && runtime != RuntimeClaudeCode {
		return fmt.Errorf("requested harness %q is unsupported; supported harnesses are %q and %q", runtime, RuntimeCodex, RuntimeClaudeCode)
	}
	return nil
}

func harnessSpec(request sessionmove.Request, worktree string) (HarnessSpec, error) {
	authority := sessionauthority.Launch{
		AggregateID: request.HandoffID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, SourceRuntime: request.SourceRuntime,
		SourceModel: request.SourceModel, RequestedHarness: request.RequestedHarness,
		ContinuationKind: sessionauthority.ContinuationTracked, ContinuationPath: request.HandoverPath,
	}
	return harnessSpecForAuthority(authority, worktree)
}

func harnessSpecForAuthority(authority sessionauthority.Launch, worktree string) (HarnessSpec, error) {
	runtime := strings.TrimSpace(authority.RequestedHarness)
	if runtime == "" {
		runtime = strings.TrimSpace(authority.SourceRuntime)
	}
	if err := ValidateHarnessSelection(authority.SourceRuntime, authority.RequestedHarness); err != nil {
		return HarnessSpec{}, err
	}
	model := ""
	if runtime == strings.TrimSpace(authority.SourceRuntime) {
		model = strings.TrimSpace(authority.SourceModel)
	}
	prompt := launchPromptForAuthority(authority)
	switch runtime {
	case RuntimeCodex:
		args := []string{"-C", worktree}
		if model != "" {
			args = append(args, "-m", model)
		}
		args = append(args, prompt)
		return HarnessSpec{Runtime: runtime, Model: model, Executable: "codex", Args: args}, nil
	case RuntimeClaudeCode:
		args := make([]string, 0, 5)
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, "--name", authority.SuccessorWBSessionID, prompt)
		return HarnessSpec{Runtime: runtime, Model: model, Executable: "claude", Args: args}, nil
	default:
		return HarnessSpec{}, fmt.Errorf("requested harness %q is unsupported; supported harnesses are %q and %q", runtime, RuntimeCodex, RuntimeClaudeCode)
	}
}

func launchPrompt(request sessionmove.Request) string {
	return launchPromptForAuthority(sessionauthority.Launch{
		AggregateID: request.HandoffID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, ContinuationKind: sessionauthority.ContinuationTracked,
		ContinuationPath: request.HandoverPath,
	})
}

func launchPromptForAuthority(authority sessionauthority.Launch) string {
	if authority.ContinuationKind == sessionauthority.ContinuationTracked {
		return fmt.Sprintf(
			"Continue WB handoff %s as session %s from predecessor %s. Read the immutable handover document at %s before acting.",
			authority.AggregateID, authority.SuccessorWBSessionID, authority.PredecessorWBSessionID, authority.ContinuationPath,
		)
	}
	return fmt.Sprintf(
		"Continue WB parked session %s as session %s from predecessor %s. Read the file named by WB_SESSION_CONTINUATION_FILE before acting.",
		authority.AggregateID, authority.SuccessorWBSessionID, authority.PredecessorWBSessionID,
	)
}
