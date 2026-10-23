// Package cmdstream constructs all stream verbs with isolated operation bindings.
package cmdstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

// streamFailure renders a refusal or a failure and selects the exit code.
// A guard that fired is exit 2 with its stable code and sanctioned command; a
// failure is exit 1. A caller must be able to tell them apart without parsing
// prose.
func streamFailure(runtime shared.Runtime, command *cobra.Command, verb, format string, err error) error {
	refusal, refused := streams.Refused(err)
	if format == "json" {
		envelope := streamEnvelope{Version: 1, Verb: verb, Outcome: outcomeFindings}
		if refused {
			envelope.Outcome = outcomeRefused
			envelope.RefusalCode = refusal.Code
			if len(refusal.Sanctioned) > 0 {
				envelope.SanctionedCommand = refusal.Sanctioned[0]
				envelope.SanctionedCommands = refusal.Sanctioned
			}
			envelope.Evidence = map[string]string{"message": streams.RedactString(refusal.Message)}
		} else {
			envelope.Evidence = map[string]string{"message": streams.RedactString(err.Error())}
		}
		if encodeErr := writeStreamJSON(command.OutOrStdout(), envelope); encodeErr != nil {
			return encodeErr
		}
	}
	if refused {
		return runtime.ExitError(shared.ExitUsage, refusal.Error())
	}
	// Every other failure still reaches stderr through cobra, so it is
	// redacted here rather than at the point it is printed.
	return errors.New(streams.RedactString(err.Error()))
}

// streamUsage turns an invocation WB rejected into the same envelope and exit
// code a guard produces. A caller that asked for --format json must never get
// an empty stdout, and an ambiguous invocation must not be reported with the
// code that means "the work is broken".
func streamUsage(runtime shared.Runtime, command *cobra.Command, verb, format, message string, sanctioned ...string) error {
	return streamFailure(runtime, command, verb, format, &streams.Refusal{
		Code: streams.RefusalUsage, Message: message, Sanctioned: sanctioned,
	})
}

func streamStartOutput(runtime shared.Runtime, command *cobra.Command, verb, format string, result streams.StartResult) error {
	outcome := outcomeSuccess
	if len(result.Reported) > 0 {
		outcome = outcomeFindings
	}
	if format == "json" {
		if err := writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: verb, Outcome: outcome, Evidence: result,
		}); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		if _, err := fmt.Fprintf(out, "stream %s on %s\n", result.Stream.Name, streams.Branch(result.Stream.Name)); err != nil {
			return err
		}
		for _, member := range result.Stream.Members {
			pullRequest := fmt.Sprintf("#%d", member.PullRequest)
			if member.PullRequest == 0 {
				pullRequest = "no draft PR: " + member.PullRequestError
			}
			if _, err := fmt.Fprintf(out, "  %-8s %s  %s  %s\n", member.Role, member.Repository, member.Worktree, pullRequest); err != nil {
				return err
			}
		}
		if err := printStreamFindings(out, result.Reported); err != nil {
			return err
		}
		for _, omitted := range result.TransitiveOmissions {
			if _, err := fmt.Fprintf(out, "  ! %s consumes a stream member but is not in the stream; remote propagation bumps only members\n", omitted); err != nil {
				return err
			}
		}
	}
	if outcome == outcomeFindings {
		return runtime.ExitError(shared.ExitFindings, "the stream was created and reported findings; see the report above")
	}
	return nil
}

func printStreamFindings(out io.Writer, findings []streams.PreflightFinding) error {
	for _, finding := range findings {
		repository := finding.Repository
		if repository == "" {
			repository = "(stream)"
		}
		if _, err := fmt.Fprintf(out, "  ! %s %s [%s] %s\n", repository, finding.Check, finding.Status, finding.Detail); err != nil {
			return err
		}
	}
	return nil
}

