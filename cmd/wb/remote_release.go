package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemoteReleaseCmd() *cobra.Command {
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "release <task>",
		Short: "Release this machine's remote claim on a task",
		Long: `Releases <task> if this login/machine holds it. Releasing a task with
no claim is a no-op, not an error. --force removes another holder's claim
too.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteRelease(defaultRemoteDeps(), projectsRoot, args[0], force, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "release the claim even if another login/machine holds it")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the release outcome as JSON")
	return cmd
}

func runRemoteRelease(deps remoteDeps, projectsRoot, task string, force, jsonOut bool, out io.Writer) error {
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

	outcome, err := provider.Release(context.Background(), task, login, cfg.Machine, force)
	if err != nil {
		return &exitError{code: exitFindings, message: "release " + task + ": " + err.Error()}
	}

	switch outcome.Kind {
	case remotestate.Released:
		return writeReleaseOutcome(out, jsonOut, outcome, fmt.Sprintf("released %s\n", task))
	case remotestate.ReleaseNoop:
		return writeReleaseOutcome(out, jsonOut, outcome, fmt.Sprintf("no remote claim on %s\n", task))
	default: // remotestate.ReleaseHeldByOther
		mine := remotestate.Claim{Login: login, Machine: cfg.Machine}
		return &exitError{code: exitFindings, message: fmt.Sprintf("remote claim on %s is held by %s, not you; --force to release it anyway", task, holderDesc(mine, *outcome.Current))}
	}
}

func writeReleaseOutcome(out io.Writer, jsonOut bool, outcome remotestate.ReleaseOutcome, text string) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(outcome)
	}
	_, err := fmt.Fprint(out, text)
	return err
}
