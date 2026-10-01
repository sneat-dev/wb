package fleet

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Everything below reads a source value and writes only the named metadata
// fields of the document types. A source field not named here is dropped.

// repoEntries is what one local repository contributes to the document,
// observed at one moment. Its counts are filled in when the document is built,
// because the pull-request and active-agent counts need other sources.
type repoEntries struct {
	repository Repository
	worktrees  []Worktree
	branches   []Branch
}

// recordedWorktree is a linked worktree joined with what it records about
// itself.
type recordedWorktree struct {
	linked LinkedWorktree
	record WorktreeRecord
}

// localRepositoryID is the id of a local repository on machine.
func localRepositoryID(machine string, repository discover.Repo) string {
	return entryID(kindRepository, machine, repository.Identity())
}

// mapLocalRepository maps one local repository: its worktrees from their own
// records, and its branches. Its entries all have route local and are
// observed at at; owner state is judged against now. errCode, when set, marks
// the repository as unreadable this pass.
func mapLocalRepository(machine string, repository discover.Repo, defaultBranch string, recorded []recordedWorktree, refs []BranchRef, errCode string, at time.Time) repoEntries {
	identity := repository.Identity()
	entry := func(id string) Entry { return Entry{ID: id, Machine: machine, Route: RouteLocal, ObservedAt: at} }
	repositoryID := localRepositoryID(machine, repository)
	mapped := repoEntries{repository: Repository{
		Entry: entry(repositoryID), Host: repository.Host, Name: repository.Slug(),
		DefaultBranch: firstNonEmpty(repository.DefaultBranch, defaultBranch), Error: errCode,
	}}
	type linkedWorktree struct{ id, task string }
	byBranch := map[string]linkedWorktree{}
	for _, item := range recorded {
		branch := firstNonEmpty(item.linked.Branch, item.record.Branch)
		id := entryID(kindWorktree, machine, identity, item.record.Task, branch)
		activity := item.record.HeartbeatAt
		if activity.IsZero() {
			activity = item.record.CreatedAt
		}
		ownerState := OwnerIdle
		if at.Sub(activity) <= worktrees.DefaultSessionFreshness {
			ownerState = OwnerActive
		}
		byBranch[branch] = linkedWorktree{id: id, task: item.record.Task}
		mapped.worktrees = append(mapped.worktrees, Worktree{
			Entry: entry(id), Repository: repositoryID, Task: item.record.Task, Branch: branch,
			OwnerState: ownerState, LastActivityAt: activity,
		})
	}
	for _, ref := range refs {
		var linked linkedWorktree
		if ref.Scope == BranchLocal {
			linked = byBranch[ref.Name]
		}
		mapped.branches = append(mapped.branches, Branch{
			Entry: entry(entryID(kindBranch, machine, identity, ref.Scope, ref.Name)), Repository: repositoryID,
			Name: ref.Name, Scope: ref.Scope, Task: linked.task, Worktree: linked.id,
			Upstream: ref.Upstream, Ahead: ref.Ahead, Behind: ref.Behind, UpstreamGone: ref.UpstreamGone,
			LastActivityAt: ref.CommittedAt,
		})
	}
	mapped.worktrees = uniqueByID(mapped.worktrees, func(item Worktree) string { return item.ID })
	mapped.branches = uniqueByID(mapped.branches, func(item Branch) string { return item.ID })
	return mapped
}

// mapPullRequests maps the locally recorded pull requests. The record names a
// repository by slug only, so it is attached to a repository when exactly one
// local repository has that slug (repositories maps a slug to the ids of the
// repositories with it) and to a worktree of it when one has the record's
// task; otherwise it keeps no repository and counts as one diagnostic.
func mapPullRequests(machine string, bindings []worktrees.RegisteredPullRequestBinding, at time.Time, repositories map[string][]string, worktreesOf map[string][]Worktree) (mapped []PullRequest, diagnostics int) {
	for _, binding := range bindings {
		pull := PullRequest{
			Entry:  Entry{ID: entryID(kindPR, machine, binding.Repository, strconv.Itoa(binding.PullRequest)), Machine: machine, Route: RouteLocal, ObservedAt: at},
			Number: binding.PullRequest, State: PullRequestUnknown, URL: binding.URL,
		}
		if ids := repositories[binding.Repository]; len(ids) == 1 {
			pull.Repository = ids[0]
			for _, worktree := range worktreesOf[ids[0]] {
				if worktree.Task == binding.Task {
					pull.Worktree, pull.Branch = worktree.ID, worktree.Branch
				}
			}
		} else {
			diagnostics++
		}
		mapped = append(mapped, pull)
	}
	return uniqueByID(mapped, func(item PullRequest) string { return item.ID }), diagnostics
}