func streamStatusOutput(runtime shared.Runtime, command *cobra.Command, format string, status streams.Status) error {
	missingPullRequests := false
	for _, member := range status.Members {
		if (member.PullRequest == 0 && member.Worktree != "") || member.PullRequestUnrecorded {
			missingPullRequests = true
			break
		}
	}
	if format == "json" {
		outcome := outcomeSuccess
		if missingPullRequests {
			outcome = outcomeFindings
		}
		if err := writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream status", Outcome: outcome, Evidence: status,
		}); err != nil {
			return err
		}
		if missingPullRequests {
			return runtime.ExitError(shared.ExitFindings, "stream status reported findings; see the report above")
		}
		return nil
	}
	out := command.OutOrStdout()
	if _, err := fmt.Fprintf(out, "stream %s (%s) on %s\n", status.Stream, status.Phase, status.Branch); err != nil {
		return err
	}
	for _, member := range status.Members {
		if _, err := fmt.Fprintf(out, "  %-8s %-28s unabsorbed=%d links=%d lease=%s\n",
			member.Role, member.Repository, member.Unabsorbed, member.LiveLinks, member.LeaseHolder); err != nil {
			return err
		}
	}
	if missingPullRequests {
		if _, err := fmt.Fprintln(out, "\nmissing member pull requests:"); err != nil {
			return err
		}
		for _, member := range status.Members {
			if (member.PullRequest != 0 || member.Worktree == "") && !member.PullRequestUnrecorded {
				continue
			}
			detail := member.PullRequestMissing
			if member.PullRequestUnrecorded {
				detail = fmt.Sprintf("open pull request #%d exists at %s but is not recorded in stream state", member.PullRequest, member.PullRequestURL)
			} else if detail == "" {
				detail = "no draft pull request is recorded"
			}
			if _, err := fmt.Fprintf(out, "  ! %s: %s\n", member.Repository, detail); err != nil {
				return err
			}
			if member.LastPublicationError != nil {
				when := "at an unknown time"
				if member.LastPublicationError.OccurredAt != nil {
					when = "at " + member.LastPublicationError.OccurredAt.UTC().Format(time.RFC3339)
				}
				if _, err := fmt.Fprintf(out, "    last publication attempt failed %s: %s\n", when, publicationFailureSummary(member.LastPublicationError.Detail)); err != nil {
					return err
				}
			}
			if member.PullRequestBlocked != "" {
				if _, err := fmt.Fprintf(out, "    blocked: %s\n", member.PullRequestBlocked); err != nil {
					return err
				}
				continue
			}
			recovery := member.PullRequestRecovery
			if recovery == "" {
				recovery = "wb stream join " + status.Stream + " " + member.Repository
			}
			if _, err := fmt.Fprintf(out, "    recover: %s\n", recovery); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out, "\nlinked consumers (gap 1):"); err != nil {
		return err
	}
	if len(status.LinkedConsumers) == 0 {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	}
	for _, linked := range status.LinkedConsumers {
		if _, err := fmt.Fprintf(out, "  %s → %s via %s (%s, was %s, content-hash %s)\n",
			linked.Repository, linked.Library, linked.Mechanism, linked.Identity, linked.PreviousVersion, linked.ContentHash); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nmerged but untagged (gap 2):"); err != nil {
		return err
	}
	if status.MergedUntagged == nil {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "  %s: %d commit(s) on %s after %s\n",
			status.MergedUntagged.Repository, len(status.MergedUntagged.Commits),
			status.MergedUntagged.Base, status.MergedUntagged.LatestTag); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nconsumers behind (gap 3):"); err != nil {
		return err
	}
	if len(status.ConsumersBehind) == 0 {
		if _, err := fmt.Fprintln(out, "  none"); err != nil {
			return err
		}
	}
	for _, behind := range status.ConsumersBehind {
		if _, err := fmt.Fprintf(out, "  %s declares %s %s in %s; published is %s\n",
			behind.Repository, behind.Identity, behind.Declared, behind.Manifest, behind.Published); err != nil {
			return err
		}
	}
	if len(status.OpenAgentPullRequests) > 0 {
		if _, err := fmt.Fprintln(out, "\nopen agent pull requests against the stream branch:"); err != nil {
			return err
		}
		for _, pullRequest := range status.OpenAgentPullRequests {
			if _, err := fmt.Fprintf(out, "  %s #%d %s (%s)\n", pullRequest.Repository, pullRequest.Number, pullRequest.Title, pullRequest.Head); err != nil {
				return err
			}
		}
	}
	if len(status.Unknowns) > 0 {
		if _, err := fmt.Fprintln(out, "\ncould not establish:"); err != nil {
			return err
		}
		for _, unknown := range status.Unknowns {
			if _, err := fmt.Fprintf(out, "  ? %s\n", unknown); err != nil {
				return err
			}
		}
	}
	if missingPullRequests {
		return runtime.ExitError(shared.ExitFindings, "stream status reported findings; see the report above")
	}
	return nil
}

