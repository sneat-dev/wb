package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// antigravityHarness adapts agy's headless NDJSON protocol. CLI 1.1.13
// accepts prompts only via --print, so task bytes live in a private file.
type antigravityHarness struct{}

func (antigravityHarness) Name() string { return HarnessAntigravity }

func (antigravityHarness) Capabilities() HarnessCapabilities {
	return HarnessCapabilities{
		Harness: HarnessAntigravity, JSONEvents: true, TaskDelivery: TaskDeliveryPrivateFile,
		TokenUsage: UsageFromEvents, Cost: CostUnavailable, Effort: EffortConfig,
		Credential: CredentialHarnessStore, Isolation: IsolationUserHome,
	}
}

func (antigravityHarness) Prepare(launch Launch) (HarnessLaunch, error) {
	if strings.TrimSpace(launch.WorktreeDir) == "" || strings.TrimSpace(launch.Model) == "" || strings.TrimSpace(launch.Task) == "" || strings.TrimSpace(launch.RunDir) == "" {
		return HarnessLaunch{}, fmt.Errorf("antigravity launch requires a working directory, private run directory, model, and task")
	}
	if launch.Effort != "" && launch.Effort != "low" && launch.Effort != "medium" && launch.Effort != "high" {
		return HarnessLaunch{}, fmt.Errorf("antigravity effort must be low, medium, or high")
	}
	briefDir := filepath.Join(launch.RunDir, "antigravity-brief")
	if err := os.MkdirAll(briefDir, 0o700); err != nil {
		return HarnessLaunch{}, fmt.Errorf("create private antigravity brief directory: %w", err)
	}
	taskPath := filepath.Join(briefDir, "task.txt")
	if err := os.WriteFile(taskPath, []byte(launch.Task), 0o600); err != nil {
		return HarnessLaunch{}, fmt.Errorf("write private antigravity task: %w", err)
	}
	// Workspace edits are auto-allowed; command permissions remain governed by
	// the operator's agy settings. Keep terminal restrictions enabled and never
	// request blanket permission bypass for an unattended worker.
	argv := []string{"--output-format", "stream-json", "--mode", "accept-edits", "--sandbox", "--model", launch.Model, "--add-dir", briefDir}
	if launch.Effort != "" {
		argv = append(argv, "--effort", launch.Effort)
	}
	if launch.Timeout > 0 {
		argv = append(argv, "--print-timeout", launch.Timeout.String())
	}
	argv = append(argv, "--print", "Read the task file at "+taskPath+" and carry out its instructions in the workspace at "+launch.WorktreeDir+". Resolve relative task paths from that workspace. Do not reproduce the task text in your response.")
	return HarnessLaunch{Argv: argv, PrivateFiles: []string{taskPath}, SplitStderr: true}, nil
}

type antigravityUsage struct {
	Input     *int `json:"input_tokens"`
	Output    *int `json:"output_tokens"`
	Thinking  *int `json:"thinking_tokens"`
	CacheRead *int `json:"cache_read_tokens"`
}

type antigravityEvent struct {
	Event string `json:"event"`
	Step  *struct {
		Index int    `json:"step_index"`
		State string `json:"state"`
		Type  string `json:"step_type"`
		Tool  string `json:"tool_name"`
		Info  *struct {
			Parameters struct {
				Command string `json:"CommandLine"`
			} `json:"parameters"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result *struct {
		Status   string            `json:"status"`
		Response string            `json:"response"`
		Error    string            `json:"error"`
		Usage    *antigravityUsage `json:"usage"`
	} `json:"result"`
}

// readAntigravity uses final cumulative usage, never per-step usage (which
// would double count), and requires an explicit successful result.
func readAntigravity(reader io.Reader, limit int) (HarnessSummary, []string) {
	var summary HarnessSummary
	summary.Diagnostics = append(summary.Diagnostics, "antigravity uses accept-edits with terminal sandboxing; command permissions use the operator's agy settings and may be soft-denied; validate the resulting work")
	var actions []string
	seen := map[int]bool{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), harnessLogLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event antigravityEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			summary.MalformedLines++
			continue
		}
		if step := event.Step; event.Event == "step_update" && step != nil && step.Type == "tool" && step.State == "DONE" && !seen[step.Index] {
			seen[step.Index] = true
			summary.ToolCalls++
			if limit > 0 {
				action := "called tool " + step.Tool
				if step.Tool == "run_command" && step.Info != nil {
					action = "ran: " + step.Info.Parameters.Command
				}
				actions = append(actions, condense(action))
				if len(actions) > limit {
					actions = actions[1:]
				}
			}
		}
		if result := event.Result; event.Event == "result" && result != nil {
			summary.TurnCompleted = result.Status == "SUCCESS" && result.Error == ""
			summary.TurnFailed = !summary.TurnCompleted
			summary.FinalMessage = result.Response
			if summary.TurnFailed {
				summary.Diagnostics = append(summary.Diagnostics, "antigravity status "+result.Status+": "+result.Error)
			}
			if usage := result.Usage; usage != nil && (usage.Input != nil || usage.Output != nil || usage.Thinking != nil || usage.CacheRead != nil) {
				summary.Tokens = &Tokens{Input: usage.Input, Output: usage.Output, Reasoning: usage.Thinking, CacheRead: usage.CacheRead}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		summary.TurnFailed = true
		summary.TurnCompleted = false
		summary.Diagnostics = append(summary.Diagnostics, "read antigravity events: "+err.Error())
	}
	return summary, actions
}

func (antigravityHarness) Summarize(reader io.Reader) HarnessSummary {
	summary, _ := readAntigravity(reader, 0)
	return summary
}

func (antigravityHarness) Actions(reader io.Reader, limit int) []string {
	_, actions := readAntigravity(reader, limit)
	return actions
}