// agentRecord is a mapped agent, the slug of the repository a run works in
// (which the build resolves to a repository id) and when it was last active.
type agentRecord struct {
	agent Agent
	slug  string
	when  time.Time
}

// The agents the document carries: at most agentCap, the newest, being
// sessions that are live or parked, runs that are running, and runs that
// finished within agentRecent.
const (
	agentCap    = 200
	agentRecent = 24 * time.Hour
)

// mapAgents maps registered sessions and dispatched runs, observed at at. A
// session with no identifier gets an entry id hashed from its process id and
// no session id, so a raw pid is never emitted. It reports whether the cap cut
// anything.
func mapAgents(machine string, sessions []session.View, runs []agents.Result, at time.Time) (mapped []agentRecord, truncated bool) {
	for _, view := range sessions {
		if view.State != session.StateLive && view.State != session.StateParked {
			continue
		}
		identity := firstNonEmpty(view.WBSessionID, "pid\x00"+strconv.Itoa(view.PID))
		mapped = append(mapped, agentRecord{agent: Agent{
			Entry: Entry{ID: entryID(kindAgent, machine, AgentSession, identity), Machine: machine, Route: RouteLocal, ObservedAt: at},
			Kind:  AgentSession, SessionID: view.WBSessionID, Runtime: view.Runtime, Model: view.Model, State: view.State,
		}, when: view.StartedAt})
	}
	for _, run := range runs {
		when := run.StartedAt
		if run.FinishedAt != nil {
			when = *run.FinishedAt
		}
		if run.State != agents.StateRunning && at.Sub(when) > agentRecent {
			continue
		}
		mapped = append(mapped, agentRecord{agent: Agent{
			Entry: Entry{ID: entryID(kindAgent, machine, AgentRun, run.AgentID), Machine: machine, Route: RouteLocal, ObservedAt: at},
			Kind:  AgentRun, RunID: run.AgentID, Runtime: run.Resolved.Harness, Model: run.Resolved.Model, State: string(run.State),
		}, slug: run.Repository, when: when})
	}
	mapped = uniqueByID(mapped, func(item agentRecord) string { return item.agent.ID })
	sort.Slice(mapped, func(i, j int) bool {
		if !mapped[i].when.Equal(mapped[j].when) {
			return mapped[i].when.After(mapped[j].when)
		}
		return mapped[i].agent.ID < mapped[j].agent.ID
	})
	if len(mapped) > agentCap {
		mapped, truncated = mapped[:agentCap], true
	}
	return mapped, truncated
}

// remoteView is what the other machines' snapshots contribute, mapped.
type remoteView struct {
	machines     []Machine
	repositories []Repository
	worktrees    []Worktree
	pullRequests []PullRequest
}

