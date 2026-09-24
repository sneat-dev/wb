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
	// AgentID identifies the subagent that issued this call. Claude Code
	// sends it only from a subagent, never from the main thread (wb#637), so
	// its mere presence is the "is this a subagent?" signal the rewrite's
	// WB_SUBAGENT_ID stamp relies on, and it feeds the provenance fields
	// wb#631 writes into every WB record.
	AgentID string `json:"agent_id"`
	// ToolUseID identifies this exact tool call.
	ToolUseID string `json:"tool_use_id"`
}

// toolInput holds the tool-specific keys the guard understands. Claude Code
// documents `command` for Bash and `file_path` for Write and Edit;
// `notebook_path` covers NotebookEdit; `model` and `prompt` cover the `Agent`
// (subagent dispatch) tool, and `run_in_background` covers Bash.
type toolInput struct {
	Command         string `json:"command"`
	FilePath        string `json:"file_path"`
	NotebookPath    string `json:"notebook_path"`
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	RunInBackground bool   `json:"run_in_background"`
}

// Decision is the guard's answer for one tool call.
type Decision struct {
	// Deny is false for every allow, including every unknown.
	Deny bool
	// Reason is the message shown to the agent, empty unless Deny.
	Reason string
	// RewriteCommand, when non-empty, is the Bash command Claude Code should
	// run instead of the one it proposed — either a narrow "simple command"
	// shape of heavy validation rewritten into `wb run --` (wb#637, founder
	// decision 2026-09-18: rewrite, not refuse, inside a managed worktree; see
	// simpleGovernedRewrite for exactly which shapes qualify), a
	// WB_SUBAGENT_ID/WB_SUBAGENT_TOOL_USE_ID export prefixed onto a call that
	// is, on its own, a simple wb invocation, or both together. Every other
	// tool_input field the caller sent is carried through unchanged; see
	// WriteDecision.
	RewriteCommand string
}

// Options configures Inspect.
type Options struct {
	// ProjectsRoot is the directory holding {owner}/{repository} canonical
	// clones.
	ProjectsRoot string
	// WBExecutable is the absolute path the agent-hook command was
	// registered with, when known. A rewrite uses it in place of the bare
	// "wb" name so it runs the exact binary the hook itself is, rather than
	// whatever "wb" resolves to on the child process's PATH (wb#645 review
	// minor m5). Empty falls back to "wb".
	WBExecutable string
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
	if call.ToolName == "Bash" {
		return inspectBashCall(call, input, options)
	}
	var result *finding
	switch {
	case call.ToolName == "Agent" || call.ToolName == "Task":
		// "Task" is the harness's earlier/alternate name for the same
		// subagent-dispatch tool; both are judged identically.
		result = inspectAgentDispatch(input, call.CWD, options.ProjectsRoot)
	case isFileWriteTool(call.ToolName):
		result = inspectFileTool(input, options.ProjectsRoot)
	}
	if result == nil {
		return Decision{}
	}
	return Decision{Deny: true, Reason: refusal(*result)}
}

