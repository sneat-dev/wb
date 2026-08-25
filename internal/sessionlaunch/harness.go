// Package sessionlaunch owns the fixed target-side harness launch boundary.
// Request data selects only one closed harness specification; it never becomes
// a shell program or executable name.
package sessionlaunch

import (
	"fmt"
	"strings"

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
	runtime := strings.TrimSpace(request.RequestedHarness)
	if runtime == "" {
		runtime = strings.TrimSpace(request.SourceRuntime)
	}
	if err := ValidateHarnessSelection(request.SourceRuntime, request.RequestedHarness); err != nil {
		return HarnessSpec{}, err
	}
	model := ""
	if runtime == strings.TrimSpace(request.SourceRuntime) {
		model = strings.TrimSpace(request.SourceModel)
	}
	prompt := launchPrompt(request)
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
		args = append(args, "--name", request.SuccessorWBSessionID, prompt)
		return HarnessSpec{Runtime: runtime, Model: model, Executable: "claude", Args: args}, nil
	default:
		return HarnessSpec{}, fmt.Errorf("requested harness %q is unsupported; supported harnesses are %q and %q", runtime, RuntimeCodex, RuntimeClaudeCode)
	}
}

func launchPrompt(request sessionmove.Request) string {
	return fmt.Sprintf(
		"Continue WB handoff %s as session %s from predecessor %s. Read the immutable handover document at %s before acting.",
		request.HandoffID, request.SuccessorWBSessionID, request.PredecessorWBSessionID, request.HandoverPath,
	)
}