func publicationFailureSummary(detail string) string {
	firstLine, _, _ := strings.Cut(strings.TrimSpace(detail), "\n")
	runes := []rune(firstLine)
	if len(runes) > 240 {
		return string(runes[:237]) + "..."
	}
	return firstLine
}

func streamListOutput(command *cobra.Command, format string, all []streams.Stream, unreadable []streams.Unreadable) error {
	if format == "json" {
		return writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream status", Outcome: outcomeSuccess,
			Evidence: map[string]any{"streams": all, "unreadable": unreadable},
		})
	}
	if len(all) == 0 && len(unreadable) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no streams")
		return err
	}
	for _, stream := range all {
		state := string(stream.Lifecycle())
		repositories := make([]string, 0, len(stream.Members))
		for _, member := range stream.Members {
			repositories = append(repositories, member.Repository)
		}
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%-24s %-9s %s\n", stream.Name, state, strings.Join(repositories, " ")); err != nil {
			return err
		}
	}
	for _, broken := range unreadable {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%-24s %-9s unreadable: %s\n", broken.Name, "?", broken.Reason); err != nil {
			return err
		}
	}
	return nil
}

func streamEndOutput(runtime shared.Runtime, command *cobra.Command, format string, result streams.EndResult) error {
	outcome := outcomeSuccess
	if len(result.Errors) > 0 {
		outcome = outcomeFindings
	}
	if format == "json" {
		if err := writeStreamJSON(command.OutOrStdout(), streamEnvelope{
			Version: 1, Verb: "stream end", Outcome: outcome, Evidence: result,
		}); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		verb := "would end"
		if result.Applied {
			verb = "ended"
		}
		if _, err := fmt.Fprintf(out, "%s stream %s\n", verb, result.Stream); err != nil {
			return err
		}
		for _, member := range result.Members {
			if _, err := fmt.Fprintf(out, "  %-28s worktree_removed=%t draft=%s %s\n",
				member.Repository, member.WorktreeRemoved, member.DraftAction, member.Detail); err != nil {
				return err
			}
		}
		for _, pullRequest := range result.AgentPullRequests {
			if _, err := fmt.Fprintf(out, "  agent PR %s #%d: %s %s\n",
				pullRequest.Repository, pullRequest.Number, pullRequest.Action, pullRequest.Detail); err != nil {
				return err
			}
		}
		for _, failure := range result.Errors {
			if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
				return err
			}
		}
		if !result.Applied {
			if _, err := fmt.Fprintln(out, "nothing was changed; re-run with --apply"); err != nil {
				return err
			}
		}
	}
	if outcome == outcomeFindings {
		return runtime.ExitError(shared.ExitFindings, "stream end reported findings")
	}
	return nil
}

func writeStreamJSON(out io.Writer, envelope streamEnvelope) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(envelope)
}
