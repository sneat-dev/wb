package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// ClaimPath is the store-relative path of one task's claim.
func ClaimPath(task string) string { return path.Join("claims", task+".yaml") }

// readClaim returns the claim at task, a flag for existence, and a decode
// error kept separate so callers can distinguish unreadable from unheld: a
// missing file is exists=false with decodeErr=nil, a present-but-corrupt
// file is exists=true with decodeErr set, and err is reserved for a real
// filesystem failure (permissions, etc.).
func (p *Provider) readClaim(task string) (claim remotestate.Claim, exists bool, decodeErr error, err error) {
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath(task)))
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return remotestate.Claim{}, false, nil, nil
		}
		return remotestate.Claim{}, false, nil, readErr
	}
	c, dErr := remotestate.DecodeClaim(data)
	if dErr != nil {
		return remotestate.Claim{}, true, dErr, nil
	}
	return c, true, nil, nil
}

// stampOwnLastSeen best-effort records claim activity in the calling
// machine's own machines/<login>/<machine>/snapshot.yaml by setting
// LastSeenAt to at and rewriting the file in place. Per the spec's
// failure-handling rule, this never fails the calling mutation: if the
// snapshot is absent (this machine never published) or present but
// undecodable, it returns without touching anything — the claim still
// lands, and published_at is never modified. Callers MUST pass only their
// own login/machine; the per-machine file-ownership rule holds, and this
// method has no way to enforce it itself.
func (p *Provider) stampOwnLastSeen(login, machine string, at time.Time) string {
	rel := SnapshotPath(login, machine)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(rel))
	data, err := os.ReadFile(abs)
	if err != nil {
		return "" // absent (or unreadable): skip silently, never create one
	}
	snap, err := remotestate.Decode(data)
	if err != nil {
		return "" // corrupt: skip silently, leave the bytes exactly as they are
	}
	snap.LastSeenAt = at
	encoded, err := remotestate.Encode(snap)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(abs, encoded, 0o644); err != nil {
		return ""
	}
	return rel
}

// mutateStore commits and pushes a claims-directory mutation performed by
// mutate, retrying once via rebase on a rejected push. mutate writes or
// deletes the one claim file it owns — and, via stampOwnLastSeen, may also
// best-effort rewrite the caller's own machines/.../snapshot.yaml — and
// returns the commit message plus whether it actually changed anything on
// disk — a byte-identical refresh changes nothing, so no commit or push
// happens and the current HEAD is returned as-is. claimPath names the claim
// file mutate touches; it is used only to name the mutation in error
// messages.
//
// onLostRace runs only when a rebase after a rejected push fails with a
// genuine conflict. Since every commit this function makes touches exactly
// one file (the caller's own claim path), such a conflict can only mean
// someone else raced for that same claim: mutateStore aborts the conflicted
// rebase, hard-resets the clone to @{u} (discarding the local commit
// entirely so the clone reflects the winner), and returns onLostRace()'s
// error. A rebase that completes cleanly (e.g. a concurrent commit for a
// different task) is not a race for THIS claim, so mutateStore simply
// pushes again.
//
// A second push rejection (after that rebase-and-retry) is handled
// differently here than in Provider.Publish, which keeps its local commit
// for the next attempt. Publish can afford that: a snapshot publish owns a
// private per-machine file that never conflicts with anyone else's commit,
// so retrying later is always safe. A claims commit has no such guarantee —
// it touches a shared task path — so a claim/release commit left sitting
// locally after two rejections can go on to conflict with whatever a
// competing machine lands on that same path next, and Provider.Fetch has no
// recovery for a wedged rebase: every later remote command on this machine
// would fail until someone manually reset the clone. Claims are trivially
// re-creatable (just claim/release again), so mutateStore instead discards
// the local commit and resets the clone to upstream, leaving it healthy for
// the next attempt.
func (p *Provider) mutateStore(claimPath string, mutate func() (message string, changed bool, extraPaths []string, err error), onLostRace func() error) (sha string, err error) {
	message, changed, extraPaths, err := mutate()
	if err != nil {
		return "", err
	}
	if !changed {
		return gitops.HeadSHA(p.opts.ClonePath)
	}
	// Scoped staging: the claims directory plus exactly the paths mutate
	// says it touched (the caller's own snapshot stamp). Never "." — with
	// no inter-process lock on the clone, a bare "." could sweep another
	// process's half-written files into a claim commit.
	committed, err := gitops.AddCommit(p.opts.ClonePath, message, append([]string{"claims"}, extraPaths...)...)
	if err != nil {
		return "", err
	}
	if !committed {
		return gitops.HeadSHA(p.opts.ClonePath)
	}
	if pushErr := p.push(); pushErr != nil {
		if rebaseErr := gitops.PullRebase(p.opts.ClonePath); rebaseErr != nil {
			if _, wasRebasing := abortDetailIfRebasing(p.opts.ClonePath); wasRebasing {
				if resetErr := gitops.ResetHardUpstream(p.opts.ClonePath); resetErr != nil {
					return "", fmt.Errorf("push rejected and rebase conflicted; reset to upstream also failed: %w", resetErr)
				}
				return "", onLostRace()
			}
			return "", fmt.Errorf("push rejected and rebase failed: %w", rebaseErr)
		}
		if err := p.push(); err != nil {
			// Second rejection: discard the local commit rather than keep
			// it, per the doc comment above. abortDetailIfRebasing is
			// defensive here — the PullRebase above already completed
			// cleanly, so no rebase should be in progress — but calling it
			// keeps this branch safe even if that assumption ever breaks.
			abortDetailIfRebasing(p.opts.ClonePath)
			if resetErr := gitops.ResetHardUpstream(p.opts.ClonePath); resetErr != nil {
				return "", fmt.Errorf("push rejected twice for %s; local change discarded — retry (reset to upstream also failed: %w)", claimPath, resetErr)
			}
			return "", fmt.Errorf("push rejected twice for %s; local change discarded — retry: %w", claimPath, err)
		}
	}
	return gitops.HeadSHA(p.opts.ClonePath)
}

