package fleet

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

// CodeIndexCollector reads the code-index freshness of checkouts from the
// indexer receipts (cockpit#req:code-index-freshness-is-shown). Begin reads
// the receipts and the queue once, for a pass, and the pass it returns answers
// for any number of repositories. A pass from which the receipts could not be
// read is an error, and then no entry carries a code index: an unreadable
// receipt stream is not evidence that nothing was indexed.
type CodeIndexCollector interface {
	Begin() (CodeIndexPass, error)
}

// CodeIndexPass answers for the repositories of one snapshot pass. Key is a
// cheap digest, which runs no Git command, of everything a state depends on
// but HEAD (the configured indexers, the receipts and the queue for these
// checkouts), so a caller can skip States while neither it nor Git state
// moved. States maps each checkout, as it was passed in, to one CodeIndex per
// indexer configured for the repository. An indexer whose state could not be
// told is left out, never guessed, and complete is then false, so the caller
// asks again next pass instead of keeping the gap.
type CodeIndexPass interface {
	Key(identity string, checkouts []string) string
	States(ctx context.Context, identity string, checkouts []string) (states map[string][]CodeIndex, complete bool)
}

// LocalCodeIndex reads the receipts the lifecycle-hook worker writes. It
// stays indexer-agnostic: it knows executor names from the hooks
// configuration and nothing of what any of them produces, and it never starts
// one, opens an artifact, writes a file or fetches. Every Git command goes
// through the hardened helper.
type LocalCodeIndex struct {
	Reader *lifecyclehooks.FreshnessReader
	// Git is the Git binary; empty means "git".
	Git string
}

// Begin reads the receipts and the queue.
func (c LocalCodeIndex) Begin() (CodeIndexPass, error) {
	view, err := c.Reader.Read()
	if err != nil {
		return nil, err
	}
	return localCodeIndexPass{view: view, git: firstNonEmpty(c.Git, "git")}, nil
}

type localCodeIndexPass struct {
	view *lifecyclehooks.FreshnessView
	git  string
}

func (p localCodeIndexPass) Key(identity string, checkouts []string) string {
	return p.view.Signature(identity, checkouts)
}

// verdict is what classifying one indexer on one checkout came to: a state, no
// state that is certain (nothing to report), or a failure that may pass, which
// the caller asks about again next pass.
type verdict int

const (
	verdictState verdict = iota
	verdictNone
	verdictUnknown
)

func (p localCodeIndexPass) States(ctx context.Context, identity string, checkouts []string) (map[string][]CodeIndex, bool) {
	states, complete := map[string][]CodeIndex{}, true
	executors := p.view.Executors(identity)
	for _, checkout := range checkouts {
		// A checkout whose directory is gone has nothing to report, now and
		// until the repository's Git state changes.
		if _, err := os.Stat(checkout); err != nil {
			continue
		}
		var head string
		var headVerdict verdict
		headRead := false
		currentHead := func() (string, verdict) {
			if !headRead {
				head, headVerdict = p.head(ctx, checkout)
				headRead = true
			}
			return head, headVerdict
		}
		for _, executor := range executors {
			state, outcome := p.classify(ctx, checkout, executor, p.view.Record(executor, identity, checkout), currentHead)
			switch outcome {
			case verdictState:
				states[checkout] = append(states[checkout], state)
			case verdictUnknown:
				complete = false
			}
		}
	}
	return states, complete
}

// classify is the state of one indexer on one checkout, from the freshness
// rules: pending when a run is queued or running; never when no receipt
// exists; failed when the latest receipt is a failure; otherwise by comparing
// the latest receipt's SHA, which is a successful one, with HEAD.
func (p localCodeIndexPass) classify(ctx context.Context, checkout, executor string, record lifecyclehooks.Record, head func() (string, verdict)) (CodeIndex, verdict) {
	state := CodeIndex{Indexer: executor}
	if record.Last != nil {
		state.receiptKey = record.Last.SHA + "\x00" + string(record.Last.Status)
	}
	if record.Success != nil {
		state.ReceiptAt = record.Success.At
	}
	switch {
	case record.Pending:
		state.State = CodeIndexPending
	case record.Last == nil:
		state.State = CodeIndexNever
	case record.Last.Status != lifecyclehooks.ReceiptSucceeded:
		state.State, state.ReceiptAt = CodeIndexFailed, record.Last.At
	default:
		current, outcome := head()
		if outcome != verdictState {
			return CodeIndex{}, outcome
		}
		if record.Last.SHA == current {
			state.State = CodeIndexFresh
			return state, verdictState
		}
		return p.relate(ctx, checkout, state, record.Last.SHA, current)
	}
	return state, verdictState
}

