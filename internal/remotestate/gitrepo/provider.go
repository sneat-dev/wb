// Package gitrepo stores machine snapshots in a git repository: one file per
// machine, history for free, no server.
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

// Options locate the state repository clone and its origin.
type Options struct {
	// ClonePath is <projects-root>/<owner>/<name>; created by cloning when absent.
	ClonePath string
	// CloneURL is what git clone receives when ClonePath does not exist yet.
	CloneURL string
}

// Provider implements remotestate.Provider over a git clone.
type Provider struct {
	opts Options
}

// New returns a provider; nothing touches disk until Publish/Fetch/List.
func New(opts Options) *Provider { return &Provider{opts: opts} }

// SnapshotPath is the store-relative path of one machine's snapshot.
func SnapshotPath(login, machine string) string {
	return path.Join("machines", login, machine, "snapshot.yaml")
}

const readme = `# WB remote state

Machine snapshots published by [wb remote publish](https://wb.sneat.dev).
One file per machine under machines/<login>/<machine>/snapshot.yaml.
Do not edit by hand; run wb remote status to read it.
`

// ensureClone clones the store when the clone path is missing, and verifies
// the origin of an existing clone against the configured remote when it is
// already present — otherwise any directory that happens to hold a `.git`
// would silently be treated as the state repo.
func (p *Provider) ensureClone() error {
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, ".git")); err == nil {
		got, err := gitops.ConfiguredOriginURL(p.opts.ClonePath)
		if err != nil {
			return err
		}
		if !sameRemote(got, p.opts.CloneURL) {
			return fmt.Errorf("%s is a clone of %s, not the configured remote store %s; move it aside or fix remote.repo", p.opts.ClonePath, got, p.opts.CloneURL)
		}
		return nil
	}
	return gitops.Clone(p.opts.CloneURL, p.opts.ClonePath)
}

// sameRemote reports whether a and b name the same git remote, tolerating
// case, a trailing "/", a ".git" suffix, and the https/ssh/scp spellings
// GitHub (and most git hosts) accept interchangeably for the same
// repository: "https://github.com/o/r", "ssh://git@github.com/o/r" and
// "git@github.com:o/r.git" all normalize to "github.com/o/r". Local clone
// sources (no scheme and no scp-like "user@host:" prefix) are additionally
// run through filepath.Clean, so "/tmp/x" and "/tmp/x/" compare equal.
func sameRemote(a, b string) bool {
	return normalizeRemote(a) == normalizeRemote(b)
}

// remoteSchemes lists the URL schemes normalizeRemote strips before
// comparison.
var remoteSchemes = []string{"https://", "http://", "ssh://", "git://"}

// normalizeRemote reduces a git remote URL (or local path) to a
// scheme-independent, case-insensitive form for sameRemote to compare.
func normalizeRemote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	// Hosted forms are case-insensitive in practice (GitHub resolves either
	// spelling), so they compare lowercased. Local paths stay case-sensitive:
	// on Linux /tmp/X and /tmp/x are different directories.
	lowered := strings.ToLower(s)
	for _, scheme := range remoteSchemes {
		if rest, ok := strings.CutPrefix(lowered, scheme); ok {
			return stripUserPrefix(rest)
		}
	}
	if host, path, ok := scpHostPath(lowered); ok {
		return host + "/" + path
	}
	return filepath.Clean(s)
}

// stripUserPrefix removes a "user@" prefix from a URL-style "host/path" (as
// left after stripping a scheme from e.g. ssh://git@github.com/o/r),
// yielding "host/path".
func stripUserPrefix(s string) string {
	if at := strings.Index(s, "@"); at != -1 {
		if slash := strings.Index(s, "/"); slash == -1 || at < slash {
			return s[at+1:]
		}
	}
	return s
}

