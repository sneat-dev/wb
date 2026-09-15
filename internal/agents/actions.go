package agents

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// maxActionDetail bounds one condensed action line so a worker command that
// embeds a whole file cannot smuggle a transcript through the summary.
const maxActionDetail = 240

// RecentActions condenses the harness event stream into the last few things the
// worker actually did. It exists so `wb agent logs` can show a human something
// useful without printing an entire run into a caller's context.
func RecentActions(reader io.Reader, limit int) []string {
	if limit <= 0 {
		return nil
	}
	actions := make([]string, 0, limit)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item *struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type != "item.completed" || event.Item == nil {
			continue
		}
		var action string
		switch event.Item.Type {
		case "command_execution":
			action = "ran: " + event.Item.Command
		case "file_change":
			action = "changed files"
		case "mcp_tool_call":
			action = "called a tool"
		case "error":
			action = "diagnostic: " + event.Item.Message
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

func condense(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxActionDetail {
		return value[:maxActionDetail] + "…"
	}
	return value
}
