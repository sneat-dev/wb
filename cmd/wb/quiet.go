package main

import (
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// This file holds the shared pieces of --quiet (spec/features/
// quiet-verbs-and-masked-status-guard): outcome and refusals only, no progress.
//
// The lifecycle verbs print progress because a long CI wait that says nothing
// looks like a hang. A caller who only wants the outcome used to pipe the verb
// through tail, which hid the verb's exit status and let a && chain run past a
// refusal (sneat-dev/wb#813). --quiet is the supported way to get the outcome
// alone, so the pipe has no reason to exist.

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
