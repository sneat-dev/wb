// Package sessionview renders the concrete session-move result used by session
// and task commands. Operation packages do not depend on presentation.
package sessionview

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"io"
)

func Move(out io.Writer, format string, result sessionrun.MoveResult) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if result.Resume {
		_, err := fmt.Fprintf(out, "completed handoff %s to successor %s via %s in tmux %s; predecessor custody is sealed\n", result.Request.HandoffID, result.Receipt.SuccessorWBSessionID, result.Courier, result.Receipt.TmuxName)
		return err
	}
	_, err := fmt.Fprintf(out, "moved session %s to successor %s for handoff %s on %s via %s in tmux %s at exact commit %s; predecessor custody is sealed\n", result.Request.PredecessorWBSessionID, result.Receipt.SuccessorWBSessionID, result.Request.HandoffID, result.Request.TargetMachine, result.Courier, result.Receipt.TmuxName, result.Request.BundleCommit)
	return err
}

// Advisories preserves existing best-effort diagnostics at the caller's stage.
func Advisories(out io.Writer, warnings []secretscan.Finding) {
	for _, finding := range warnings {
		_, _ = fmt.Fprintf(out, "secret scan advisory: %s\n", finding.String())
	}
}

func ParkChecklist(out io.Writer, lead string) {
	_, _ = fmt.Fprintln(out, lead)
	for _, category := range []string{
		"why anything was left uncommitted (a correct fix whose proving test is unwritten is not a finished fix)",
		"ordering constraints proven the hard way, and what proved them",
		"what is blocked on a human decision, and the exact question being asked",
		"which lanes were dispatched, against which repos, and what each was told",
		"corrections: claims made earlier this session that were later disproved",
	} {
		_, _ = fmt.Fprintf(out, "  - %s\n", category)
	}
}