// scpHostPath recognizes git's scp-like syntax "user@host:path" (e.g.
// git@github.com:o/r) and splits it into host and path. It rejects an "@"
// that belongs to something else (no following ":") and a colon that is
// actually a Windows drive letter local path (e.g. "c:/foo" has no "@", so
// it never reaches here anyway; this guard is for hosts containing "/",
// which a real hostname never does).
func scpHostPath(s string) (host, path string, ok bool) {
	at := strings.Index(s, "@")
	if at == -1 {
		return "", "", false
	}
	rest := s[at+1:]
	colon := strings.Index(rest, ":")
	if colon == -1 {
		return "", "", false
	}
	host = rest[:colon]
	if host == "" || strings.Contains(host, "/") {
		return "", "", false
	}
	return host, rest[colon+1:], true
}

// abortDetailIfRebasing aborts a rebase left in progress by a failed
// PullRebase, so a conflict never leaves the clone wedged mid-rebase — but
// only when one is actually running. A PullRebase failure often never
// starts a rebase at all (e.g. a dirty tracked file, or nothing fetched yet
// because the upstream branch does not exist), and calling RebaseAbort then
// would itself fail with an unrelated "no rebase in progress" error that
// would mask the real cause. wasRebasing is false in exactly that case, and
// callers must then wrap the original error alone.
func abortDetailIfRebasing(clonePath string) (detail string, wasRebasing bool) {
	inProgress, err := gitops.RebaseInProgress(clonePath)
	if err != nil || !inProgress {
		return "", false
	}
	if err := gitops.RebaseAbort(clonePath); err != nil {
		return fmt.Sprintf("rebase abort also failed: %v; local commits kept", err), true
	}
	return "rebase aborted, local commits kept", true
}

// fetchRetryAttempts and fetchRetryBackoff bound Fetch's recovery from the
// transient git failures wb#321 observed: "cannot rebase: Your index
// contains uncommitted changes" and "cannot lock ref ... is at ... but
// expected ..." — both symptoms of a second process's git command touching
// this same working directory mid-operation. cloneLock (see clonelock.go)
// is the real fix for that when the second process is another WB
// invocation; this retry is defense in depth for anything else transient —
// a stray non-WB git command, a lock momentarily held by the OS closing a
// just-finished process, etc. abortDetailIfRebasing already clears a
// left-behind rebase between attempts, so a retry after a dirty-index
// failure gets a clean tree to work with, not a repeat of the same error.
var (
	fetchRetryAttempts = 3
	fetchRetryBackoff  = 100 * time.Millisecond
)

// onFetchRetry, when non-nil, runs immediately after a failed attempt (and
// any rebase-abort recovery) but before Fetch sleeps and retries. It exists
// only so tests can deterministically fix the transient condition between
// attempts instead of racing a timer against a background goroutine; it is
// never set outside tests.
var onFetchRetry func()

