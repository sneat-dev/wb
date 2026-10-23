package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
)

func renderCreated(out io.Writer, format string, claim worktreerun.RemoteClaimOutcome, results []worktrees.CreateResult) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(worktreeCreateJSON(claim, results))
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(out, "%s %s: %s (%s from origin/%s)\n", result.Action, result.Repository, result.WorktreeDir, result.Branch, result.Base); err != nil {
			return err
		}
	}
	return nil
}

func worktreeCreateJSON(claim worktreerun.RemoteClaimOutcome, results []worktrees.CreateResult) any {
	if claim.Outcome == "disabled" {
		return results
	}
	return struct {
		RemoteClaim worktreerun.RemoteClaimOutcome `json:"remote_claim"`
		Worktrees   []worktrees.CreateResult       `json:"worktrees"`
	}{RemoteClaim: claim, Worktrees: results}
}
