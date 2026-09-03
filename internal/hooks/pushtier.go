package hooks

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/streambranch"
)

// CheckpointRefPrefix marks a WB checkpoint ref. A push confined to this
// namespace is a fast-persistence path, never a landing receipt: nothing
// about a checkpoint ref implies the work merged or landed anywhere.
const CheckpointRefPrefix = "refs/wb/checkpoints/"

const (
	zeroOID40 = "0000000000000000000000000000000000000000"
	zeroOID64 = "0000000000000000000000000000000000000000000000000000000000000000"
)

// pushTier names the three fixed levels a pre-push hook can run. They compose
// with, and never replace, the always-on Tier 0 admission/guard/diff-check
// block that every managed pre-push shim runs unconditionally: this package
// only ever decides whether the language profile's lint (Tier 1) and test
// (Tier 2) blocks run.
type pushTier int

const (
	// tierIrrelevant marks one ref update that carries no publication
	// requirement of its own (a deletion, a WB checkpoint ref, or a push to a
	// stream branch whose pull request CI is the gate). It never raises the
	// overall decision; it is excluded when combining ref updates.
	tierIrrelevant pushTier = -1
	// TierSkip runs neither lint nor test.
	TierSkip pushTier = 0
	// TierLint runs lint/vet only. This is the fast lane: a feature branch
	// with no open pull request yet.
	TierLint pushTier = 1
	// TierFull runs lint/vet and the full test suite. This is a publication
	// push: the default branch, a tag, or a branch with an open pull request.
	TierFull pushTier = 2
)

// RefUpdate is one line of the pushed-ref list Git streams on pre-push stdin:
// "<local ref> <local sha1> <remote ref> <remote sha1>".
type RefUpdate struct {
	LocalRef  string
	LocalSHA  string
	RemoteRef string
	RemoteSHA string
}

// ParseRefUpdates reads Git's pre-push stdin protocol. Blank lines are
// skipped; anything else that does not carry exactly four fields is rejected
// rather than silently ignored, so a malformed invocation is never
// misclassified as "nothing to push".
func ParseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	var updates []RefUpdate
	scanner := bufio.NewScanner(r)
	// Git never sends a pathologically long single ref-update line, but a
	// generous cap avoids a silent truncation if it ever does.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("malformed pushed-ref line %q: want 4 fields, got %d", line, len(fields))
		}
		updates = append(updates, RefUpdate{
			LocalRef: fields[0], LocalSHA: fields[1],
			RemoteRef: fields[2], RemoteSHA: fields[3],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read pushed-ref list: %w", err)
	}
	return updates, nil
}

func isZeroOID(value string) bool {
	return value == zeroOID40 || value == zeroOID64
}

func isDeletion(update RefUpdate) bool { return isZeroOID(update.LocalSHA) }

func isCheckpointRef(ref string) bool { return strings.HasPrefix(ref, CheckpointRefPrefix) }

func isTagRef(ref string) bool { return strings.HasPrefix(ref, "refs/tags/") }

func branchFromRef(ref string) (string, bool) {
	return strings.CutPrefix(ref, "refs/heads/")
}

// PRLookup answers whether one branch has an open pull request. known is
// false whenever the answer cannot be established locally or within a
// bounded, offline-safe check; a caller must treat that as "unknown", never
// as "no", and must never let "unknown" silently escalate to the full tier.
type PRLookup interface {
	OpenPullRequest(branch string) (open bool, known bool)
}

// Classification is the tier decision for one pre-push invocation, always
// paired with a one-line, human-readable Reason so an agent can tell "fast
// tier, no PR" from "hung" or "full tier, publication push" without guessing.
type Classification struct {
	Tier   pushTier
	Reason string
}

// RunLint reports whether the Tier 1 lint/vet block should run.
func (c Classification) RunLint() bool { return c.Tier >= TierLint }

// RunTest reports whether the Tier 2 test block should run.
func (c Classification) RunTest() bool { return c.Tier >= TierFull }

