package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// openCodeHarness adapts `opencode run`. Its JSON vocabulary is flatter than
// Codex's: one event per line with a typed part, and no guaranteed terminal
// event, so completion is inferred from positive evidence rather than from a
// turn event that may never arrive.
type openCodeHarness struct{}

func (openCodeHarness) Name() string { return HarnessOpenCode }

func (openCodeHarness) Capabilities() HarnessCapabilities {
	return HarnessCapabilities{
		Harness: HarnessOpenCode, JSONEvents: true, TaskDelivery: TaskDeliveryStdin,
		TokenUsage: UsageFromEvents, Cost: CostHarnessEstimated, Effort: EffortVariant,
		Credential: CredentialHarnessStore, Isolation: IsolationUserHome,
	}
}

// Prepare builds the non-interactive OpenCode invocation.
//
// `--standalone` is mandatory: without it the harness attaches to a long-lived
// background server that lives outside the worker's process group, so timeout,
// cancellation and process-tree cleanup would silently stop applying.
// `--auto` answers the harness's own permission prompts, because a detached
// worker has no terminal to answer them on.
func (openCodeHarness) Prepare(launch Launch) (HarnessLaunch, error) {
	if strings.TrimSpace(launch.WorktreeDir) == "" {
		return HarnessLaunch{}, fmt.Errorf("opencode launch requires a working directory")
	}
	model := strings.TrimSpace(launch.Model)
	if model == "" {
		return HarnessLaunch{}, fmt.Errorf("opencode launch requires a model")
	}
	if strings.Contains(model, "#") {
		return HarnessLaunch{}, fmt.Errorf("opencode model %q must not contain a variant; configure the profile's variant separately", model)
	}
	if variant := strings.TrimSpace(launch.Variant); variant != "" {
		model += "#" + variant
	}
	argv := []string{
		"run", "--standalone", "--auto", "--format", "json",
		"--model", model,
	}
	return HarnessLaunch{Argv: argv, Stdin: launch.Task}, nil
}

type openCodeEvent struct {
	Type string `json:"type"`
	Part *struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Tool  string `json:"tool"`
		State *struct {
			Status string `json:"status"`
			Input  *struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"state"`
		Cost   *float64 `json:"cost"`
		Tokens *struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cache     struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// openCodeSummary accumulates what one JSON stream proved.
type openCodeSummary struct {
	HarnessSummary
	sawEvidence bool
	lastText    string
	input       int
	output      int
	reasoning   int
	cacheRead   int
	cacheWrite  int
	steps       int
	tokensSeen  bool
	costTotal   float64
	costSeen    bool
}

func (openCodeHarness) Summarize(reader io.Reader) HarnessSummary {
	summary := summarizeOpenCode(reader)
	result := summary.HarnessSummary
	result.FinalMessage = summary.lastText
	result.TurnCompleted = summary.sawEvidence && !result.TurnFailed
	if summary.tokensSeen {
		result.Tokens = &Tokens{
			Input:      intPointer(summary.input),
			Output:     intPointer(summary.output),
			CacheRead:  intPointer(summary.cacheRead),
			CacheWrite: intPointer(summary.cacheWrite),
			Reasoning:  intPointer(summary.reasoning),
		}
	}
	if summary.costSeen {
		if summary.costTotal == 0 && summary.steps > 0 && summary.output > 0 {
			result.CostNote = "harness reported zero cost with non-zero tokens (subscription plan); no per-run price was observed"
		} else {
			cost := summary.costTotal
			result.CostUSD = &cost
		}
	}
	return result
}

func summarizeOpenCode(reader io.Reader) openCodeSummary {
	var summary openCodeSummary
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), harnessLogLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event openCodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			summary.MalformedLines++
			continue
		}
		switch event.Type {
		case "text":
			if event.Part != nil && strings.TrimSpace(event.Part.Text) != "" {
				summary.lastText = event.Part.Text
				summary.sawEvidence = true
			}
		case "step_finish":
			summary.sawEvidence = true
			summary.steps++
			if event.Part != nil {
				if event.Part.Cost != nil {
					summary.costTotal += *event.Part.Cost
					summary.costSeen = true
				}
				if tokens := event.Part.Tokens; tokens != nil {
					summary.tokensSeen = true
					summary.input += tokens.Input
					summary.output += tokens.Output
					summary.reasoning += tokens.Reasoning
					summary.cacheRead += tokens.Cache.Read
					summary.cacheWrite += tokens.Cache.Write
				}
			}
		case "tool_use":
			if event.Part != nil && event.Part.State != nil && event.Part.State.Status == "completed" {
				summary.ToolCalls++
			}
		case "error":
			summary.TurnFailed = true
			if event.Error != nil {
				message := strings.TrimSpace(event.Error.Message)
				if event.Error.Type != "" {
					message = event.Error.Type + ": " + message
				}
				if message != "" {
					summary.Diagnostics = append(summary.Diagnostics, message)
				}
			}
		}
	}
	return summary
}

func (openCodeHarness) Actions(reader io.Reader, limit int) []string {
	if limit <= 0 {
		return nil
	}
	var actions []string
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), harnessLogLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event openCodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		var action string
		switch event.Type {
		case "tool_use":
			if event.Part == nil || event.Part.State == nil {
				continue
			}
			switch event.Part.Tool {
			case "shell", "bash":
				command := ""
				if event.Part.State.Input != nil {
					command = event.Part.State.Input.Command
				}
				action = "ran: " + command
			default:
				action = "called tool " + event.Part.Tool
			}
		case "error":
			if event.Error != nil {
				action = "error: " + event.Error.Type + ": " + event.Error.Message
			}
		default:
			continue
		}
		actions = append(actions, condense(action))
		if len(actions) > limit {
			actions = actions[1:]
		}
	}
	return actions
}
