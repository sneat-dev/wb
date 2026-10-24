package agents

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// dshProfile is the profile name the harness documents for "answer one task,
// print the final message, and exit".
const dshProfile = "headless"

// dshModelPatchName is the private overlay that pins the model for one run.
const dshModelPatchName = "dsh-model-patch.yml"

// dshHarness adapts the DeepSeek harness (`dsh`). It is the least instrumented
// of the three: no JSON events, no usage report, and the task can only travel on
// the command line.
type dshHarness struct{}

func (dshHarness) Name() string { return HarnessDSH }

func (dshHarness) Capabilities() HarnessCapabilities {
	return HarnessCapabilities{
		Harness: HarnessDSH, JSONEvents: false, TaskDelivery: TaskDeliveryArgv,
		TokenUsage: UsageUnavailable, Cost: CostUnavailable, Effort: EffortUnsupported,
		Credential: CredentialHarnessStore, Isolation: IsolationUserHome,
	}
}

// dshPatchEntry is the compose-tree overlay that sets the default model. The
// field order mirrors what the harness prints, so a reader comparing the patch
// with `--dump-config` sees the same shape.
type dshPatchEntry struct {
	ID     string `yaml:"id"`
	Config struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
	} `yaml:"config"`
}

// dshModelPatch renders the overlay for one model.
func dshModelPatch(model string) ([]byte, error) {
	entry := dshPatchEntry{ID: "agent-default-model"}
	entry.Config.Provider = ProviderDshDefault
	entry.Config.Model = model
	raw, err := yaml.Marshal([]dshPatchEntry{entry})
	if err != nil {
		return nil, fmt.Errorf("render dsh model patch: %w", err)
	}
	return raw, nil
}

// Prepare writes the model overlay and builds the invocation.
//
// `dsh` cannot read its task from stdin, and its launcher consumes `--` before
// the inner parser, so the task travels as one argv element. A task that starts
// with "-" would be read as an option, so it is prefixed with a newline: the
// text is preserved, and the record states that the delivery was normalized.
func (dshHarness) Prepare(launch Launch) (HarnessLaunch, error) {
	if strings.TrimSpace(launch.WorktreeDir) == "" {
		return HarnessLaunch{}, fmt.Errorf("dsh launch requires a working directory")
	}
	model := strings.TrimSpace(launch.Model)
	if model == "" {
		return HarnessLaunch{}, fmt.Errorf("dsh launch requires a model")
	}
	if strings.TrimSpace(launch.RunDir) == "" {
		return HarnessLaunch{}, fmt.Errorf("dsh launch requires a private run directory for its model overlay")
	}
	task, err := dshTaskArgument(launch.Task)
	if err != nil {
		return HarnessLaunch{}, err
	}
	patch, err := dshModelPatch(model)
	if err != nil {
		return HarnessLaunch{}, err
	}
	patchPath := filepath.Join(launch.RunDir, dshModelPatchName)
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		return HarnessLaunch{}, fmt.Errorf("write dsh model overlay: %w", err)
	}
	argv := []string{
		"--profile", dshProfile,
		"--patch", patchPath,
		task,
	}
	return HarnessLaunch{Argv: argv, PrivateFiles: []string{patchPath}}, nil
}

// dshTaskArgument returns the one argument the harness receives, refusing a task
// that cannot be delivered without being altered beyond a leading newline.
func dshTaskArgument(task string) (string, error) {
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("dsh launch requires a non-empty task")
	}
	if len(task) > maxHarnessArgvBytes {
		return "", fmt.Errorf("dsh delivers its task on the command line and cannot carry %d bytes; keep the task under %d bytes or use a harness that reads stdin", len(task), maxHarnessArgvBytes)
	}
	if strings.HasPrefix(task, "-") {
		return "\n" + task, nil
	}
	return task, nil
}

// Summarize treats the stream as the final message: `dsh --profile headless`
// prints exactly that and nothing structured. There is no usage or cost to
// extract, so neither is invented.
func (dshHarness) Summarize(reader io.Reader) HarnessSummary {
	raw, err := io.ReadAll(io.LimitReader(reader, harnessLogLimit))
	if err != nil {
		return HarnessSummary{}
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		return HarnessSummary{}
	}
	return HarnessSummary{TurnCompleted: true, FinalMessage: message}
}

// Actions reports nothing: `dsh` exposes no action stream, and inventing one
// from prose would be a guess.
func (dshHarness) Actions(io.Reader, int) []string { return nil }

// dshTaskNormalized reports whether a task needed the leading-newline delivery
// form, so the record can state it rather than leaving a silent difference.
func dshTaskNormalized(task string) bool { return strings.HasPrefix(task, "-") }