// ExitCode is the fixed process-exit encoding `wb hooks push-tier` uses to
// hand its decision to the calling shell template: 0, 1, or 2.
func (c Classification) ExitCode() int { return int(c.Tier) }

// ClassifyPushTier decides the tier for one composed set of pushed-ref
// updates. It implements the founder-approved publication rule (option b):
// a publication push is one whose remote ref is the repository's default
// branch, OR is a tag, OR names a branch with an open pull request. Anything
// else pushed to refs/heads/* is the fast lane: lint only. A deletion or a
// WB checkpoint-ref push carries no requirement of its own; if every pushed
// ref is one of those two, the whole push skips both lint and test.
//
// defaultBranch may be empty when it cannot be determined locally (never
// fetched, detached remote HEAD); an empty value simply never matches, so an
// unresolvable default branch degrades to the same publication test as any
// other branch (PR lookup) rather than guessing.
func ClassifyPushTier(updates []RefUpdate, defaultBranch string, lookup PRLookup) Classification {
	if len(updates) == 0 {
		return Classification{Tier: TierFull, Reason: "no pushed-ref lines were observed; running the full tier as a safe default"}
	}
	best := tierIrrelevant
	reason := ""
	for _, update := range updates {
		tier, note := classifyOneRef(update, defaultBranch, lookup)
		if tier > best {
			best, reason = tier, note
		} else if tier == best && reason == "" {
			reason = note
		}
	}
	if best == tierIrrelevant {
		if reason == "" {
			reason = "every pushed ref carries no local verification requirement of its own"
		}
		return Classification{Tier: TierSkip, Reason: reason + "; skipping lint and test"}
	}
	return Classification{Tier: best, Reason: reason}
}

func classifyOneRef(update RefUpdate, defaultBranch string, lookup PRLookup) (pushTier, string) {
	switch {
	case isDeletion(update):
		return tierIrrelevant, fmt.Sprintf("%s is a remote-ref deletion", update.RemoteRef)
	case isCheckpointRef(update.RemoteRef):
		return tierIrrelevant, fmt.Sprintf("%s is a WB checkpoint ref, not a landing receipt", update.RemoteRef)
	// A stream branch carries a draft pull request whose CI verifies every
	// push. Re-running that verification locally duplicates it on the very
	// machine the stream is trying to keep free, so the push hook runs no
	// verification at all here.
	//
	// This case must precede the publication tests below: a stream branch
	// always has an open pull request, which is exactly what would otherwise
	// force the full tier on every single push to it.
	case streambranch.Is(update.RemoteRef):
		return tierIrrelevant, fmt.Sprintf(
			"%s is a stream branch: CI on its stream pull request is the gate, so no local verification runs", update.RemoteRef)
	case isTagRef(update.RemoteRef):
		return TierFull, fmt.Sprintf("%s is a tag: publication push", update.RemoteRef)
	case defaultBranch != "" && update.RemoteRef == "refs/heads/"+defaultBranch:
		return TierFull, fmt.Sprintf("%s is the default branch: publication push", update.RemoteRef)
	}

	branch, isBranch := branchFromRef(update.RemoteRef)
	if !isBranch {
		// An unrecognized ref namespace (neither refs/heads, refs/tags, nor a
		// checkpoint ref) is unfamiliar territory; the safe default is the
		// full tier rather than guessing it is exempt.
		return TierFull, fmt.Sprintf("%s is an unrecognized ref namespace; running the full tier as a safe default", update.RemoteRef)
	}
	if lookup == nil {
		return TierLint, fmt.Sprintf("%s: no pull-request signal available; running the fast lane (CI is the real gate)", branch)
	}
	open, known := lookup.OpenPullRequest(branch)
	switch {
	case !known:
		return TierLint, fmt.Sprintf("%s: open-PR status unknown; running the fast lane (CI is the real gate)", branch)
	case open:
		return TierFull, fmt.Sprintf("%s has an open pull request: publication push", branch)
	default:
		return TierLint, fmt.Sprintf("%s has no open pull request: fast lane", branch)
	}
}
