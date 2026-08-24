package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemoteClaimCmd() *cobra.Command {
	var note string
	var takeOver, force, jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "claim <task>",
		Short: "Claim a task in the remote store, or refresh your own claim",
		Long: `Claims <task> for this login/machine. Refreshing your own claim (same
login and machine) always succeeds. Taking over someone else's claim needs
either --take-over, which only replaces a claim whose holder's heartbeat has
gone stale (see --stale), or --force, which replaces any claim, stale or
fresh, loudly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteClaim(defaultRemoteDeps(), projectsRoot, args[0], note, takeOver, force, jsonOut, stale, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "free-form note stored with the claim")
	cmd.Flags().BoolVar(&takeOver, "take-over", false, "replace another holder's claim, but only if it is stale")
	cmd.Flags().BoolVar(&force, "force", false, "replace any claim, stale or fresh")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the claim outcome as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "a claim's holder is stale once their snapshot is older than this")
	return cmd
}

// runRemoteClaim always attempts a normal claim first: unheld, this
// acquires; held by the exact login/machine, this refreshes. Only when that
// call reports the task ClaimHeld by someone else do --take-over and
// --force come into play. Staleness is judged here, from the holder's
// snapshot age against --stale, never inside the provider.
func runRemoteClaim(deps remoteDeps, projectsRoot, task, note string, takeOver, force, jsonOut bool, stale time.Duration, out io.Writer) error {
	if err := remotestate.ValidTaskName(task); err != nil {
		return &exitError{code: exitUsage, message: err.Error()}
	}
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	login, err := deps.login()
	if err != nil || login == "" {
		return &exitError{code: exitUsage, message: fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err)}
	}
	mine := remotestate.Claim{
		SchemaVersion: remotestate.ClaimSchemaVersion,
		Task:          task,
		Login:         login,
		Machine:       cfg.Machine,
		ClaimedAt:     deps.now(),
		Note:          note,
	}
	ctx := context.Background()

	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimNormal)
	if err != nil {
		if !force {
			return &exitError{code: exitFindings, message: "claim " + task + ": " + err.Error()}
		}
		// The current claim file could not even be read (e.g. a newer schema
		// version); --force proceeds anyway, but there is no coherent holder
		// to name.
		return forceClaim(ctx, provider, mine, "unreadable claim", jsonOut, out)
	}

	switch outcome.Kind {
	case remotestate.ClaimAcquired:
		return writeClaimOutcome(out, jsonOut, outcome, fmt.Sprintf("claimed %s for %s → %s\n", task, mine.Holder(), outcome.Location))
	case remotestate.ClaimRefreshed:
		return writeClaimOutcome(out, jsonOut, outcome, fmt.Sprintf("refreshed your remote claim on %s\n", task))
	default: // remotestate.ClaimHeld
		return handleClaimHeld(ctx, provider, deps, mine, outcome.Current, takeOver, force, jsonOut, stale, out)
	}
}

// handleClaimHeld composes the refusal, take-over, or force path once the
// normal claim attempt found the task ClaimHeld by another holder.
func handleClaimHeld(ctx context.Context, provider remotestate.Provider, deps remoteDeps, mine, holder remotestate.Claim, takeOver, force, jsonOut bool, stale time.Duration, out io.Writer) error {
	machines, err := provider.List(ctx)
	if err != nil {
		return &exitError{code: exitFindings, message: "claim " + mine.Task + ": read remote store: " + err.Error()}
	}
	isStale := holderStale(machines, holder.Login, holder.Machine, deps.now(), stale)

	switch {
	case force:
		return forceClaim(ctx, provider, mine, holderDesc(mine, holder), jsonOut, out)
	case takeOver && isStale:
		outcome, err := provider.Claim(ctx, mine, remotestate.ClaimTakeOverStale)
		if err != nil {
			return &exitError{code: exitFindings, message: "claim " + mine.Task + ": " + err.Error()}
		}
		hb := heartbeatPhrase(machines, holder.Login, holder.Machine, deps.now(), "never")
		text := fmt.Sprintf("took over %s from %s (their heartbeat: %s)\n", mine.Task, holderDesc(mine, holder), hb)
		return writeClaimOutcome(out, jsonOut, outcome, text)
	case takeOver:
		return &exitError{code: exitFindings, message: fmt.Sprintf("claim is fresh; ask %s to release, or use --force", holderDesc(mine, holder))}
	default:
		hb := heartbeatPhrase(machines, holder.Login, holder.Machine, deps.now(), "never published")
		suffix := "it is fresh — ask them to release, or use --force"
		if isStale {
			suffix = "it is stale — retry with --take-over"
		}
		return &exitError{code: exitFindings, message: fmt.Sprintf("remote claim on %s is held by %s (heartbeat %s); %s", mine.Task, holderDesc(mine, holder), hb, suffix)}
	}
}

// forceClaim replaces whatever claim is there with mine, then prints the
// OVERRIDING banner (holderLabel names who is being overridden, or
// "unreadable claim" when the prior claim file couldn't be decoded at all)
// only on success.
func forceClaim(ctx context.Context, provider remotestate.Provider, mine remotestate.Claim, holderLabel string, jsonOut bool, out io.Writer) error {
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimForce)
	if err != nil {
		return &exitError{code: exitFindings, message: "claim " + mine.Task + ": " + err.Error()}
	}
	text := ""
	if !jsonOut {
		text = fmt.Sprintf("OVERRIDING fresh remote claim by %s on %s\n", holderLabel, mine.Task)
	}
	return writeClaimOutcome(out, jsonOut, outcome, text)
}

func writeClaimOutcome(out io.Writer, jsonOut bool, outcome remotestate.ClaimOutcome, text string) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(outcome)
	}
	if text == "" {
		return nil
	}
	_, err := fmt.Fprint(out, text)
	return err
}
