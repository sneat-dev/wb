package agents

import (
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
	scanHarnessEvents(reader, func(event harnessEvent) {
		if event.Type != "item.completed" || event.Item == nil {
			return
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
			return
		}
		actions = append(actions, condense(action))
		if len(actions) > limit {
			actions = actions[1:]
		}
	})
	return actions
}

func condense(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxActionDetail {
		return value[:maxActionDetail] + "…"
	}
	return value
}
