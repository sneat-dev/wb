package agentguard

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ToolCall is the part of a Claude Code PreToolUse payload this guard reads.
//
// Every field is optional on purpose. The payload schema belongs to Claude
// Code, not to WB, and a WB that refuses a payload it does not fully recognise
// would block the whole fleet the first time a field is added. Unknown fields
// are ignored and missing fields resolve to an allow.
type ToolCall struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	CWD           string          `json:"cwd"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// toolInput holds the tool-specific keys the guard understands. Claude Code
// documents `command` for Bash and `file_path` for Write and Edit;
// `notebook_path` covers NotebookEdit.
type toolInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
}

// Decision is the guard's answer for one tool call.
type Decision struct {
	// Deny is false for every allow, including every unknown.
	Deny bool
	// Reason is the message shown to the agent, empty unless Deny.
	Reason string
}

// Options configures Inspect.
type Options struct {
	// ProjectsRoot is the directory holding {owner}/{repository} canonical
	// clones.
	ProjectsRoot string
}

// Inspect judges one tool call.
//
// It never returns an error and never panics: a recovered panic is an allow,
// because a guard that runs before every tool call of every agent must not be
// able to take the machine down with it.
func Inspect(call ToolCall, options Options) (decision Decision) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decision = Decision{}
		}
	}()
	if options.ProjectsRoot == "" {
		return Decision{}
	}
	if call.HookEventName != "" && call.HookEventName != "PreToolUse" {
		return Decision{}
	}
	var input toolInput
	if len(call.ToolInput) > 0 {
		// A tool_input that is not an object, or that holds an unexpected
		// shape, leaves every field empty and therefore allows.
		_ = json.Unmarshal(call.ToolInput, &input)
	}
	var result *finding
	switch {
	case call.ToolName == "Bash":
		result = inspectBash(input.Command, call.CWD, options.ProjectsRoot)
	case isFileWriteTool(call.ToolName):
		result = inspectFileTool(input, options.ProjectsRoot)
	}
	if result == nil {
		return Decision{}
	}
	return Decision{Deny: true, Reason: refusal(*result)}
}

// isFileWriteTool names the tools that write the file they point at.
//
// The list is an allowlist rather than a denylist because Read, NotebookRead,
// and several MCP tools carry a `file_path` too, and judging by the presence
// of that key alone would refuse reads of a canonical clone — which are not
// only legitimate but the main reason the clone exists. The two suffix checks
// admit tools added later that follow the same naming convention.
func isFileWriteTool(name string) bool {
	switch name {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return true
	case "Read", "NotebookRead":
		return false
	}
	return strings.Contains(name, "Edit") || strings.Contains(name, "Write")
}

// inspectFileTool covers every tool that names the file it writes: Write,
// Edit, NotebookEdit, and any future tool that follows the same convention.
func inspectFileTool(input toolInput, projectsRoot string) *finding {
	for _, path := range []string{input.FilePath, input.NotebookPath} {
		if path == "" {
			continue
		}
		absolute, ok := absolutePath(path)
		if !ok {
			continue
		}
		location := Classify(projectsRoot, absolute)
		if location.Kind == KindCanonical {
			return &finding{Location: location, Detail: "writing " + absolute}
		}
	}
	return nil
}

// refusal writes the message the agent reads. It has to carry the remedy, not
// just the rule: a refusal an agent cannot act on becomes a refusal it works
// around.
func refusal(result finding) string {
	slug := result.Location.Slug()
	if slug == "" {
		slug = "<owner/repository>"
	}
	var message strings.Builder
	fmt.Fprintf(&message, "%s is a canonical clone and must stay clean.\n", result.Location.Root)
	fmt.Fprintf(&message, "Refused: %s.\n\n", result.Detail)
	message.WriteString("Every linked worktree in the fleet is cut from this clone, so uncommitted\n")
	message.WriteString("work left here is invisible to WB and one routine checkout away from being\n")
	message.WriteString("destroyed.\n\n")
	fmt.Fprintf(&message, "Run: wb worktree create <task> %s\n", slug)
	message.WriteString("Then work in the printed worktree path.\n\n")
	message.WriteString("If this clone already holds uncommitted work, rescue it first:\n")
	fmt.Fprintf(&message, "  wb worktree rescue %s\n", result.Location.Root)
	return message.String()
}

// hookResponse is the PreToolUse response document.
//
// Only a deny is ever written. An explicit "allow" would suppress the
// permission prompt the user would otherwise see, turning a guard meant to add
// a check into one that removes one, so an allow is silence.
type hookResponse struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// WriteDecision emits the response Claude Code reads, and reports whether
// anything was written.
//
// The decision travels as JSON on stdout with a zero exit status, never as
// exit code 2. Exit code 2 is Claude Code's other blocking channel, and WB
// already uses exit 2 for a usage error — so a WB too old to know this
// subcommand, or any mistyped invocation, would exit 2 and block every tool
// call on the machine with cobra's usage text as the reason. Carrying the
// decision in the document instead makes "WB said nothing" mean "allow",
// which is the only safe default for a guard on this path.
func WriteDecision(out io.Writer, decision Decision) (bool, error) {
	if !decision.Deny {
		return false, nil
	}
	response := hookResponse{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "deny",
		PermissionDecisionReason: decision.Reason,
	}}
	encoded, err := json.Marshal(response)
	if err != nil {
		return false, err
	}
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

// DecodeToolCall reads a PreToolUse payload. A payload that is not valid JSON,
// or not an object, yields an empty ToolCall — which Inspect allows.
func DecodeToolCall(reader io.Reader) ToolCall {
	var call ToolCall
	limited := io.LimitReader(reader, maxPayloadBytes)
	if err := json.NewDecoder(limited).Decode(&call); err != nil {
		return ToolCall{}
	}
	return call
}

// maxPayloadBytes caps how much of a payload is read. A Write tool call
// carries the whole file content, and the guard has no use for it beyond the
// path, so a very large body must not turn this into a memory or latency
// problem. A truncated payload fails to parse and therefore allows.
const maxPayloadBytes = 16 << 20
