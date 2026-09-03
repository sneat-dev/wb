package locallink

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/streams"
)

// LiveLink is one reason a worktree must not be pushed or landed.
type LiveLink struct {
	// Source is "stream-state" or "go.work": the two signals are independent
	// on purpose.
	Source string `json:"source"`
	// Detail names the offending link or `use` entry.
	Detail string `json:"detail"`
	// Sanctioned is the exact command that clears it.
	Sanctioned string `json:"sanctioned_command"`
}

// LiveLinkStore is the read side of stream state the guard needs. It is an
// interface so a caller that already holds a store passes it straight through,
// and a test provides its own without a WB home.
type LiveLinkStore interface {
	LiveLinksForWorktree(worktree string) ([]streams.StreamLink, error)
}

// HasLiveLink reports every live link recorded against a worktree, from both
// independent signals.
//
// `wb worktree merge` and `wb pr land` MUST refuse a worktree this reports on,
// naming the offending link and the command that clears it, and there is no
// flag that both bypasses the guard and pushes.
//
// The two signals are checked independently and both are reported: stream state
// alone would miss a hand-written `go.work`, and `go.work` alone would miss an
// npm link. A worktree with a hand-written workspace and no stream record is
// still linked, and refusing it is the whole point.
//
// A store that cannot be read is an error, never an empty result: "I could not
// tell" must not be spelled the same way as "there is no link".
//
// Implements: dependency-streams#req:merge-refuses-a-linked-worktree.
func HasLiveLink(store LiveLinkStore, worktree string) ([]LiveLink, error) {
	var found []LiveLink
	if store != nil {
		recorded, err := store.LiveLinksForWorktree(worktree)
		if err != nil {
			return nil, fmt.Errorf("read stream link records for %s: %w", worktree, err)
		}
		for _, link := range recorded {
			found = append(found, LiveLink{
				Source: "stream-state",
				Detail: fmt.Sprintf("stream %s: %s links %s (%s) to %s",
					link.Stream, link.Repository, link.Link.Identity, link.Link.Mechanism, link.Link.Library),
				Sanctioned: fmt.Sprintf("wb deps propagate local %s --to %s --undo", link.Link.Library, worktree),
			})
		}
	}
	entries, err := GoWorkUseEntries(worktree)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		sort.Strings(entries)
		found = append(found, LiveLink{
			Source:     "go.work",
			Detail:     fmt.Sprintf("%s/go.work carries use entries: %s", worktree, strings.Join(entries, ", ")),
			Sanctioned: fmt.Sprintf("wb deps propagate local --to %s --undo", worktree),
		})
	}
	return found, nil
}

// RefusalMessage renders the refusal a landing verb prints. It names every
// offending link and every command that clears one, because a refusal an agent
// cannot resolve becomes a hand-written workaround.
func RefusalMessage(worktree string, links []LiveLink) string {
	if len(links) == 0 {
		return ""
	}
	details := make([]string, 0, len(links))
	commands := make([]string, 0, len(links))
	for _, link := range links {
		details = append(details, link.Source+": "+link.Detail)
		commands = append(commands, link.Sanctioned)
	}
	return fmt.Sprintf(
		"%s has a live local link, so it builds against an unpublished working tree and must not be pushed or landed — %s; clear it with: %s",
		worktree, strings.Join(details, "; "), strings.Join(dedupe(commands), " && "))
}

// Refusal is a guard that fired, carrying the stable code a caller branches on
// and the exact commands that satisfy it.
type Refusal struct {
	Code       string
	Message    string
	Sanctioned []string
}

func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return refusal.Message
	}
	return refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or ")
}

// Refused reports whether err is a guard refusal rather than a failure.
func Refused(err error) (*Refusal, bool) {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal, true
	}
	return nil, false
}

// Refusal codes are contract: skills and the JSON envelope branch on them.
const (
	// RefusalNotRecordable fires when no open stream holds the consumer, so
	// the link could not be recorded. An unrecorded link is un-undoable and
	// invisible to the merge guard's state signal.
	RefusalNotRecordable = "link-not-recordable"
)