// inspectBashCall judges one Bash call, then applies wb#637's narrow rewrite
// and subagent-ID stamp on top of an allow.
//
// Deny wins (wb#645 review Blocker 2, founder decision: fix 3): every WB deny
// policy across the whole line is evaluated first, ignoring any
// governed-validation match, before a rewrite is even considered. This is
// what refuses `go test ./... && gh pr merge 5 --merge` and
// `go vet ./... ; rm -rf <canonical clone>/README.md` outright, exactly as
// base does, instead of letting the governed-validation match hand the whole
// line — deny included — to a rewrite that then carries no permission
// prompt.
//
// Once that pass finds nothing, a governed-validation finding is rewritten
// only when the whole command matches the narrow "simple command" shape
// simpleGovernedRewrite recognises — a single governed command, optionally
// prefixed by `VAR=value` assignments and/or a leading `cd <dir> &&`.
// Anything else falls back to the pre-PR refusal, unchanged: there is no
// `sh -c` wrapping anywhere any more (wb#645 review Blockers 1, 4, Major 1).
func inspectBashCall(call ToolCall, input toolInput, options Options) Decision {
	command := input.Command

	if result := inspectBashDenyOnly(command, call.CWD, options.ProjectsRoot); result != nil {
		return Decision{Deny: true, Reason: refusal(*result)}
	}

	rewritten := command
	changed := false
	if result := inspectBash(command, call.CWD, options.ProjectsRoot); result != nil {
		if len(result.GovernedCommand) == 0 {
			return Decision{Deny: true, Reason: refusal(*result)}
		}
		simple, ok := simpleGovernedRewrite(command, options.WBExecutable)
		if !ok {
			return Decision{Deny: true, Reason: refusal(*result)}
		}
		rewritten = simple
		changed = true
	}
	if stamped := stampSubagentID(rewritten, call.AgentID, call.ToolUseID); stamped != rewritten {
		rewritten = stamped
		changed = true
	}
	if !changed {
		return Decision{}
	}
	return Decision{RewriteCommand: rewritten}
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
//
// A finding carrying GovernedCommand reaches here whenever inspectBashCall
// could not rewrite it into `wb run --` — either a real deny elsewhere on the
// line outranked the rewrite (wb#645 review Blocker 2, "deny wins"), or the
// command did not match the narrow "simple command" shape
// simpleGovernedRewrite requires (wb#645 review fix 2). Either way this
// renders the pre-PR governed-command refusal, naming the wrapped command to
// run instead.
func refusal(result finding) string {
	// Message is set by policies that are not about a canonical-clone write —
	// missing-model dispatch, hook bypass, auto-tagging, a literal report
	// path, a claimed repository — and carries its own complete, self-quoting
	// text. Those policies never set Location/Detail in the shape the
	// canonical-clone wording below assumes.
	if result.Message != "" {
		return result.Message
	}
	if len(result.GovernedCommand) > 0 {
		return governedCommandRefusal(result)
	}
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

// governedCommandRefusal is the pre-PR refusal for a governed-validation
// command this call cannot rewrite — either a real deny elsewhere on the line
// outranked the rewrite, or the command did not match the narrow "simple
// command" shape simpleGovernedRewrite requires (wb#645 review). It names the
// exact command the agent should submit through `wb run --` itself.
func governedCommandRefusal(result finding) string {
	var message strings.Builder
	message.WriteString("CPU-heavy validation in a managed worktree must run through WB.\n")
	fmt.Fprintf(&message, "Refused: %s.\n\n", result.Detail)
	message.WriteString("This gives the operation a durable ID and timing receipt, and lets WB queue\n")
	message.WriteString("or coalesce equivalent work when several agents share a constrained machine.\n")
	message.WriteString("Run: wb run --")
	for _, word := range result.GovernedCommand {
		message.WriteByte(' ')
		message.WriteString(shellQuote(word))
	}
	message.WriteByte('\n')
	message.WriteString("Run gofmt or Prettier directly after edits; they are intentionally not queued.\n")
	return message.String()
}

// shellQuote quotes value only when it needs it, for a refusal message's
// individual, already-split words. See simpleGovernedRewrite in bash.go for
// the rewrite path's own quoting, which uses this too: both need a value to
// survive as one shell word without disturbing what the caller wrote when it
// already needs no quoting at all.
func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-", character)
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// hookResponse is the PreToolUse response document.
//
// A deny is the historical shape: an explicit "allow" would ordinarily
// suppress the permission prompt the user would otherwise see, turning a
// guard meant to add a check into one that removes one, so silence has always
// been how this guard allows a call. A rewrite (wb#637) is the other allow
// shape, but it deliberately never sets PermissionDecision either: it emits
// only UpdatedInput. Claude Code v2.1.276 applies a response carrying
// UpdatedInput with no PermissionDecision as the new input and then runs the
// normal permission flow on it, so the user's own prompts and allow, deny and
// ask rules still apply to the rewritten command exactly as they would to the
// original (wb#645 review: an explicit "allow" here was found to suppress the
// permission prompt entirely, which is not this guard's call to make). This
// relies on Claude Code >= 2.1.276's behaviour for a response that sets
// UpdatedInput without PermissionDecision; it is undocumented, and
// TestRewriteAndStampNeverSetPermissionDecision pins it.
type hookResponse struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

// WriteDecision emits the response Claude Code reads for one tool call, and
// reports whether anything was written.
//
// The decision travels as JSON on stdout with a zero exit status, never as
// exit code 2. Exit code 2 is Claude Code's other blocking channel, and WB
// already uses exit 2 for a usage error — so a WB too old to know this
// subcommand, or any mistyped invocation, would exit 2 and block every tool
// call on the machine with cobra's usage text as the reason. Carrying the
// decision in the document instead makes "WB said nothing" mean "allow",
// which is the only safe default for a guard on this path.
//
// toolInput is the call's own original tool_input, verbatim JSON. A rewrite
// merges decision.RewriteCommand into it as "command" and carries every other
// field — description, timeout, run_in_background, and anything this guard
// does not otherwise know about — through unchanged (wb#637). It never sets
// PermissionDecision (see hookResponse's own doc): the rewritten command
// still goes through the user's normal permission flow.
func WriteDecision(out io.Writer, decision Decision, toolInput json.RawMessage) (bool, error) {
	var output hookSpecificOutput
	switch {
	case decision.Deny:
		output = hookSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: decision.Reason,
		}
	case decision.RewriteCommand != "":
		output = hookSpecificOutput{
			HookEventName: "PreToolUse",
			UpdatedInput:  mergeUpdatedCommand(toolInput, decision.RewriteCommand),
		}
	default:
		return false, nil
	}
	encoded, err := json.Marshal(hookResponse{HookSpecificOutput: output})
	if err != nil {
		return false, err
	}
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

// mergeUpdatedCommand returns original with its "command" key replaced by
// command, decoding original as a generic object so every field this guard
// does not itself model still survives the round trip — the same reasoning
// mergeAgentHookSettings documents for the settings file this guard is
// registered from. A non-object or malformed original degrades to an object
// holding only "command": a rewrite that drops an unknown field is still far
// safer than one that fails to write at all.
func mergeUpdatedCommand(original json.RawMessage, command string) json.RawMessage {
	fields := map[string]any{}
	if len(original) > 0 {
		_ = json.Unmarshal(original, &fields)
	}
	fields["command"] = command
	if encoded, err := json.Marshal(fields); err == nil {
		return encoded
	}
	encoded, _ := json.Marshal(map[string]string{"command": command})
	return encoded
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