// Fetch clones if needed and rebases local state onto origin.
//
// A store with no branches yet (a freshly created, genuinely empty
// repository) has nothing to rebase onto: HasUpstream is false, and Fetch
// returns nil rather than attempting a rebase that git would reject with a
// misleading "no such ref was fetched". The first Publish then creates the
// branch with a plain push.
func (p *Provider) Fetch(_ context.Context) error {
	if err := p.ensureClone(); err != nil {
		return err
	}
	has, err := gitops.HasUpstream(p.opts.ClonePath)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	var lastErr error
	for attempt := 1; attempt <= fetchRetryAttempts; attempt++ {
		if err := gitops.PullRebase(p.opts.ClonePath); err != nil {
			if detail, wasRebasing := abortDetailIfRebasing(p.opts.ClonePath); wasRebasing {
				lastErr = fmt.Errorf("pull --rebase failed (%s): %w", detail, err)
			} else {
				lastErr = fmt.Errorf("pull --rebase failed: %w", err)
			}
			if attempt < fetchRetryAttempts {
				if onFetchRetry != nil {
					onFetchRetry()
				}
				time.Sleep(fetchRetryBackoff)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// push publishes the clone's current branch. A branch with an upstream
// (the ordinary case, and every case after the first publish into a given
// store) pushes plainly; a branch with none — the first publish into a
// store that had no branches at all — pushes with -u so the branch and its
// upstream both come into existence in the same push.
func (p *Provider) push() error {
	has, err := gitops.HasUpstream(p.opts.ClonePath)
	if err != nil {
		return err
	}
	if has {
		return gitops.Push(p.opts.ClonePath)
	}
	branch, err := gitops.CurrentBranch(p.opts.ClonePath)
	if err != nil {
		return err
	}
	return gitops.PushSetUpstream(p.opts.ClonePath, branch)
}

// Publish writes the snapshot, commits, and pushes, rebasing once on a
// rejected push. An unchanged snapshot returns the current HEAD without a
// new commit.
//
// The whole method runs under cloneLock: Fetch plus the write/commit/push
// that follows it is one critical section against the shared clone
// directory, not just the Fetch half of it — see clonelock.go.
func (p *Provider) Publish(ctx context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	lock, err := acquireCloneLock(p.opts.ClonePath)
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	defer func() { _ = lock.release() }()
	if err := p.Fetch(ctx); err != nil {
		return remotestate.PublishResult{}, err
	}
	data, err := remotestate.Encode(snapshot)
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	rel := SnapshotPath(snapshot.Login, snapshot.Machine)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return remotestate.PublishResult{}, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return remotestate.PublishResult{}, err
	}
	readmePath := filepath.Join(p.opts.ClonePath, "README.md")
	if _, err := os.Stat(readmePath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
			return remotestate.PublishResult{}, err
		}
	}
	message := fmt.Sprintf("wb: publish %s @ %s", snapshot.Key(), snapshot.PublishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	committed, err := gitops.AddCommit(p.opts.ClonePath, message, rel, "README.md")
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	if committed {
		pushErr := p.push()
		if pushErr != nil {
			if rebaseErr := gitops.PullRebase(p.opts.ClonePath); rebaseErr != nil {
				if detail, wasRebasing := abortDetailIfRebasing(p.opts.ClonePath); wasRebasing {
					return remotestate.PublishResult{}, fmt.Errorf("push rejected and rebase failed (%s): %w", detail, rebaseErr)
				}
				return remotestate.PublishResult{}, fmt.Errorf("push rejected and rebase failed: %w", rebaseErr)
			}
			if err := gitops.Push(p.opts.ClonePath); err != nil {
				return remotestate.PublishResult{}, fmt.Errorf("push rejected twice; local commit kept for the next publish: %w", err)
			}
		}
	}
	sha, err := gitops.HeadSHA(p.opts.ClonePath)
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: sha}, nil
}

// List fetches and then reads every machines/<login>/<machine>/snapshot.yaml.
// It runs under cloneLock like Publish, even though it never writes: a read
// concurrent with another process's in-flight Fetch/rebase can otherwise
// observe a half-updated working tree.
func (p *Provider) List(ctx context.Context) ([]remotestate.Entry, error) {
	lock, err := acquireCloneLock(p.opts.ClonePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.release() }()
	if err := p.Fetch(ctx); err != nil {
		return nil, err
	}
	root := filepath.Join(p.opts.ClonePath, "machines")
	var entries []remotestate.Entry
	err = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && file == root {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != "snapshot.yaml" {
			return nil
		}
		rel, _ := filepath.Rel(root, file)
		relSlash := filepath.ToSlash(rel)
		parts := strings.Split(relSlash, "/")
		if len(parts) != 3 {
			entries = append(entries, remotestate.Entry{
				Snapshot: remotestate.Snapshot{Login: "?", Machine: relSlash},
				Error:    fmt.Sprintf("unexpected path %s: want machines/<login>/<machine>/snapshot.yaml", relSlash),
			})
			return nil
		}
		entry := remotestate.Entry{Snapshot: remotestate.Snapshot{Login: parts[0], Machine: parts[1]}}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			entry.Error = readErr.Error()
		} else if snapshot, decodeErr := remotestate.Decode(data); decodeErr != nil {
			entry.Error = decodeErr.Error()
		} else {
			snapshot.Login, snapshot.Machine = parts[0], parts[1]
			entry.Snapshot = snapshot
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Snapshot.Key() < entries[j].Snapshot.Key() })
	return entries, nil
}
