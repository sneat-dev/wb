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

// ensureClone clones the store when the clone path is missing.
func (p *Provider) ensureClone() error {
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, ".git")); err == nil {
		return nil
	}
	return gitops.Clone(p.opts.CloneURL, p.opts.ClonePath)
}

// Fetch clones if needed and rebases local state onto origin.
func (p *Provider) Fetch(_ context.Context) error {
	if err := p.ensureClone(); err != nil {
		return err
	}
	return gitops.PullRebase(p.opts.ClonePath)
}

// Publish writes the snapshot, commits, and pushes, rebasing once on a
// rejected push. An unchanged snapshot returns the current HEAD without a
// new commit.
func (p *Provider) Publish(ctx context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
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
		if err := gitops.Push(p.opts.ClonePath); err != nil {
			if err := gitops.PullRebase(p.opts.ClonePath); err != nil {
				return remotestate.PublishResult{}, fmt.Errorf("push rejected and rebase failed; local commit kept: %w", err)
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
func (p *Provider) List(ctx context.Context) ([]remotestate.Entry, error) {
	if err := p.Fetch(ctx); err != nil {
		return nil, err
	}
	root := filepath.Join(p.opts.ClonePath, "machines")
	var entries []remotestate.Entry
	err := filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
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
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
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
