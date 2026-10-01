package fleet

import (
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// localMachineID is the id of the machine entry of this machine.
func localMachineID(machine string) string { return entryID(kindMachine, machine) }

// localEntry is the entry of state this daemon observed itself on machine.
func localEntry(id, machine string, at time.Time) Entry {
	return Entry{ID: id, Machine: machine, MachineID: localMachineID(machine), Route: RouteLocal, ObservedAt: at}
}

// localRepositoryID is the id of a local repository on machine.
func localRepositoryID(machine string, repository discover.Repo) string {
	return entryID(kindRepository, machine, repository.Identity())
}

// localCodeIndex is the code-index state of a local repository's checkouts:
// its own, and each recorded worktree's by the worktree's path, which stays
// inside the daemon.
//
// asked is what the provider answered, by checkout, and the receipt each
// answer was for, so a checkout is asked once per receipt.
type localCodeIndex struct {
	repository []CodeIndex
	byPath     map[string][]CodeIndex
	asked      map[string]askedStatistics
	// retry says a provider answer is still owed or failed within its attempt
	// cap, so the next pass reads the states again instead of reusing them.
	retry bool
}

// askedStatistics is a provider's answer for one checkout and the time of the
// receipt it was asked at; zero for a checkout with no receipt.
type askedStatistics struct {
	receipt  time.Time
	key      string
	stats    CodeStatistics
	attempts int
	final    bool
}

// mapLocalRepository maps one local repository: its worktrees from their own
// records, and its branches. Its entries all have route local and are
// observed at at; owner state is judged against now. errCode, when set, marks
// the repository as unreadable this pass. codeIndex is the freshness of its
// checkouts; the zero value means none is known. originHost is the forge host
// the origin names, which fills the host of a flat-layout clone (its placement
// has none); the entry id still comes from the placement.
func mapLocalRepository(machine string, repository discover.Repo, defaultBranch string, recorded []recordedWorktree, refs []BranchRef, errCode string, codeIndex localCodeIndex, originHost string, at time.Time) repoEntries {
	identity := repository.Identity()
	entry := func(id string) Entry { return localEntry(id, machine, at) }
	repositoryID := localRepositoryID(machine, repository)
	host := firstNonEmpty(repository.Host, originHost)
	mapped := repoEntries{repository: Repository{
		Entry: entry(repositoryID), Host: host, Name: repository.Slug(),
		DefaultBranch: firstNonEmpty(repository.DefaultBranch, defaultBranch), Error: errCode, CodeIndex: codeIndex.repository,
		RemoteURLWeb: webURL(host, repository.Slug()),
	}}
	localRefs := map[string]BranchRef{}
	for _, ref := range refs {
		if ref.Scope == BranchLocal {
			localRefs[ref.Name] = ref
			if ref.CommittedAt.After(mapped.repository.LastActivityAt) {
				mapped.repository.LastActivityAt = ref.CommittedAt
			}
		}
	}
	type linkedWorktree struct{ id, task string }
	byBranch := map[string]linkedWorktree{}
	for _, item := range recorded {
		branch := firstNonEmpty(item.linked.Branch, item.record.Branch)
		id := entryID(kindWorktree, machine, identity, item.record.Task, branch)
		activity := item.record.HeartbeatAt
		if activity.IsZero() {
			activity = item.record.CreatedAt
		}
		byBranch[branch] = linkedWorktree{id: id, task: item.record.Task}
		worktree := Worktree{
			Entry: entry(id), Repository: repositoryID, Name: item.record.Task, Task: item.record.Task, Branch: branch,
			OwnerState: localOwnerState(item.record.Owner), LastActivityAt: activity, CodeIndex: codeIndex.byPath[item.linked.Path],
		}
		if ref, tracked := localRefs[branch]; tracked {
			hasUpstream := ref.Upstream != ""
			worktree.HasUpstream = &hasUpstream
			switch {
			case ref.UpstreamGone:
				worktree.UpstreamGone = true
			case hasUpstream:
				ahead, behind := ref.Ahead, ref.Behind
				worktree.Ahead, worktree.Behind = &ahead, &behind
			}
		}
		mapped.worktrees = append(mapped.worktrees, worktree)
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

// localOwnerState is the owner state of a worktree of this machine, from the
// liveness of its recorded owner process (cockpit-views#req:owner-state-
// vocabulary): alive is active, gone is orphaned, and no process recorded is
// unknown. The heartbeat plays no part.
func localOwnerState(owner string) string {
	switch owner {
	case worktrees.OwnerLive:
		return OwnerActive
	case worktrees.OwnerGone:
		return OwnerOrphaned
	}
	return OwnerUnknown
}

// maxRemoteText caps a free-text field taken from another machine's snapshot.
const maxRemoteText = 200

// plainText is text with control and format characters (which include the
// bidirectional controls) removed and at most maxRemoteText runes kept: a
// snapshot is another machine's data, and a name in it is shown as text only.
func plainText(text string) string { return plainTextMax(text, maxRemoteText) }

// plainTextMax is plainText cut to at most limit characters.
func plainTextMax(text string, limit int) string {
	var kept []rune
	for _, character := range text {
		if len(kept) == limit {
			break
		}
		if !unsafeRune(character) {
			kept = append(kept, character)
		}
	}
	return string(kept)
}

// unsafeRune reports whether character is one plainText removes: a control or
// format character, or a line or paragraph separator.
func unsafeRune(character rune) bool {
	return unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
}

// remoteLifecycles are the lifecycle values a published snapshot is built with.
var remoteLifecycles = []string{"working", "review", "merged", "superseded"}

// publishedLifecycle keeps a published lifecycle that is one of the vocabulary
// and drops any other value.
func publishedLifecycle(lifecycle string) string {
	if slices.Contains(remoteLifecycles, lifecycle) {
		return lifecycle
	}
	return ""
}

// maxURLLength bounds an address taken from a pull request record.
const maxURLLength = 2048

// safeHTTPSURL is rawURL, with its scheme in lower case, when it is an https
// address of printable ASCII, at most maxURLLength long, whose host passes the
// hostname rule and is neither an IP literal nor localhost, with no port and no
// user information; else empty. A pull request address reaches the browser as a
// link, so only this shape does. A GitHub Enterprise address with a port loses
// its link by design.
func safeHTTPSURL(rawURL string) string {
	if len(rawURL) > maxURLLength || len(rawURL) < len("https") ||
		strings.ContainsFunc(rawURL, func(character rune) bool { return character <= ' ' || character > '~' }) {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Opaque != "" ||
		len(parsed.Host) > 253 || !hostnamePattern.MatchString(parsed.Host) || !hasLetterLabel(parsed.Host) ||
		parsed.Host == "localhost" || strings.HasSuffix(parsed.Host, ".localhost") {
		return ""
	}
	return "https" + rawURL[len("https"):]
}

// hasLetterLabel reports whether the last label of host holds a letter, which
// no IP literal does (a numeric dotted host is an address, not a name).
func hasLetterLabel(host string) bool {
	last := host[strings.LastIndex(host, ".")+1:]
	return strings.ContainsFunc(last, unicode.IsLetter)
}

// earliestBootTime is the oldest boot time a snapshot may publish.
var earliestBootTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// publishedBootTime is bootTime when it lies between 2000-01-01 and now, else
// the zero time.
func publishedBootTime(bootTime, now time.Time) time.Time {
	if bootTime.Before(earliestBootTime) || bootTime.After(now) {
		return time.Time{}
	}
	return bootTime
}

// hostnamePattern is a dotted DNS name: labels of letters, digits and inner
// hyphens. It admits no port, path, query, space or percent escape.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// segmentPattern is one path segment of a repository address.
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// webURL is the https://<host>/<owner>/<name> address of a repository
// (cockpit-views#req:repository-activity-fields), built from the forge host and
// the repository's owner/name alone, and empty unless the host matches a
// hostname pattern and both segments match [A-Za-z0-9._-]+ and are not "." or
// "..". It is never taken from an origin URL, which may carry a credential.
func webURL(host, name string) string {
	owner, repo, found := strings.Cut(name, "/")
	if !found || len(host) > 253 || !hostnamePattern.MatchString(host) {
		return ""
	}
	for _, segment := range []string{owner, repo} {
		if !segmentPattern.MatchString(segment) || segment == "." || segment == ".." {
			return ""
		}
	}
	return "https://" + host + "/" + owner + "/" + repo
}

// shortNamePattern is an operating system or architecture name.
var shortNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// shortName is name when it is a short plain word, else empty: a snapshot is
// another machine's data and an odd value is dropped.
func shortName(name string) string {
	if shortNamePattern.MatchString(name) {
		return name
	}
	return ""
}

// maxCPUCount bounds a published CPU count; a larger number is dropped.
const maxCPUCount = 65536

// cpuCount is count when it is plausible, else zero (omitted).
func cpuCount(count int) int {
	if count < 1 || count > maxCPUCount {
		return 0
	}
	return count
}

// publishedOwnerState keeps the owner state another machine published when it
// is one of the vocabulary and drops any other value.
func publishedOwnerState(state string) string {
	if slices.Contains([]string{OwnerActive, OwnerIdle, OwnerOrphaned, OwnerUnknown}, state) {
		return state
	}
	return ""
}

// splitForgeName splits a snapshot's repository name that starts with a
// hostname-like segment (one with a dot, as github.com is) into the host and
// the rest, and returns the name unchanged with no host otherwise.
func splitForgeName(name string) (host, rest string) {
	first, remainder, found := strings.Cut(name, "/")
	if found && strings.Contains(first, ".") && hostnamePattern.MatchString(first) && hasLetterLabel(first) && strings.Contains(remainder, "/") &&
		!slices.Contains(strings.Split(remainder, "/"), "") {
		return first, remainder
	}
	return "", name
}

// mapPullRequests maps the locally recorded pull requests. The record names a
// repository by slug only, so it is attached to a repository when exactly one
// local repository has that slug (repositories maps a slug to the ids of the
// repositories with it) and to a worktree of it when one has the record's
// task; otherwise it keeps no repository and counts as one diagnostic.
func mapPullRequests(machine string, bindings []worktrees.RegisteredPullRequestBinding, at time.Time, repositories map[string][]string, worktreesOf map[string][]Worktree, observed map[string]pullObservation) (mapped []PullRequest, diagnostics int) {
	for _, binding := range bindings {
		pull := PullRequest{
			Entry:  localEntry(entryID(kindPR, machine, binding.Repository, strconv.Itoa(binding.PullRequest)), machine, at),
			Number: binding.PullRequest, URL: safeHTTPSURL(binding.URL),
		}
		if observation, ok := observed[pullKey(binding.Repository, binding.PullRequest)]; ok {
			observation.applyTo(&pull)
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
			Entry: localEntry(entryID(kindAgent, machine, AgentSession, identity), machine, at),
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
			Entry: localEntry(entryID(kindAgent, machine, AgentRun, run.AgentID), machine, at),
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

// remoteView is what the other machines' snapshots contribute, mapped. named
// lists the machine entries by the machine name their snapshot was published
// under, with the login, so that a configured machine's published entries can be
// found by its configured key.
type remoteView struct {
	machines     []Machine
	repositories []Repository
	worktrees    []Worktree
	pullRequests []PullRequest
	named        map[string][]publishedMachine
}

// publishedMachine is a published-store machine entry's id and the login it was
// published under.
type publishedMachine struct {
	id    string
	login string
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
func mapRemote(local, login, projectsRoot string, entries []remotestate.Entry, now time.Time) remoteView {
	var view remoteView
	for _, entry := range entries {
		snapshot := entry.Snapshot
		// With no login to compare, the machine's name alone could be another
		// machine's, so the projects root (compared here, never emitted) must
		// be this machine's too.
		own := snapshot.Machine == local && ((login != "" && snapshot.Login == login) || (login == "" && projectsRoot != "" && snapshot.ProjectsRoot == projectsRoot))
		machineName := plainText(snapshot.Machine)
		unusable := entry.Error != "" || snapshot.PublishedAt.IsZero() || machineName == "" || strings.ContainsAny(snapshot.Machine, `/\`)
		if own || unusable {
			continue
		}
		key := snapshot.Key()
		published := snapshot.PublishedAt
		machineID := entryID(kindMachine, key)
		cached := func(id string) Entry {
			return Entry{ID: id, Machine: machineName, MachineID: machineID, Route: RouteCached, ObservedAt: published}
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
			host, repositoryName := splitForgeName(name)
			repositories = append(repositories, Repository{Entry: cached(id), Host: host, Name: plainText(repositoryName)})
		}
		var worktreeViews []Worktree
		var pullRequests []PullRequest
		for _, state := range snapshot.Worktrees {
			id := entryID(kindWorktree, key, state.Repository, state.Task, state.Branch)
			worktreeViews = append(worktreeViews, Worktree{
				Entry: cached(id), Repository: repositoryIDs[state.Repository], Name: plainText(state.Task), Task: plainText(state.Task), Stream: plainText(state.Stream),
				Branch: plainText(state.Branch), Lifecycle: publishedLifecycle(state.Lifecycle), OwnerState: publishedOwnerState(state.OwnerState), LastActivityAt: state.LastActivityAt,
			})
			if pull := state.PullRequest; pull != nil && strings.EqualFold(pull.State, "open") {
				pullRequests = append(pullRequests, PullRequest{
					Entry: cached(entryID(kindPR, key, state.Repository, strconv.Itoa(pull.Number))), Repository: repositoryIDs[state.Repository],
					Worktree: id, Branch: plainText(state.Branch), Number: pull.Number, State: "open", URL: safeHTTPSURL(pull.URL),
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
		if view.named == nil {
			view.named = map[string][]publishedMachine{}
		}
		if !slices.ContainsFunc(view.named[snapshot.Machine], func(published publishedMachine) bool { return published.id == machineID }) {
			view.named[snapshot.Machine] = append(view.named[snapshot.Machine], publishedMachine{id: machineID, login: snapshot.Login})
		}
		view.machines = append(view.machines, Machine{
			Entry: cached(machineID), WBVersion: plainText(snapshot.WBVersion),
			RepositoryCount: len(repositories), WorktreeCount: len(worktreeViews),
			OS: shortName(snapshot.OS), Arch: shortName(snapshot.Arch), CPUCount: cpuCount(snapshot.CPUCount), BootTime: publishedBootTime(snapshot.BootTime, now),
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
