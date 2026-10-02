package main

import (
	"context"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// This file holds the shared pieces of --quiet (spec/features/
// quiet-verbs-and-masked-status-guard): outcome and refusals only, no progress.
//
// The lifecycle verbs print progress because a long CI wait that says nothing
// looks like a hang. A caller who only wants the outcome used to pipe the verb
// through tail, which hid the verb's exit status and let a && chain run past a
// refusal (sneat-dev/wb#813). --quiet is the supported way to get the outcome
// alone, so the pipe has no reason to exist.

// newLandingProgress is the CI-wait progress sink of a landing verb: live
// lines on stderr, or none under --quiet. A non-terminal agent still gets the
// newline-delimited form unless it asks for quiet.
func newLandingProgress(inv *invocation, command *cobra.Command, nonInteractive bool) *ciWaitProgress {
	interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
	return newCIWaitProgress(progressOutput(command.ErrOrStderr(), interactive), !inv.quiet)
}

// suggestedClosesToPrint is the issue list `wb pr create` offers as a --closes
// suggestion. A suggestion is a courtesy, not an outcome, so --quiet offers
// none and does not read the Work Log for one.
func suggestedClosesToPrint(inv *invocation, ctx context.Context, worktreeArg string) []int {
	if inv.quiet {
		return nil
	}
	return suggestedClosesFromWorktreePrompt(inv, ctx, worktreeArg)
}

// quietArtifacts is the WB-internal artifact list `worktree cleanup` narrates
// as `info:` lines on stderr. Under --quiet there are none: the same artifacts
// stay in the --format json document.
func quietArtifacts(inv *invocation, artifacts []worktrees.LifecycleArtifact) []worktrees.LifecycleArtifact {
	if inv.quiet {
		return nil
	}
	return artifacts
}

// routineClaimNotes are the remote-claim notes that report success. A claim
// that is held by someone else, skipped, or taken over is not routine and is
// never dropped.
var routineClaimNotes = []string{
	"remote claim: acquired ",
	"remote claim: refreshed ",
	"remote claim: released ",
}

// outcomeClaimWriter returns the stream remote-claim notes go to. Under quiet
// it drops the routine success notes and keeps every note that reports a
// problem. Each note is written with one Fprintf, so one Write is one note.
func outcomeClaimWriter(command *cobra.Command, quiet bool) io.Writer {
	out := remoteClaimWriter(command)
	if !quiet {
		return out
	}
	return routineClaimNoteFilter{out: out}
}

type routineClaimNoteFilter struct{ out io.Writer }

func (filter routineClaimNoteFilter) Write(payload []byte) (int, error) {
	note := string(payload)
	for _, prefix := range routineClaimNotes {
		if strings.HasPrefix(note, prefix) {
			return len(payload), nil
		}
	}
	return filter.out.Write(payload)
}

// markQuietVerb makes a quiet-consuming verb findable by `wb commands --search
// quiet`, next to the discovery terms it already carries.
func markQuietVerb(command *cobra.Command) {
	terms := command.Annotations[discoveryTermsAnnotation]
	setDiscoveryTerms(command, strings.TrimSpace(terms+" quiet outcome only no progress pipe tail"))
}
