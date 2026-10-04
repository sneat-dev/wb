package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func renderGuard(environment shared.Runtime, command *cobra.Command, result worktrees.GuardResult, format string, quiet, published bool) error {
	// A warning prints even under --quiet: warn mode exists precisely
	// to be seen before enforcement starts refusing the same commit.
	if result.Admission != nil && result.Admission.Reason != "" {
		_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: %s\n  %s\n", result.Admission.Reason, result.Admission.Remedy)
	}
	if result.Freshness != nil && result.Freshness.Status != worktrees.CanonicalFreshnessCurrent {
		_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: canonical freshness for %s: %s\n", result.Path, formatCanonicalFreshness(result.Freshness))
	}
	// The publication finding is a finding, not a warning: an
	// unpublished HEAD is exactly the state an operator already
	// believed was fine, so it must change the exit code and not
	// merely add a line above an "ok:".
	publicationFinding := ""
	if published {
		publicationFinding = worktrees.PublicationFinding(result.Publication, result.Branch)
		if publicationFinding != "" {
			_, _ = fmt.Fprintf(command.ErrOrStderr(), "unpublished: %s\n", publicationFinding)
		}
	}
	if !quiet {
		switch format {
		case "text":
			checkout := result.Branch
			if result.Transient {
				checkout = "detached HEAD (active " + result.TransientOperation + ")"
			}
			kind := result.Kind
			if result.External {
				kind += " (adopted)"
			}
			suffix := ""
			if result.Freshness != nil && result.Freshness.Status == worktrees.CanonicalFreshnessCurrent {
				suffix = fmt.Sprintf(" (fresh against %s at %s)", result.Freshness.RemoteRef, result.Freshness.RemoteSHA)
			}
			if worktrees.PublicationVerified(result.Publication) {
				suffix += fmt.Sprintf(" (published at %s %s)", result.Publication.RemoteRef, result.Publication.RemoteSHA)
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "ok: %s checkout %s on %s%s\n", kind, result.Path, checkout, suffix); err != nil {
				return err
			}
		case "json":
			encoder := json.NewEncoder(command.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(result); err != nil {
				return err
			}
		}
	}
	if publicationFinding != "" {
		return environment.ExitError(shared.ExitFindings, "HEAD is not verified as published on origin; see the finding above")
	}
	return nil
}

func formatCanonicalFreshness(freshness *worktrees.CanonicalFreshness) string {
	if freshness == nil {
		return "not checked"
	}
	if freshness.Error != "" {
		return fmt.Sprintf("status=%s target=%s: %s", freshness.Status, freshness.RemoteRef, freshness.Error)
	}
	return fmt.Sprintf("status=%s target=%s local=%s remote=%s (%d ahead, %d behind)", freshness.Status, freshness.RemoteRef, freshness.LocalSHA, freshness.RemoteSHA, freshness.Ahead, freshness.Behind)
}