// errStorePathFreed is onLostRace's internal signal that the rebase
// conflict was actually a competing Release: the post-reset re-read found
// the claim path gone, not held by someone else. It never escapes this
// file — claim/release catch it and retry the whole operation once against
// the now-current store state before it can reach a caller.
var errStorePathFreed = errors.New("gitrepo: claim path freed by a concurrent operation")

// Claim acquires or refreshes a claim on a task.
func (p *Provider) Claim(ctx context.Context, claim remotestate.Claim, mode remotestate.ClaimMode, expectedHolder string) (remotestate.ClaimOutcome, error) {
	return p.claim(ctx, claim, mode, expectedHolder, false)
}

// claim implements Claim. retried is true only on the one bounded retry
// triggered by onLostRace finding the claim path freed by a concurrent
// Release (see errStorePathFreed) — it prevents an unbounded retry loop if
// the store keeps changing out from under us.
func (p *Provider) claim(ctx context.Context, claim remotestate.Claim, mode remotestate.ClaimMode, expectedHolder string, retried bool) (remotestate.ClaimOutcome, error) {
	if err := remotestate.ValidTaskName(claim.Task); err != nil {
		return remotestate.ClaimOutcome{}, err
	}
	if err := p.Fetch(ctx); err != nil {
		return remotestate.ClaimOutcome{}, err
	}

	current, exists, decodeErr, err := p.readClaim(claim.Task)
	if err != nil {
		return remotestate.ClaimOutcome{}, err
	}
	if decodeErr != nil && mode != remotestate.ClaimForce {
		return remotestate.ClaimOutcome{}, fmt.Errorf("%s is unreadable (%v); use --force to replace it", ClaimPath(claim.Task), decodeErr)
	}

	var previous *remotestate.Claim
	var kind remotestate.ClaimOutcomeKind
	var message string
	switch {
	case decodeErr != nil:
		// Only reachable with mode == ClaimForce (checked above): overwrite
		// an unreadable file. Previous stays nil — we cannot know who, if
		// anyone coherent, held it.
		kind = remotestate.ClaimAcquired
		message = fmt.Sprintf("wb: claim %s by %s", claim.Task, claim.Holder())
	case !exists:
		kind = remotestate.ClaimAcquired
		message = fmt.Sprintf("wb: claim %s by %s", claim.Task, claim.Holder())
	case current.Login == claim.Login && current.Machine == claim.Machine:
		// Refresh applies only to the exact login/machine, regardless of mode.
		kind = remotestate.ClaimRefreshed
		message = fmt.Sprintf("wb: claim %s by %s", claim.Task, claim.Holder())
	case mode == remotestate.ClaimNormal:
		return remotestate.ClaimOutcome{Kind: remotestate.ClaimHeld, Current: current}, nil
	case mode == remotestate.ClaimTakeOverStale && expectedHolder != "" && current.Holder() != expectedHolder:
		// The holder the caller judged stale is no longer the current
		// holder: they released and a fresh third party claimed the task
		// (or refreshed away their own staleness) between the caller's
		// staleness judgment and this call landing. Replacing whoever holds
		// it NOW would be wrong — report it as an ordinary ClaimHeld naming
		// the actual current holder instead, exactly like ClaimNormal would.
		return remotestate.ClaimOutcome{Kind: remotestate.ClaimHeld, Current: current}, nil
	default:
		// ClaimTakeOverStale (holder confirmed, or no expectedHolder given)
		// or ClaimForce: the provider never judges staleness itself, it
		// just authorizes replacing another holder.
		held := current
		previous = &held
		kind = remotestate.ClaimTookOver
		message = fmt.Sprintf("wb: take over %s from %s by %s", claim.Task, current.Holder(), claim.Holder())
	}

	rel := ClaimPath(claim.Task)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(rel))
	data, err := remotestate.EncodeClaim(claim)
	if err != nil {
		return remotestate.ClaimOutcome{}, err
	}

	mutate := func() (string, bool, []string, error) {
		if old, readErr := os.ReadFile(abs); readErr == nil && string(old) == string(data) {
			return message, false, nil, nil
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", false, nil, err
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return "", false, nil, err
		}
		// Best-effort: stamp the claimant's own snapshot in the same commit
		// as the claim write. Only reached when the claim file itself is
		// actually changing, so a byte-identical refresh (handled above)
		// never dirties the clone with an uncommitted stamp.
		var extra []string
		if stamped := p.stampOwnLastSeen(claim.Login, claim.Machine, claim.ClaimedAt); stamped != "" {
			extra = append(extra, stamped)
		}
		return message, true, extra, nil
	}
	onLostRace := func() error {
		other, stillExists, dErr, rErr := p.readClaim(claim.Task)
		if rErr != nil {
			return fmt.Errorf("lost the race for %s: %w", claim.Task, rErr)
		}
		// A concurrent write we can't coherently attribute a holder to:
		// either an unreadable file we're not authorized to force over, or
		// one we are — either way it still holds the path, so it's not the
		// "freed by a release" case below.
		heldOrUnreadable := stillExists && (dErr == nil || mode != remotestate.ClaimForce)
		if heldOrUnreadable {
			if dErr != nil {
				return fmt.Errorf("%s is unreadable after losing the race for %s (%v); use --force to replace it", ClaimPath(claim.Task), claim.Task, dErr)
			}
			return fmt.Errorf("lost the race for %s to %s", claim.Task, other.Holder())
		}
		// The path is free (a competing Release landed) or force-overwritable.
		// Retry once against current store state rather than reporting a
		// phantom winner; a second lost race means the store is thrashing.
		if retried {
			return fmt.Errorf("store is changing too fast for %s; retry", claim.Task)
		}
		return errStorePathFreed
	}

	sha, err := p.mutateStore(rel, mutate, onLostRace)
	if err != nil {
		if errors.Is(err, errStorePathFreed) {
			return p.claim(ctx, claim, mode, expectedHolder, true)
		}
		return remotestate.ClaimOutcome{}, err
	}
	return remotestate.ClaimOutcome{Kind: kind, Current: claim, Previous: previous, Location: sha}, nil
}

