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

	// kept[stream][repository] is the links whose removal did NOT succeed.
	// Only the ones that were really removed are cleared from the record: a
	// record cleared after a failed removal hides a link that is still live
	// from the merge guard and from `stream end`, and there is no filesystem
	// signal for an npm link to catch it later.
	kept := map[string]map[string][]streams.Link{}
	undone := map[string]bool{}
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
			outcome, remaining := engine.undoMember(ctx, member)
			outcome.Repository = member.Repository
			result.Consumers = append(result.Consumers, outcome)
			undone[normalizeWorktree(member.Worktree)] = true
			if result.Stream == "" {
				result.Stream = stream.Name
			}
			if kept[stream.Name] == nil {
				kept[stream.Name] = map[string][]streams.Link{}
			}
			kept[stream.Name][member.Repository] = remaining
		}
	}
	for name, byRepository := range kept {
		if _, err := engine.Store.Update(name, func(stream *streams.Stream) error {
			for index := range stream.Members {
				remaining, touched := byRepository[stream.Members[index].Repository]
				if !touched {
					continue
				}
				stream.Members[index].Links = remaining
			}
			return nil
		}); err != nil {
			return result, fmt.Errorf("clear link records in stream %s: %w", name, err)
		}
	}

	// A named consumer with no record can still carry a `go.work` — the
	// window MF-1 used to open, or a hand-written one. The merge guard fires
	// on that file and names this command, so this command has to be able to
	// clear it; otherwise the refusal names something that cannot satisfy it
	// and the worktree can never be landed.
	for _, consumerPath := range options.Consumers {
		consumer, absErr := filepath.Abs(consumerPath)
		if absErr != nil {
			return result, absErr
		}
		if undone[normalizeWorktree(consumer)] {
			continue
		}
		result.Consumers = append(result.Consumers, engine.undoUnrecorded(consumer))
	}
	if len(result.Consumers) == 0 {
		result.Consumers = append(result.Consumers, ConsumerResult{
			Consumer: firstOrEmpty(options.Consumers),
			Skipped:  true,
			Reason:   "no recorded link and no go.work; nothing to undo",
		})
	}
	return result, nil
}

// undoUnrecorded clears a `go.work` that stream state does not know about.
func (engine *Engine) undoUnrecorded(consumer string) ConsumerResult {
	outcome := ConsumerResult{Consumer: consumer}
	entries, err := streams.GoWorkUseEntries(consumer)
	if err != nil {
		outcome.Errors = append(outcome.Errors, err.Error())
		return outcome
	}
	if len(entries) == 0 {
		outcome.Skipped = true
		outcome.Reason = "no recorded link and no go.work; nothing to undo"
		return outcome
	}
	if err := removeGoWork(consumer); err != nil {
		outcome.Errors = append(outcome.Errors, err.Error())
		return outcome
	}
	outcome.Reason = fmt.Sprintf(
		"removed an unrecorded go.work carrying %d use entry/entries; stream state had no record of it",
		len(entries))
	return outcome
}

func normalizeWorktree(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// undoMember removes one member's links and reports which ones survived.
//
// A link whose removal failed is RETURNED, not dropped: its record is what
// keeps the merge guard closed and `stream end` refusing while the artefact is
// still on disk.
func (engine *Engine) undoMember(ctx context.Context, member streams.Member) (ConsumerResult, []streams.Link) {
	outcome := ConsumerResult{Consumer: member.Worktree, Links: member.Links}
	var remaining []streams.Link
	removedWorkspace := false
	for _, link := range member.Links {
		switch link.Mechanism {
		case streams.MechanismGoWork:
			if removedWorkspace {
				continue
			}
			if err := removeGoWork(member.Worktree); err != nil {
				outcome.Errors = append(outcome.Errors, err.Error())
				remaining = append(remaining, link)
				continue
			}
			removedWorkspace = true
		case streams.MechanismPnpmLink:
			if engine.Node == nil {
				outcome.Errors = append(outcome.Errors, fmt.Sprintf("no Node toolchain available to unlink %s", link.Identity))
				remaining = append(remaining, link)
				continue
			}
			if err := engine.Node.Unlink(ctx, member.Worktree, link.Identity); err != nil {
				outcome.Errors = append(outcome.Errors, err.Error())
				remaining = append(remaining, link)
			}
		default:
			outcome.Errors = append(outcome.Errors, "unknown link mechanism "+string(link.Mechanism))
			remaining = append(remaining, link)
		}
	}
	return outcome, remaining
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
