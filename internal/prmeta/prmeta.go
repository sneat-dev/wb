package prmeta

import (
	"encoding/json"
	"strings"
)

const marker = "<!-- wb:provenance:v1 "

// Provenance adds stable WB coordination identity that GitHub does not already
// carry. The source branch remains GitHub-owned PR metadata; local worktree
// paths are deliberately excluded because they can differ between machines.
type Provenance struct {
	Effort string `json:"effort"`
	Stream string `json:"stream,omitempty"`
}

func Append(body string, provenance Provenance) string {
	provenance.Effort = strings.TrimSpace(provenance.Effort)
	provenance.Stream = strings.TrimSpace(provenance.Stream)
	if provenance.Effort == "" || strings.Contains(body, marker) {
		return body
	}
	encoded, err := json.Marshal(provenance)
	if err != nil {
		return body
	}
	lines := []string{"WB effort: `" + provenance.Effort + "`"}
	if provenance.Stream != "" {
		lines = append(lines, "WB stream: `"+provenance.Stream+"`")
	}
	lines = append(lines, marker+string(encoded)+" -->")
	return strings.TrimRight(body, "\n") + "\n\n" + strings.Join(lines, "\n") + "\n"
}