// Release removes a claim.
func (p *Provider) Release(ctx context.Context, task, login, machine string, force bool) (remotestate.ReleaseOutcome, error) {
	return p.release(ctx, task, login, machine, force, false)
}

// release implements Release. retried mirrors claim's bounded-retry flag:
// true only on the one retry triggered by onLostRace finding the claim
// path already gone (see errStorePathFreed).
func (p *Provider) release(ctx context.Context, task, login, machine string, force, retried bool) (remotestate.ReleaseOutcome, error) {
	if err := remotestate.ValidTaskName(task); err != nil {
		return remotestate.ReleaseOutcome{}, err
	}
	if err := p.Fetch(ctx); err != nil {
		return remotestate.ReleaseOutcome{}, err
	}

	current, exists, decodeErr, err := p.readClaim(task)
	if err != nil {
		return remotestate.ReleaseOutcome{}, err
	}
	if !exists {
		return remotestate.ReleaseOutcome{Kind: remotestate.ReleaseNoop}, nil
	}
	if !force {
		if decodeErr != nil {
			return remotestate.ReleaseOutcome{}, fmt.Errorf("%s is unreadable (%v); use --force to remove it", ClaimPath(task), decodeErr)
		}
		if current.Login != login || current.Machine != machine {
			held := current
			return remotestate.ReleaseOutcome{Kind: remotestate.ReleaseHeldByOther, Current: &held}, nil
		}
	}

	rel := ClaimPath(task)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(rel))
	message := fmt.Sprintf("wb: release %s by %s/%s", task, login, machine)

	mutate := func() (string, bool, []string, error) {
		if err := os.Remove(abs); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return message, false, nil, nil
			}
			return "", false, nil, err
		}
		// Best-effort: stamp the releasing machine's own snapshot in the
		// same commit as the claim removal. Release has no caller-supplied
		// operation time (unlike Claim's ClaimedAt), so it self-stamps with
		// the current time.
		var extra []string
		if stamped := p.stampOwnLastSeen(login, machine, time.Now().UTC()); stamped != "" {
			extra = append(extra, stamped)
		}
		return message, true, extra, nil
	}
	onLostRace := func() error {
		other, stillExists, dErr, rErr := p.readClaim(task)
		if rErr != nil {
			return fmt.Errorf("lost the race for %s: %w", task, rErr)
		}
		heldOrUnreadable := stillExists && (dErr == nil || !force)
		if heldOrUnreadable {
			if dErr != nil {
				return fmt.Errorf("%s is unreadable after losing the race for %s (%v); use --force to remove it", ClaimPath(task), task, dErr)
			}
			return fmt.Errorf("lost the race for %s to %s", task, other.Holder())
		}
		// The path is already gone (a competing Release beat us to it) or
		// force-removable. Retry once: the retried call's own !exists check
		// naturally reports this as the idempotent ReleaseNoop.
		if retried {
			return fmt.Errorf("store is changing too fast for %s; retry", task)
		}
		return errStorePathFreed
	}

	sha, err := p.mutateStore(rel, mutate, onLostRace)
	if err != nil {
		if errors.Is(err, errStorePathFreed) {
			return p.release(ctx, task, login, machine, force, true)
		}
		return remotestate.ReleaseOutcome{}, err
	}
	return remotestate.ReleaseOutcome{Kind: remotestate.Released, Location: sha}, nil
}

