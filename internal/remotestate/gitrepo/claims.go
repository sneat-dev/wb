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

// mutateStore commits and pushes a claims-directory mutation performed by
// mutate, retrying once via rebase on a rejected push. mutate writes or
// deletes the one claim file it owns and returns the commit message plus
// whether it actually changed anything on disk — a byte-identical refresh
// changes nothing, so no commit or push happens and the current HEAD is
// returned as-is.
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
func (p *Provider) mutateStore(mutate func() (message string, changed bool, err error), onLostRace func() error) (sha string, err error) {
	message, changed, err := mutate()
	if err != nil {
		return "", err
	}
	if !changed {
		return gitops.HeadSHA(p.opts.ClonePath)
	}
	committed, err := gitops.AddCommit(p.opts.ClonePath, message, "claims")
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
			return "", fmt.Errorf("push rejected twice; local commit kept for the next attempt: %w", err)
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
func (p *Provider) Claim(ctx context.Context, claim remotestate.Claim, mode remotestate.ClaimMode) (remotestate.ClaimOutcome, error) {
	return p.claim(ctx, claim, mode, false)
}

// claim implements Claim. retried is true only on the one bounded retry
// triggered by onLostRace finding the claim path freed by a concurrent
// Release (see errStorePathFreed) — it prevents an unbounded retry loop if
// the store keeps changing out from under us.
func (p *Provider) claim(ctx context.Context, claim remotestate.Claim, mode remotestate.ClaimMode, retried bool) (remotestate.ClaimOutcome, error) {
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
	default:
		// ClaimTakeOverStale or ClaimForce: the provider never judges
		// staleness itself, it just authorizes replacing another holder.
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

	mutate := func() (string, bool, error) {
		if old, readErr := os.ReadFile(abs); readErr == nil && string(old) == string(data) {
			return message, false, nil
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", false, err
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return "", false, err
		}
		return message, true, nil
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

	sha, err := p.mutateStore(mutate, onLostRace)
	if err != nil {
		if errors.Is(err, errStorePathFreed) {
			return p.claim(ctx, claim, mode, true)
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

	mutate := func() (string, bool, error) {
		if err := os.Remove(abs); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return message, false, nil
			}
			return "", false, err
		}
		return message, true, nil
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

	sha, err := p.mutateStore(mutate, onLostRace)
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
