package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// streamWorktrees delegates every checkout a stream needs to the existing
// worktree lifecycle. `stream-is-a-named-set-of-worktrees` forbids a second
// creation path, so this adapter exists to carry the stream's branch and
// prompt into `worktrees.Create` and its removal into `worktrees.Cleanup` —
// never to reimplement either.
type streamWorktrees struct {
	projectsRoot string
	workLog      worktrees.WorkLogOptions
	sessionMode  bool
	base         string
}

// PlannedWorktree is where Create will publish one repository's checkout,
// derived from WB's own layout without touching the filesystem.
//
// The stream record needs each member's intended path BEFORE anything is
// created, so an interrupted start leaves coordinates `wb stream end` can act
// on. Deriving it here — rather than guessing later — keeps that promise
// truthful even when creation never ran.
func (adapter *streamWorktrees) PlannedWorktree(task, repository string) (string, error) {
	owner, name, found := strings.Cut(repository, "/")
	if !found {
		return "", fmt.Errorf("repository must be owner/name, got %q", repository)
	}
	canonical := filepath.Join(adapter.projectsRoot, owner, name)
	placement, err := worktrees.ResolveUserWorktreePlacement(canonical)
	if err != nil {
		return "", err
	}
	return placement.Path(task, repository)
}

func (adapter *streamWorktrees) Create(ctx context.Context, task, branch string, repositories []string) ([]streams.CreatedWorktree, error) {
	// Resume is on because `wb stream join` adds a repository to a task that
	// already exists. Creation still refuses to reuse an existing branch or
	// checkout it did not record, so this widens nothing else.
	results, err := worktrees.Create(ctx, repositories, worktrees.CreateOptions{
		ProjectsRoot:    adapter.projectsRoot,
		Operation:       task,
		Branch:          branch,
		BranchChosen:    true,
		Base:            adapter.base,
		Resume:          true,
		SessionRequired: adapter.sessionMode,
		WorkLog:         adapter.workLog,
	})
	if err != nil {
		return nil, err
	}
	created := make([]streams.CreatedWorktree, 0, len(results))
	for _, result := range results {
		created = append(created, streams.CreatedWorktree{
			Repository: result.Repository,
			Worktree:   result.WorktreeDir,
			Canonical:  result.CanonicalDir,
			Branch:     result.Branch,
			Base:       result.Base,
		})
	}
	return created, nil
}

func (adapter *streamWorktrees) Remove(ctx context.Context, task, repository, worktree string) error {
	outcome, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
		ProjectsRoot:    adapter.projectsRoot,
		Task:            task,
		ExactRepository: repository,
		Apply:           true,
		Workers:         1,
	})
	if err != nil {
		return err
	}
	for _, result := range outcome.Results {
		if !strings.EqualFold(result.Repository, repository) {
			continue
		}
		if result.Applied || result.WorktreeGone {
			return nil
		}
		reason := result.Reason
		if reason == "" {
			reason = "cleanup reported no reason"
		}
		return fmt.Errorf("cleanup did not retire %s: %s", worktree, reason)
	}
	return fmt.Errorf("cleanup reported no candidate for %s at %s", repository, worktree)
}

// streamLeaseIdentity reads the login and machine the stream lease records. It
// reuses the remote-claim configuration rather than inventing a second notion
// of who holds work, and degrades to an unattributed lease rather than
// failing: a fleet that never opted into `wb remote` still gets streams.
func streamLeaseIdentity(dependencies remoteDeps, projectsRoot string) (login, machine string) {
	config, _, err := loadRemote(dependencies, projectsRoot)
	if err != nil {
		return "", ""
	}
	machine = config.Machine
	if resolved, err := dependencies.login(); err == nil {
		login = resolved
	}
	return login, machine
}

// streamSessionIdentity is the live registered session a lease is bound to.
// `claims-carry-a-session-identity` makes session scope the intra-machine
// unit, so the stream records it at start rather than deriving it later.
func streamSessionIdentity() string {
	identity, ok := worktrees.RegisteredIdentity()
	if !ok {
		return ""
	}
	return identity.WBSessionID
}

// proposedTransitiveConsumers reads the transitive consumers of the proposed
// members from the dependency graph evidence `wb deps graph` already wrote.
//
// `stream-membership-is-proposed-from-the-transitive-graph` requires the
// proposal to come from the full transitive walk. Re-walking the fleet inside
// `stream start` would contradict `stream-speed-and-cpu-are-first-class` — the
// walk fetches every repository — so this reads the graph WB already built and
// says so when there is none, rather than either guessing or paying for a
// second walk. found is false when no graph evidence exists.
func proposedTransitiveConsumers(projectsRoot string, members []string) (consumers []string, found bool, err error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, false, err
	}
	adjacency := map[string][]string{}
	for _, ecosystem := range []string{string(deps.EcosystemGo), string(deps.EcosystemNPM)} {
		path := filepath.Join(home, "reports", "deps-graph-"+ecosystem, "deps-graph.json")
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var graph deps.Graph
		if json.Unmarshal(contents, &graph) != nil {
			continue
		}
		found = true
		for _, requirement := range graph.Requirements {
			provider := requirement.ProviderRepository
			consumer := requirement.ConsumerRepository
			if provider == "" || consumer == "" || provider == consumer {
				continue
			}
			adjacency[provider] = append(adjacency[provider], consumer)
		}
	}
	if !found {
		return nil, false, nil
	}
	reached := map[string]bool{}
	queue := append([]string(nil), members...)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, consumer := range adjacency[current] {
			if reached[consumer] {
				continue
			}
			reached[consumer] = true
			queue = append(queue, consumer)
		}
	}
	for name := range reached {
		consumers = append(consumers, name)
	}
	sort.Strings(consumers)
	return consumers, true, nil
}
