package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CreatedWorkspace identifies the fresh workspace and its initial owned pane.
// The wire contract is Herdr TUI 0.9.1, not the separate herdr-core hc API.
type CreatedWorkspace struct {
	WorkspaceID string
	Pane        Pane
}

// WorkspaceCreate creates one unfocused workspace. A malformed reply is an
// error, never authority to close a guessed workspace or reuse an existing one.
func (c *Client) WorkspaceCreate(ctx context.Context, cwd, label string) (CreatedWorkspace, error) {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || unsafeCommandText(cwd) || strings.TrimSpace(label) == "" || len(label) > 128 || unsafeCommandText(label) {
		return CreatedWorkspace{}, fmt.Errorf("herdr workspace creation requires a clean absolute directory and bounded label")
	}
	raw, err := c.call(ctx, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
	if err != nil {
		return CreatedWorkspace{}, err
	}
	var body struct {
		Type      string `json:"type"`
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		Pane rawPaneOrAgent `json:"root_pane"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Type != "workspace_created" || body.Workspace.ID == "" || !body.Pane.valid() {
		return CreatedWorkspace{}, fmt.Errorf("%w: workspace creation reply lacks exact workspace and pane identity", ErrUnparseableOutput)
	}
	pane := body.Pane.toPane()
	if pane.WorkspaceID != body.Workspace.ID {
		return CreatedWorkspace{}, fmt.Errorf("%w: created pane belongs to a different workspace", ErrUnparseableOutput)
	}
	return CreatedWorkspace{WorkspaceID: body.Workspace.ID, Pane: pane}, nil
}

// ForegroundProcess is bounded process evidence reported by the pane API.
type ForegroundProcess struct {
	PID  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

type PaneProcesses struct {
	PaneID     string              `json:"pane_id"`
	ShellPID   int                 `json:"shell_pid"`
	Foreground []ForegroundProcess `json:"foreground_processes"`
}

// PaneProcessInfo returns exact pane-scoped process evidence; callers must
// corroborate their own expected process identity rather than pick any PID.
func (c *Client) PaneProcessInfo(ctx context.Context, paneID string) (PaneProcesses, error) {
	if err := ValidateTarget("pane id", paneID); err != nil {
		return PaneProcesses{}, err
	}
	raw, err := c.call(ctx, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return PaneProcesses{}, err
	}
	var body struct {
		Type string        `json:"type"`
		Info PaneProcesses `json:"process_info"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Type != "pane_process_info" || body.Info.PaneID != paneID || body.Info.ShellPID < 0 || len(body.Info.Foreground) > 128 {
		return PaneProcesses{}, fmt.Errorf("%w: process reply contradicts exact pane identity", ErrUnparseableOutput)
	}
	seen := make(map[int]bool, len(body.Info.Foreground))
	for _, process := range body.Info.Foreground {
		if process.PID <= 0 || process.Name == "" || seen[process.PID] || len(process.Argv) > 256 {
			return PaneProcesses{}, fmt.Errorf("%w: invalid or duplicate foreground process", ErrUnparseableOutput)
		}
		seen[process.PID] = true
	}
	return body.Info, nil
}

// PaneExec runs a fixed executable in a newly owned pane. Herdr's pane run is
// a shell-text operation, not exec-argv: every argument is separately quoted,
// control lines are rejected, and exec preserves the private wrapper's PID.
// The caller must first establish exclusive ownership of the fresh pane.
func (c *Client) PaneExec(ctx context.Context, paneID, executable string, arguments []string) error {
	if err := ValidateTarget("pane id", paneID); err != nil {
		return err
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || unsafeCommandText(executable) || len(arguments) > 256 {
		return fmt.Errorf("herdr pane execution requires a fixed absolute executable")
	}
	var command strings.Builder
	command.WriteString("exec")
	for _, argument := range append([]string{executable}, arguments...) {
		if unsafeCommandText(argument) {
			return fmt.Errorf("herdr pane execution refuses control characters")
		}
		// Check the quoted size before allocating replacement strings or
		// appending. Every single quote expands from one byte to five.
		if len(argument) > 128<<10 || len(argument)+4*strings.Count(argument, "'")+3 > (128<<10)-command.Len() {
			return fmt.Errorf("herdr pane command exceeds its bound")
		}
		command.WriteByte(' ')
		command.WriteString(shellArgument(argument))
	}
	return c.callEmptyAck(ctx, "pane", "run", paneID, command.String())
}

func unsafeCommandText(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Cf, r)
	}) >= 0
}

func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
