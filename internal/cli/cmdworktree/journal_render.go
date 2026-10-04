package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
)

func writeJournalResult(out io.Writer, format string, result worktrees.LogVerbResult) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	_, err := fmt.Fprintf(out, "%s %s applied=%t", result.Verb, result.Worktree, result.Applied)
	if err != nil {
		return err
	}
	if result.Prompt != "" {
		if _, err := fmt.Fprintf(out, " prompt=%s", result.Prompt); err != nil {
			return err
		}
	}
	if result.Event != nil {
		if _, err := fmt.Fprintf(out, " event=%s#%d", result.Event.Type, result.Event.Seq); err != nil {
			return err
		}
	}
	if result.Offline {
		if _, err := fmt.Fprintf(out, " offline outbox=%d", result.Outbox); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	for _, note := range result.Notes {
		if _, err := fmt.Fprintf(out, "- %s\n", note); err != nil {
			return err
		}
	}
	for _, line := range result.Diagnosis {
		if _, err := fmt.Fprintf(out, "diagnosis: %s\n", line); err != nil {
			return err
		}
	}
	return nil
}
