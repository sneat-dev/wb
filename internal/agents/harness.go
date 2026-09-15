package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// HarnessOptions is everything the Codex harness needs that is not in the
// process environment. It exists so every harness-specific flag and
// configuration-key spelling lives in exactly one file: a harness version
// change is a one-file change.
type HarnessOptions struct {
	WorktreeDir string
	Model       string
	Reasoning   string
	// ProviderName keys the provider registry entry, and Provider carries the
	// routing. The credential itself is never here — only the environment
	// variable name the harness must read it from.
	ProviderName string
	Provider     Provider
	// LastMessagePath asks the harness to write its final message there, which
	// is the one bounded, non-transcript result channel.
	LastMessagePath string
}

// CodexArgv builds the exact non-interactive Codex invocation.
//
// The task is deliberately absent: it is delivered on stdin (a trailing "-"),
// so task text never appears in the process table. Values are JSON-encoded
// because the harness parses each override value as TOML, where a bare word
// and a quoted string are different things.
func CodexArgv(options HarnessOptions) ([]string, error) {
	if strings.TrimSpace(options.WorktreeDir) == "" {
		return nil, fmt.Errorf("codex launch requires a working directory")
	}
	if strings.TrimSpace(options.Model) == "" {
		return nil, fmt.Errorf("codex launch requires a model")
	}
	if options.Provider.WireAPI == "" || options.Provider.CredentialEnv == "" || options.Provider.BaseURL == "" {
		return nil, fmt.Errorf("codex launch requires a fully resolved provider")
	}
	prefix := "model_providers." + options.ProviderName
	overrides := []struct {
		key   string
		value any
	}{
		{"model_provider", options.ProviderName},
		{prefix + ".name", options.ProviderName},
		{prefix + ".base_url", options.Provider.BaseURL},
		{prefix + ".wire_api", options.Provider.WireAPI},
		{prefix + ".env_key", options.Provider.CredentialEnv},
		// A dispatched worker must never block on an interactive approval
		// prompt: nothing is attached to answer it.
		{"approval_policy", "never"},
		// The harness's own shell tool subprocesses must not inherit the
		// credential the harness authenticates with, so a worker command
		// cannot read the key that pays for the run.
		{"shell_environment_policy.inherit", "core"},
	}
	argv := []string{
		"exec", "--ephemeral", "--json", "--ignore-user-config",
		"-C", options.WorktreeDir,
		"-s", "workspace-write",
		"-m", options.Model,
	}
	for _, override := range overrides {
		encoded, err := encodeConfigValue(override.value)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "-c", override.key+"="+encoded)
	}
	exclude, err := encodeConfigValue([]string{"*KEY*", "*TOKEN*", "*SECRET*"})
	if err != nil {
		return nil, err
	}
	argv = append(argv, "-c", "shell_environment_policy.exclude="+exclude)
	// Reasoning is the model's own vocabulary and drifts per model, so WB
	// passes the configured value through and lets the model be the authority
	// on which levels exist. An absent value passes no override at all.
	if strings.TrimSpace(options.Reasoning) != "" {
		encoded, err := encodeConfigValue(strings.TrimSpace(options.Reasoning))
		if err != nil {
			return nil, err
		}
		argv = append(argv, "-c", "model_reasoning_effort="+encoded)
	}
	if strings.TrimSpace(options.LastMessagePath) != "" {
		argv = append(argv, "-o", options.LastMessagePath)
	}
	// The trailing "-" is the harness's documented "read the prompt from
	// stdin" spelling. Omitting it would make an empty prompt instead.
	return append(argv, "-"), nil
}

// HarnessSummary is what the run owner extracts from the harness event stream.
// It is execution metadata only; it deliberately carries nothing that judges
// whether the work is correct.
type HarnessSummary struct {
	TurnCompleted bool
	TurnFailed    bool
	Usage         *Usage
	ToolCalls     int
	Diagnostics   []string
	// MalformedLines counts lines that look like harness events but could not
	// be decoded. Ordinary non-JSON lines are the harness's own stderr sharing
	// the file and are expected; a malformed *event* is not, and must be
	// retained as a diagnosis rather than silently dropped.
	MalformedLines int
}

// harnessEvent is the subset of the harness's JSONL vocabulary WB consumes.
// One decoder serves every consumer, so a harness field rename is a
// single-place change.
type harnessEvent struct {
	Type string `json:"type"`
	Item *struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"item"`
	Usage *struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		CacheWriteInputTokens int `json:"cache_write_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
		ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

// harnessLogLimit bounds how much of a transcript is ever held in memory.
const harnessLogLimit = 8 * 1024 * 1024

// scanHarnessEvents reads the harness's merged stdout/stderr stream line by
// line. A line that is not a JSON object at all is the harness's own diagnostic
// text on stderr and is skipped; a line that begins like an event but does not
// decode is counted as malformed.
func scanHarnessEvents(reader io.Reader, handle func(harnessEvent)) int {
	malformed := 0
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), harnessLogLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event harnessEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			malformed++
			continue
		}
		handle(event)
	}
	return malformed
}

// SummarizeEvents parses the harness's JSONL event stream.
func SummarizeEvents(reader io.Reader) HarnessSummary {
	var summary HarnessSummary
	summary.MalformedLines = scanHarnessEvents(reader, func(event harnessEvent) {
		switch event.Type {
		case "turn.completed":
			summary.TurnCompleted = true
			if event.Usage != nil {
				summary.Usage = &Usage{
					InputTokens:           event.Usage.InputTokens,
					CachedInputTokens:     event.Usage.CachedInputTokens,
					CacheWriteInputTokens: event.Usage.CacheWriteInputTokens,
					OutputTokens:          event.Usage.OutputTokens,
					ReasoningOutputTokens: event.Usage.ReasoningOutputTokens,
				}
			}
		case "turn.failed", "error":
			summary.TurnFailed = true
		case "item.completed":
			if event.Item == nil {
				return
			}
			switch event.Item.Type {
			case "command_execution", "file_change", "mcp_tool_call":
				summary.ToolCalls++
			case "error":
				// The harness reports non-fatal degradations — a missing model
				// catalogue, a shortened skills budget — as error items. They
				// belong in the run record as diagnostics, not as failures.
				if message := strings.TrimSpace(event.Item.Message); message != "" {
					summary.Diagnostics = append(summary.Diagnostics, message)
				}
			}
		}
	})
	return summary
}

func encodeConfigValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode harness configuration value: %w", err)
	}
	return string(encoded), nil
}
