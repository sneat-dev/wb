package herdr

import "strings"

// ReadSource selects which buffer `herdr agent read` / `herdr pane read`
// reads from. Values here are the JSON wire spelling, as herdr's own
// socket API schema (`herdr api schema --json`, read-only) declares them
// for ReadSource: snake_case, unlike the CLI flag's kebab-case spelling
// (`--source recent-unwrapped`, from `herdr pane`/`herdr agent`'s bare
// group help). [ReadSource.cliArg] converts between the two; this package
// found no other place either spelling needs converting.
type ReadSource string

const (
	ReadSourceVisible         ReadSource = "visible"
	ReadSourceRecent          ReadSource = "recent"
	ReadSourceRecentUnwrapped ReadSource = "recent_unwrapped"
	ReadSourceDetection       ReadSource = "detection"
)

// cliArg renders s as the CLI expects it on --source.
func (s ReadSource) cliArg() string {
	return strings.ReplaceAll(string(s), "_", "-")
}

// ReadFormat selects `herdr agent read` / `herdr pane read`'s output
// encoding. Unlike [ReadSource], the CLI flag and the JSON wire value use
// the same spelling.
type ReadFormat string

const (
	ReadFormatText ReadFormat = "text"
	ReadFormatANSI ReadFormat = "ansi"
)

// ScreenText is one screen-text read result, as herdr's PaneReadResult
// reports it (`herdr api schema --json`, $defs.PaneReadResult, read-only).
// `agent.read`'s own request/response schema declares no distinct result
// type of its own, and PaneReadResult ("pane_read") is the only
// screen-text shape in herdr's closed success-response union, so
// [Client.AgentRead] decodes into the same struct `herdr pane read` would.
// This was verified by reading the schema, not by invoking `agent read`
// against the founder's real herdr session, which safety.md forbids;
// treat the field set, not the sample values in testdata/agent_read.json,
// as the verified part.
type ScreenText struct {
	PaneID      string
	WorkspaceID string
	TabID       string
	Source      ReadSource
	Format      ReadFormat
	Text        string
	Revision    uint64
	Truncated   bool
}

// rawScreenText mirrors PaneReadResult's JSON field names exactly.
type rawScreenText struct {
	PaneID      string     `json:"pane_id"`
	WorkspaceID string     `json:"workspace_id"`
	TabID       string     `json:"tab_id"`
	Source      ReadSource `json:"source"`
	Format      ReadFormat `json:"format"`
	Text        string     `json:"text"`
	Revision    uint64     `json:"revision"`
	Truncated   bool       `json:"truncated"`
}

func (raw rawScreenText) valid() bool {
	return raw.PaneID != ""
}

// toScreenText converts raw to [ScreenText]. The two structs share an
// identical field sequence (only JSON tags differ), so this is a plain
// type conversion rather than a field-by-field copy.
func (raw rawScreenText) toScreenText() ScreenText {
	return ScreenText(raw)
}