// relate says whether the receipt's commit is behind HEAD (stale, with the
// number of commits) or not an ancestor of it (diverged).
//
// A receipt commit the repository does not have, or a SHA that names no
// commit, is diverged: it is not an ancestor of anything. A shallow clone is
// the exception, because it lacks history on purpose: there a commit it does
// not have, and so cannot compare, gets no state at all (the entry is left
// out), and diverged is reported only when the commit is present and Git says
// it is not an ancestor (Git answers a missing object with status 1 or 128; any
// other failure may pass and is asked again). A count is reported only when the commit is proved an
// ancestor, so it is never taken from a truncated history it cannot trust.
func (p localCodeIndexPass) relate(ctx context.Context, checkout string, state CodeIndex, receipt, head string) (CodeIndex, verdict) {
	diverged := func() (CodeIndex, verdict) {
		state.State = CodeIndexDiverged
		return state, verdictState
	}
	if !isObjectID(receipt) {
		return diverged()
	}
	_, ancestorErr := gitOutput(ctx, p.git, checkout, "merge-base", "--is-ancestor", receipt, head)
	if ancestorErr == nil {
		count, err := gitOutput(ctx, p.git, checkout, "rev-list", "--count", receipt+".."+head)
		behind, parseErr := strconv.Atoi(strings.TrimSpace(string(count)))
		if err != nil || parseErr != nil || behind < 1 {
			return CodeIndex{}, verdictUnknown
		}
		state.State, state.Behind = CodeIndexStale, behind
		return state, verdictState
	}
	_, commitErr := gitOutput(ctx, p.git, checkout, "cat-file", "-e", receipt+"^{commit}")
	var exit exitError
	switch {
	case commitErr == nil && notFound(ancestorErr):
		return diverged()
	case commitErr == nil:
		return CodeIndex{}, verdictUnknown
	case errors.As(commitErr, &exit) && exit.code == 128:
		// 128 is Git's generic fatal, which an unreadable or corrupt object
		// store also gives. Only the plain existence check's own "no" (status
		// 1) says the object is missing; the object being there, though not a
		// commit, is an answer too. Anything else may pass.
		if _, plainErr := gitOutput(ctx, p.git, checkout, "cat-file", "-e", receipt); plainErr == nil || notFound(plainErr) {
			return p.missing(ctx, checkout, diverged)
		}
		return CodeIndex{}, verdictUnknown
	}
	if notFound(commitErr) {
		return p.missing(ctx, checkout, diverged)
	}
	return CodeIndex{}, verdictUnknown
}

// missing is the answer for a receipt commit that is not a commit of this
// repository: diverged, except in a shallow clone, which lacks history on
// purpose and gets no state. When Git cannot say whether the clone is shallow
// the answer is not known.
func (p localCodeIndexPass) missing(ctx context.Context, checkout string, diverged func() (CodeIndex, verdict)) (CodeIndex, verdict) {
	out, err := gitOutput(ctx, p.git, checkout, "rev-parse", "--is-shallow-repository")
	switch {
	case err != nil:
		return CodeIndex{}, verdictUnknown
	case strings.TrimSpace(string(out)) == "true":
		return CodeIndex{}, verdictNone
	}
	return diverged()
}

// head is the commit a checkout has checked out. A checkout with no commit yet
// has nothing to compare, which is certain; any other failure may pass.
func (p localCodeIndexPass) head(ctx context.Context, checkout string) (string, verdict) {
	out, err := gitOutput(ctx, p.git, checkout, "rev-parse", "--verify", "--quiet", "HEAD")
	sha := strings.TrimSpace(string(out))
	switch {
	case err == nil && isObjectID(sha):
		return sha, verdictState
	case notFound(err):
		return "", verdictNone
	}
	return "", verdictUnknown
}

// notFound reports a Git command's documented "no" answer, exit status 1.
func notFound(err error) bool {
	var exit exitError
	return errors.As(err, &exit) && exit.code == 1
}