// mapRemote maps every machine entry but this machine's own last publication
// (the entry named local with login when login is known, else with
// projectsRoot), which is already observed first-hand, and any entry that is unusable: one that carries
// an error (an unreadable or malformed snapshot), has no publish time, or has
// no machine name, or one that is a file path. Of a
// snapshot it keeps machine name and version, repository names, and per
// worktree the task, stream, repository, branch, lifecycle, owner state, last
// activity and open pull request. The snapshot's paths, file names, commit
// subjects, task summaries, head SHAs, owners, attention text and login are not
// read, so they cannot reach the document. Every entry is cached and observed
// at its snapshot's publish time.
func mapRemote(local, login, projectsRoot string, entries []remotestate.Entry) remoteView {
	var view remoteView
	for _, entry := range entries {
		snapshot := entry.Snapshot
		// With no login to compare, the machine's name alone could be another
		// machine's, so the projects root (compared here, never emitted) must
		// be this machine's too.
		own := snapshot.Machine == local && ((login != "" && snapshot.Login == login) || (login == "" && projectsRoot != "" && snapshot.ProjectsRoot == projectsRoot))
		unusable := entry.Error != "" || snapshot.PublishedAt.IsZero() || snapshot.Machine == "" || strings.ContainsAny(snapshot.Machine, `/\`)
		if own || unusable {
			continue
		}
		key := snapshot.Key()
		published := snapshot.PublishedAt
		cached := func(id string) Entry {
			return Entry{ID: id, Machine: snapshot.Machine, Route: RouteCached, ObservedAt: published}
		}
		repositoryNames := map[string]bool{}
		for _, name := range snapshot.KnownRepositories {
			repositoryNames[name] = true
		}
		for _, state := range snapshot.Worktrees {
			repositoryNames[state.Repository] = true
		}
		var repositories []Repository
		repositoryIDs := map[string]string{}
		for name := range repositoryNames {
			id := entryID(kindRepository, key, name)
			repositoryIDs[name] = id
			repositories = append(repositories, Repository{Entry: cached(id), Name: name})
		}
		var worktreeViews []Worktree
		var pullRequests []PullRequest
		for _, state := range snapshot.Worktrees {
			id := entryID(kindWorktree, key, state.Repository, state.Task, state.Branch)
			worktreeViews = append(worktreeViews, Worktree{
				Entry: cached(id), Repository: repositoryIDs[state.Repository], Task: state.Task, Stream: state.Stream,
				Branch: state.Branch, Lifecycle: state.Lifecycle, OwnerState: state.OwnerState, LastActivityAt: state.LastActivityAt,
			})
			if pull := state.PullRequest; pull != nil && strings.EqualFold(pull.State, "open") {
				pullRequests = append(pullRequests, PullRequest{
					Entry: cached(entryID(kindPR, key, state.Repository, strconv.Itoa(pull.Number))), Repository: repositoryIDs[state.Repository],
					Worktree: id, Branch: state.Branch, Number: pull.Number, State: pull.State, URL: pull.URL,
				})
			}
		}
		worktreeViews = uniqueByID(worktreeViews, func(item Worktree) string { return item.ID })
		pullRequests = uniqueByID(pullRequests, func(item PullRequest) string { return item.ID })
		for index := range repositories {
			repositories[index].WorktreeCount = countWhere(worktreeViews, func(item Worktree) bool { return item.Repository == repositories[index].ID })
			open := countWhere(pullRequests, func(item PullRequest) bool { return item.Repository == repositories[index].ID })
			repositories[index].OpenPullRequestCount = &open
		}
		view.machines = append(view.machines, Machine{
			Entry: cached(entryID(kindMachine, key)), WBVersion: snapshot.WBVersion,
			RepositoryCount: len(repositories), WorktreeCount: len(worktreeViews),
		})
		view.repositories = append(view.repositories, repositories...)
		view.worktrees = append(view.worktrees, worktreeViews...)
		view.pullRequests = append(view.pullRequests, pullRequests...)
	}
	view.machines = uniqueByID(view.machines, func(item Machine) string { return item.ID })
	return view
}

// uniqueByID keeps the first of any items sharing an id, so an id is unique
// within its collection even when a source lists one identity twice.
func uniqueByID[T any](items []T, id func(T) string) []T {
	seen := make(map[string]bool, len(items))
	kept := items[:0:0]
	for _, item := range items {
		if key := id(item); !seen[key] {
			seen[key] = true
			kept = append(kept, item)
		}
	}
	return kept
}

func countWhere[T any](items []T, match func(T) bool) int {
	count := 0
	for _, item := range items {
		if match(item) {
			count++
		}
	}
	return count
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// sortByName orders items by name, then id, so a document is deterministic.
func sortByName[T any](items []T, name func(T) string, id func(T) string) {
	sort.Slice(items, func(i, j int) bool {
		if name(items[i]) != name(items[j]) {
			return name(items[i]) < name(items[j])
		}
		return id(items[i]) < id(items[j])
	})
}
