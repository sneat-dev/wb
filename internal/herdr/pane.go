package herdr

import (
	"context"
	"encoding/json"
	"fmt"
)

// PaneCurrent reports the pane hosting the calling process, via
// `herdr pane current --current`. It refuses with [ErrCurrentUnavailable]
// on a Client configured with [WithSocketPath] or [WithSessionName]:
// "current" resolves from the calling process's own ambient HERDR_PANE_ID
// (confirmed live, 2026-09-19), which names a pane on whichever server the
// caller's own environment happens to point at — not necessarily the one
// this Client was explicitly told to target, where the same ID could name
// a different pane entirely. Callers with an explicit target use
// [Client.PaneGet] instead.
func (c *Client) PaneCurrent(ctx context.Context) (Pane, error) {
	if c.targeted() {
		return Pane{}, fmt.Errorf("%w: use PaneGet with a known pane id instead", ErrCurrentUnavailable)
	}
	result, err := c.call(ctx, "pane", "current", "--current")
	if err != nil {
		return Pane{}, err
	}
	var body struct {
		Pane rawPaneOrAgent `json:"pane"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || !body.Pane.valid() {
		return Pane{}, fmt.Errorf("%w: pane current: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	return body.Pane.toPane(), nil
}

// paneListResultType is the "type" discriminator herdr's own success
// envelope carries for `pane list` (herdr api schema --json,
// $defs.ResponseResult: {"type":"pane_list","panes":[...]}).
const paneListResultType = "pane_list"

// PaneList lists panes, via `herdr pane list`. An empty workspaceID omits
// the --workspace filter and lists every pane the current session can see.
// It requires the result's own "type" discriminator to read "pane_list"
// and every entry to be [rawPaneOrAgent.valid] before returning anything,
// so a differently-shaped or partially-populated response is reported as
// [ErrUnparseableOutput] rather than a silently incomplete or wrong list.
func (c *Client) PaneList(ctx context.Context, workspaceID string) ([]Pane, error) {
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	result, err := c.call(ctx, args...)
	if err != nil {
		return nil, err
	}
	var body struct {
		Type  string           `json:"type"`
		Panes []rawPaneOrAgent `json:"panes"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || body.Type != paneListResultType {
		return nil, fmt.Errorf("%w: pane list: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	panes := make([]Pane, 0, len(body.Panes))
	for _, raw := range body.Panes {
		if !raw.valid() {
			return nil, fmt.Errorf("%w: pane list: an entry is missing its pane id: %s",
				ErrUnparseableOutput, truncate(string(result), 256))
		}
		panes = append(panes, raw.toPane())
	}
	return panes, nil
}

// PaneGet reports one pane by ID, via `herdr pane get <pane_id>`.
func (c *Client) PaneGet(ctx context.Context, paneID string) (Pane, error) {
	if paneID == "" {
		return Pane{}, fmt.Errorf("%w: pane id is empty", ErrUnknownTarget)
	}
	result, err := c.call(ctx, "pane", "get", paneID)
	if err != nil {
		return Pane{}, err
	}
	var body struct {
		Pane rawPaneOrAgent `json:"pane"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || !body.Pane.valid() {
		return Pane{}, fmt.Errorf("%w: pane get: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	return body.Pane.toPane(), nil
}

// PaneSendText sends literal text to a pane without any key encoding, via
// `herdr pane send-text <pane_id> <text>` — herdr's own group help
// distinguishes this from `send-keys`, which sends key presses, not text.
// It is validated exactly like [Client.AgentPrompt]'s text, via
// [ValidatePromptText]: no newline, carriage return, or other control
// character, so a caller can never smuggle extra lines into a live pane
// through this call either.
func (c *Client) PaneSendText(ctx context.Context, paneID, text string) error {
	if paneID == "" {
		return fmt.Errorf("%w: pane id is empty", ErrUnknownTarget)
	}
	if err := ValidatePromptText(text); err != nil {
		return err
	}
	_, err := c.call(ctx, "pane", "send-text", paneID, text)
	return err
}

// describeParseFailure renders either the json.Unmarshal error or, when
// unmarshalling itself succeeded but produced a value this package does
// not recognize (valid() is false), the raw payload — bounded, so an
// unexpectedly large or malformed body cannot make an error unbounded.
func describeParseFailure(unmarshalErr error, raw json.RawMessage) string {
	if unmarshalErr != nil {
		return unmarshalErr.Error()
	}
	return fmt.Sprintf("result did not match the expected shape: %s", truncate(string(raw), 256))
}