// Claims returns every claim currently in the store, sorted by task name.
// Only files directly under claims/*.yaml are claims; a directory or a file
// at any other depth/extension becomes an error entry, the same philosophy
// List uses for machines/*.
func (p *Provider) Claims(ctx context.Context) ([]remotestate.ClaimEntry, error) {
	if err := p.Fetch(ctx); err != nil {
		return nil, err
	}
	root := filepath.Join(p.opts.ClonePath, "claims")
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var entries []remotestate.ClaimEntry
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() {
			entries = append(entries, remotestate.ClaimEntry{
				Claim: remotestate.Claim{Task: name},
				Error: fmt.Sprintf("claims/%s is a directory, want claims/<task>.yaml", name),
			})
			continue
		}
		if !strings.HasSuffix(name, ".yaml") {
			entries = append(entries, remotestate.ClaimEntry{
				Claim: remotestate.Claim{Task: name},
				Error: fmt.Sprintf("unexpected file claims/%s, want claims/<task>.yaml", name),
			})
			continue
		}
		task := strings.TrimSuffix(name, ".yaml")
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			entries = append(entries, remotestate.ClaimEntry{Claim: remotestate.Claim{Task: task}, Error: readErr.Error()})
			continue
		}
		claim, decErr := remotestate.DecodeClaim(data)
		if decErr != nil {
			entries = append(entries, remotestate.ClaimEntry{Claim: remotestate.Claim{Task: task}, Error: decErr.Error()})
			continue
		}
		claim.Task = task
		entries = append(entries, remotestate.ClaimEntry{Claim: claim})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Claim.Task < entries[j].Claim.Task })
	return entries, nil
}
