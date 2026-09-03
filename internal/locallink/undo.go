package locallink

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/streams"
)

// undo restores each consumer's published dependency versions and removes the
// links.
//
// The recorded state — not the library — is the source of truth for reversal,
// so `--undo` succeeds even when the library worktree has since been removed.
// Nothing here reads the library.
//
// Implements: dependency-streams#req:links-are-recorded-and-undoable.
func (engine *Engine) undo(ctx context.Context, options Options) (Result, error) {
	result := Result{Plan: []string{
		"read every recorded link for the named consumers from stream state",
		"remove the untracked link artefacts, restoring the published versions the record names",
		"clear the link records so the merge refusal and stream end stop firing",
	}}
	if engine.Store == nil {
		return result, fmt.Errorf("no stream state is available; --undo reverses recorded links")
	}
	all, unreadable, err := engine.Store.List()
	if err != nil {
		return result, err
	}
	if len(unreadable) > 0 {
		// Undo reverses from the record, so an unreadable record is a link
		// this call cannot reverse. Reporting success while one exists would
		// leave a consumer resolving an unpublished tree.
		names := make([]string, 0, len(unreadable))
		for _, broken := range unreadable {
			names = append(names, broken.Name+" ("+broken.Reason+")")
		}
		return result, fmt.Errorf(
			"stream state is unreadable for %s; --undo reverses recorded links, so it cannot prove every link was removed",
			strings.Join(names, ", "))
	}
	wanted := map[string]bool{}
	for _, consumer := range options.Consumers {
		absolute, absErr := filepath.Abs(consumer)
		if absErr != nil {
			return result, absErr
		}
		wanted[absolute] = true
	}

	cleared := map[string][]string{}
	for _, stream := range all {
		if options.Stream != "" && stream.Name != options.Stream {
			continue
		}
		for _, member := range stream.Members {
			if len(member.Links) == 0 {
				continue
			}
			if len(wanted) > 0 && !matchesAnyWorktree(member.Worktree, wanted) {
				continue
			}
			outcome := engine.undoMember(ctx, member)
			outcome.Repository = member.Repository
			result.Consumers = append(result.Consumers, outcome)
			if result.Stream == "" {
				result.Stream = stream.Name
			}
			cleared[stream.Name] = append(cleared[stream.Name], member.Repository)
		}
	}
	for name, repositories := range cleared {
		if _, err := engine.Store.Update(name, func(stream *streams.Stream) error {
			for index := range stream.Members {
				for _, repository := range repositories {
					if stream.Members[index].Repository == repository {
						stream.Members[index].Links = nil
					}
				}
			}
			return nil
		}); err != nil {
			return result, fmt.Errorf("clear link records in stream %s: %w", name, err)
		}
	}
	if len(result.Consumers) == 0 {
		result.Consumers = append(result.Consumers, ConsumerResult{
			Consumer: firstOrEmpty(options.Consumers),
			Skipped:  true,
			Reason:   "no recorded link; nothing to undo",
		})
	}
	return result, nil
}

func (engine *Engine) undoMember(ctx context.Context, member streams.Member) ConsumerResult {
	outcome := ConsumerResult{Consumer: member.Worktree, Links: member.Links}
	removedWorkspace := false
	for _, link := range member.Links {
		switch link.Mechanism {
		case streams.MechanismGoWork:
			if removedWorkspace {
				continue
			}
			if err := removeGoWork(member.Worktree); err != nil {
				outcome.Errors = append(outcome.Errors, err.Error())
				continue
			}
			removedWorkspace = true
		case streams.MechanismPnpmLink:
			if engine.Node == nil {
				outcome.Errors = append(outcome.Errors, fmt.Sprintf("no Node toolchain available to unlink %s", link.Identity))
				continue
			}
			if err := engine.Node.Unlink(ctx, member.Worktree, link.Identity); err != nil {
				outcome.Errors = append(outcome.Errors, err.Error())
			}
		default:
			outcome.Errors = append(outcome.Errors, "unknown link mechanism "+string(link.Mechanism))
		}
	}
	return outcome
}

func matchesAnyWorktree(worktree string, wanted map[string]bool) bool {
	for candidate := range wanted {
		if sameWorktree(worktree, candidate) {
			return true
		}
	}
	return false
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
