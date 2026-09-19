package herdr

import (
	"context"
	"encoding/json"
	"fmt"
)

// agentListResultType is the "type" discriminator herdr's own success
// envelope carries for `agent list` (herdr api schema --json,
// $defs.ResponseResult: {"type":"agent_list","agents":[...]}).
const agentListResultType = "agent_list"

// AgentList lists live agents with their status, pane, workspace and
// harness session identity where herdr exposes it, via `herdr agent list`.
// It requires the result's own "type" discriminator to read "agent_list"
// and every entry to be [rawPaneOrAgent.valid] before returning anything,
// so a differently-shaped or partially-populated response is reported as
// [ErrUnparseableOutput] rather than a silently incomplete or wrong list.
func (c *Client) AgentList(ctx context.Context) ([]Agent, error) {
	result, err := c.call(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var body struct {
		Type   string           `json:"type"`
		Agents []rawPaneOrAgent `json:"agents"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || body.Type != agentListResultType {
		return nil, fmt.Errorf("%w: agent list: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	agents := make([]Agent, 0, len(body.Agents))
	for _, raw := range body.Agents {
		if !raw.valid() {
			return nil, fmt.Errorf("%w: agent list: an entry is missing its pane id: %s",
				ErrUnparseableOutput, truncate(string(result), 256))
		}
		agents = append(agents, raw.toAgent())
	}
	return agents, nil
}

// AgentGet reports one agent by its unique live name or the pane ID
// currently hosting it, via `herdr agent get <target>`.
func (c *Client) AgentGet(ctx context.Context, target string) (Agent, error) {
	if target == "" {
		return Agent{}, fmt.Errorf("%w: agent target is empty", ErrUnknownTarget)
	}
	result, err := c.call(ctx, "agent", "get", target)
	if err != nil {
		return Agent{}, err
	}
	var body struct {
		Agent rawPaneOrAgent `json:"agent"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || !body.Agent.valid() {
		return Agent{}, fmt.Errorf("%w: agent get: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	return body.Agent.toAgent(), nil
}

// AgentRead returns the raw screen text herdr has recorded for target, via
// `herdr agent read <target> --source <source>`. herdr agent read is one
// of the commands safety.md forbids running against the founder's real
// herdr session (it reads the founder's screen), so this decodes the
// schema-verified [ScreenText] shape (see its doc comment) against
// fixtures only, defensively enough that an unexpected real shape becomes
// [ErrUnparseableOutput] rather than a wrong answer.
func (c *Client) AgentRead(ctx context.Context, target string, source ReadSource) (ScreenText, error) {
	if target == "" {
		return ScreenText{}, fmt.Errorf("%w: agent target is empty", ErrUnknownTarget)
	}
	result, err := c.call(ctx, "agent", "read", target, "--source", source.cliArg())
	if err != nil {
		return ScreenText{}, err
	}
	var body struct {
		Read rawScreenText `json:"read"`
	}
	if unmarshalErr := json.Unmarshal(result, &body); unmarshalErr != nil || !body.Read.valid() {
		return ScreenText{}, fmt.Errorf("%w: agent read: %s", ErrUnparseableOutput, describeParseFailure(unmarshalErr, result))
	}
	return body.Read.toScreenText(), nil
}

// AgentPrompt submits text to target as one line, via
// `herdr agent prompt <target> <text>`. It refuses text containing a
// newline or another control character (see [ValidatePromptText] and "the
// newline is the authority boundary" in doc.go) before ever invoking
// herdr, and never appends one itself: herdr's own submission behavior is
// exactly what makes this call binding on whoever reads target's pane,
// which is a decision for this package's caller, not for this method.
func (c *Client) AgentPrompt(ctx context.Context, target, text string) error {
	if target == "" {
		return fmt.Errorf("%w: agent target is empty", ErrUnknownTarget)
	}
	if err := ValidatePromptText(text); err != nil {
		return err
	}
	_, err := c.call(ctx, "agent", "prompt", target, text)
	return err
}

// AgentSendKeys sends one or more explicit key names to target without
// submitting them, via `herdr agent send-keys <target> <key> [key...]`.
// Every key must pass [ValidateKeyName]; this method never forwards
// arbitrary text as a "key".
func (c *Client) AgentSendKeys(ctx context.Context, target string, keys ...string) error {
	if target == "" {
		return fmt.Errorf("%w: agent target is empty", ErrUnknownTarget)
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: at least one key is required", ErrInvalidKeyName)
	}
	for _, key := range keys {
		if err := ValidateKeyName(key); err != nil {
			return err
		}
	}
	args := make([]string, 0, 3+len(keys))
	args = append(args, "agent", "send-keys", target)
	args = append(args, keys...)
	_, err := c.call(ctx, args...)
	return err
}

// AgentWait blocks until target reaches one of the requested states (or
// herdr's own settled-state default when until is empty), via
// `herdr agent wait <target> [--until STATUS]...`.
//
// herdr agent wait is one of the commands safety.md forbids running
// against the founder's real herdr session, so this package's success
// decoding is not verified against a live response; it is inferred from
// spec/ideas/daemon-as-coordinator.md's report that a verified wait
// "returned `agent_status: working`" — the same field agent get/list
// report — and decoded defensively enough that a different real shape
// becomes [ErrUnparseableOutput], never a panic or a silently wrong Agent.
func (c *Client) AgentWait(ctx context.Context, target string, until ...AgentStatus) (Agent, error) {
	if target == "" {
		return Agent{}, fmt.Errorf("%w: agent target is empty", ErrUnknownTarget)
	}
	args := []string{"agent", "wait", target}
	for _, status := range until {
		args = append(args, "--until", string(status))
	}
	result, err := c.call(ctx, args...)
	if err != nil {
		return Agent{}, err
	}
	var wrapped struct {
		Agent rawPaneOrAgent `json:"agent"`
	}
	if unmarshalErr := json.Unmarshal(result, &wrapped); unmarshalErr == nil && wrapped.Agent.valid() {
		return wrapped.Agent.toAgent(), nil
	}
	var bare rawPaneOrAgent
	if unmarshalErr := json.Unmarshal(result, &bare); unmarshalErr == nil && bare.valid() {
		return bare.toAgent(), nil
	}
	return Agent{}, fmt.Errorf("%w: agent wait: result matched neither known shape: %s",
		ErrUnparseableOutput, truncate(string(result), 256))
}
