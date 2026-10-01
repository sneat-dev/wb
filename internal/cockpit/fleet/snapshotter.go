package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// DefaultInterval is the refresh interval when cockpit.refresh_interval is
// not set (cockpit#req:snapshot-refresh). It was chosen from a one-off
// measurement of the production local collectors on the founder's projects
// root on 2026-10-01 (the measuring test was removed: it read a real
// directory named by the environment, which no test may do): for 417
// repositories, 490 worktrees and 3824 branches the first partial document
// took 64 ms, the whole first pass 3.1 s and a pass in which no fingerprint
// moved 0.45 s. A full pass is about 5% of a minute, inside the
// 10% budget, and the minute is the floor an interval is allowed to have.
const DefaultInterval = time.Minute

const (
	// maxWorkers caps the repositories one pass reads at once.
	maxWorkers = 8
	// defaultRepositoryTimeout bounds one repository's read, so a slow or
	// broken repository cannot stall the pass.
	defaultRepositoryTimeout = 30 * time.Second
	// sourceTimeout bounds each source that is not per repository.
	sourceTimeout = time.Minute
	// defaultStopWait is how long stopping the snapshotter waits for a read of
	// the other machines that is still running.
	defaultStopWait = 2 * time.Second
	// publishInterval is the shortest time between two publications while a
	// pass runs; the pass publishes once more when it ends.
	publishInterval = 250 * time.Millisecond
	// runningState is the agent run state that counts as an active agent.
	runningState = "running"
)

// defaultWorkers sizes the worker pool from the CPU count, capped.
func defaultWorkers() int { return max(1, min(runtime.NumCPU(), maxWorkers)) }

// ErrUnknownRepository is returned by RefreshRepository for an id the last
// scan did not produce.
var ErrUnknownRepository = errors.New("unknown repository")

// errPanicked is what a collector that panicked is reported as.
var errPanicked = errors.New("a collector panicked")

// catch runs work and turns a panic in it into an error, so one collector's
// panic marks its repository or source and never ends the daemon. The error
// names the type of what was panicked with and nothing of its text, which could
// carry a path.
func catch(work func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w (%T)", errPanicked, recovered)
		}
	}()
	return work()
}

// defaultBranchOf is the default branch of a repository: the one its listing
// names, else the one Git resolved. The document and the README route both use
// it.
func defaultBranchOf(repo discover.Repo, resolved string) string {
	return firstNonEmpty(repo.DefaultBranch, resolved)
}

// ErrorGitTooOld is the document's error code when Git is older than the
// oldest version that is safe to run against a repository.
const ErrorGitTooOld = "git_too_old"

// Options configures a Snapshotter.
type Options struct {
	// Machine is this machine's name and Version its WB version. Login is the
	// login this machine publishes its own snapshot under, when known: the
	// machine's own publication is skipped by login and name together, and by
	// name alone while Login is empty.
	Machine string
	Version string
	Login   string
	// ProjectsRoot is this machine's projects root, which is compared (never
	// emitted) with a snapshot's to recognise this machine's own publication
	// when Login is not known.
	ProjectsRoot string
	// Collectors are the sources; a nil Remote means no other machines.
	Collectors Collectors
	// Interval is the refresh interval; zero or less means DefaultInterval.
	Interval time.Duration
	// Workers bounds the repositories read at once; zero or less means the
	// CPU count, at most eight.
	Workers int
	// RepositoryTimeout bounds one repository's read; zero or less means 30s.
	RepositoryTimeout time.Duration
	// StopWait bounds how long stopping waits for a read of the other
	// machines; zero or less means two seconds.
	StopWait time.Duration
	// ProviderTimeout bounds one code-index provider ask and ProviderBudget all
	// the asks of one repository's read; zero or less means 20 s and 2 min.
	ProviderTimeout time.Duration
	ProviderBudget  time.Duration
	// Hardware is this machine's hardware facts for its machine entry; the zero
	// value omits them. The daemon passes LocalHardware().
	Hardware Hardware
	// Compress compresses a stored body; nil means cockpit.Gzip. It runs once
	// for each snapshot stored and once for each repository's branch list that
	// is first asked for after a change, never per request; a test counts it.
	Compress func([]byte) []byte
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Tick delivers the refresh ticks for an interval and a function that
	// stops them; nil means a time.Ticker. A test supplies its own.
	Tick func(interval time.Duration) (<-chan time.Time, func())
	// Fingerprint computes a clone's fingerprint; nil means Fingerprint.
	Fingerprint func(checkout string) (string, error)
	// Logf reports a refresh that failed, in whole or in part; nil discards.
	Logf func(format string, args ...any)
}

// repoState is what the snapshotter keeps of one local repository between
// refreshes: the clone, the Git state it was last read at (its fingerprint,
// worktrees, branches and default branch), and what it contributed to the
// document then. Every read takes a ticket when it starts, and only the read
// holding the newest ticket seen so far may store its result, so an older read
// that finishes late never overwrites a newer one.
type repoState struct {
	repo          discover.Repo
	fingerprint   string
	gitRead       bool
	linked        []LinkedWorktree
	refs          []BranchRef
	defaultBranch string
	scanned       bool
	identity      string
	indexKey      string
	indexed       bool
	codeIndex     localCodeIndex
	ticket        int
	applied       int
	passPrint     string
	entries       repoEntries
	// branches is the branch list's body prepared for serving, built on the
	// first request after entries changed and dropped when they change again.
	branches *cockpit.Payload
}

// Snapshotter builds the fleet document in the background and holds the last
// one. A request reads Document or Body and nothing else: it never reaches a
// collector, so it never waits for Git (cockpit#req:no-fleet-scan-on-the-
// request-path).
type Snapshotter struct {
	machine           string
	version           string
	login             string
	projectsRoot      string
	hardware          Hardware
	compress          func([]byte) []byte
	collectors        Collectors
	interval          time.Duration
	workers           int
	repositoryTimeout time.Duration
	stopWait          time.Duration
	providerTimeout   time.Duration
	providerBudget    time.Duration
	now               func() time.Time
	tick              func(time.Duration) (<-chan time.Time, func())
	fingerprint       func(string) (string, error)
	logf              func(string, ...any)

	// refresh serialises full passes. side counts the read of the other
	// machines, which outlives the pass that started it, and remoteBusy keeps
	// at most one such read running. mu guards everything below.
	refresh    sync.Mutex
	side       sync.WaitGroup
	remoteBusy atomic.Bool
	mu         sync.RWMutex

	doc         Document
	payload     cockpit.Payload
	complete    bool
	passing     bool
	listError   string
	gitChecked  bool
	gitOld      bool
	lastPublish time.Time
	publishes   int
	repos       map[string]*repoState
	agents      []agentRecord
	truncated   bool
	bindings    []worktrees.RegisteredPullRequestBinding
	boundAt     time.Time
	remote      remoteView
	// remoteBranches holds the prepared empty branch lists of the repositories
	// cached from other machines, dropped whenever remote is replaced.
	remoteBranches map[string]cockpit.Payload
	codePass       CodeIndexPass
	codeErr        string
}

// New builds a Snapshotter that has taken no snapshot: Document is the empty
// warming-up document until the first repository completes.
func New(options Options) *Snapshotter {
	snapshotter := &Snapshotter{
		machine: options.Machine, version: options.Version, login: options.Login, projectsRoot: options.ProjectsRoot, hardware: options.Hardware, compress: options.Compress, collectors: options.Collectors,
		interval: options.Interval, workers: options.Workers, repositoryTimeout: options.RepositoryTimeout,
		stopWait: options.StopWait, providerTimeout: options.ProviderTimeout, providerBudget: options.ProviderBudget, now: options.Now, tick: options.Tick,
		fingerprint: options.Fingerprint, logf: options.Logf,
		repos: map[string]*repoState{},
	}
	if snapshotter.interval <= 0 {
		snapshotter.interval = DefaultInterval
	}
	if snapshotter.workers <= 0 {
		snapshotter.workers = defaultWorkers()
	}
	if snapshotter.repositoryTimeout <= 0 {
		snapshotter.repositoryTimeout = defaultRepositoryTimeout
	}
	if snapshotter.stopWait <= 0 {
		snapshotter.stopWait = defaultStopWait
	}
	if snapshotter.providerTimeout <= 0 {
		snapshotter.providerTimeout = defaultProviderTimeout
	}
	if snapshotter.providerBudget <= 0 {
		snapshotter.providerBudget = defaultProviderBudget
	}
	if snapshotter.compress == nil {
		snapshotter.compress = cockpit.Gzip
	}
	if snapshotter.now == nil {
		snapshotter.now = time.Now
	}
	if snapshotter.tick == nil {
		snapshotter.tick = tickEvery
	}
	if snapshotter.fingerprint == nil {
		snapshotter.fingerprint = Fingerprint
	}
	if snapshotter.logf == nil {
		snapshotter.logf = func(string, ...any) {}
	}
	snapshotter.store(emptyDocument(snapshotter.interval))
	return snapshotter
}

// tickEvery is the production tick source.
func tickEvery(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// store makes document the published one and prepares its body once: the JSON,
// its gzip encoding and their strong ETags, so a request copies bytes and runs
// neither an encoder nor a compressor (cockpit-views#req:compressed-responses).
// The document types cannot fail to marshal.
func (s *Snapshotter) store(document Document) {
	body, _ := json.Marshal(document)
	s.doc, s.payload = document, cockpit.NewPayload(append(body, '\n'), s.compress)
}

// Payload returns the last published document prepared for serving.
func (s *Snapshotter) Payload() cockpit.Payload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.payload
}

// Body returns the last published document as marshalled JSON, and its strong
// ETag.
func (s *Snapshotter) Body() (body []byte, etag string) {
	return s.Payload().Identity()
}

// Branches returns the branch list of the local repository with id, prepared for
// serving, from the last scan and without running anything (cockpit-views#req:
// lazy-branches-route). A repository cached from another machine is known and has no
// branches here: the answer is an empty list and its reason. An id that is
// neither is not found.
func (s *Snapshotter) Branches(id string) (payload cockpit.Payload, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, local := s.repos[id]; local && state.scanned {
		if state.branches == nil {
			branches := slices.Clone(state.entries.branches)
			sortByName(branches, func(item Branch) string { return item.Name }, func(item Branch) string { return item.ID })
			state.branches = s.branchesPayload(BranchesResponse{Repository: id, Branches: branches})
		}
		return *state.branches, true
	}
	if payload, held := s.remoteBranches[id]; held {
		return payload, true
	}
	for _, repository := range s.remote.repositories {
		if repository.ID == id {
			payload = *s.branchesPayload(BranchesResponse{Repository: id, Reason: ReasonCachedRepository})
			if s.remoteBranches == nil {
				s.remoteBranches = map[string]cockpit.Payload{}
			}
			s.remoteBranches[id] = payload
			return payload, true
		}
	}
	return cockpit.Payload{}, false
}

// branchesPayload marshals response, with a list that is never null.
func (s *Snapshotter) branchesPayload(response BranchesResponse) *cockpit.Payload {
	if response.Branches == nil {
		response.Branches = []Branch{}
	}
	body, _ := json.Marshal(response)
	payload := cockpit.NewPayload(append(body, '\n'), s.compress)
	return &payload
}

// readmeTarget is the repository with id and its default branch, which is empty
// until the repository's Git state has been read.
func (s *Snapshotter) readmeTarget(id string) (repo discover.Repo, branch string, found bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, found := s.repos[id]
	if !found {
		return discover.Repo{}, "", false
	}
	return state.repo, defaultBranchOf(state.repo, state.defaultBranch), true
}

// gitTooOld reports whether Git was found too old to run.
func (s *Snapshotter) gitTooOld() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gitOld
}

// Start refreshes now and then on every interval until the returned function
// is called or ctx ends. The function stops the loop, waits for it, and then
// waits a short, bounded time for a read of the other machines still running;
// no repository read outlives it.
func (s *Snapshotter) Start(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	return func() {
		cancel()
		<-done
		waited := make(chan struct{})
		go func() {
			s.side.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(s.stopWait):
		}
	}
}

func (s *Snapshotter) run(ctx context.Context) {
	ticks, stopTicks := s.tick(s.interval)
	defer stopTicks()
	for {
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.logf("cockpit fleet refresh: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// Refresh runs one pass. The agents and the pull-request records are read
// beside it, every repository is read by a bounded worker pool, and the
// document is republished as repositories complete (at most every
// publishInterval), so it is readable while the first pass runs. A
// repository's Git state is read only when its fingerprint moved; its
// worktrees' manifests and heartbeats, which live outside Git, are read every
// pass. A repository or source that fails keeps what it last contributed, a
// repository carrying an error code, and the failures come back joined. The
// other machines' snapshots are read by their own goroutine, which neither
// delays the pass nor ends the warm-up. If the repositories cannot be listed
// the document says so in its error field and the agents and other machines
// are still read. The first full pass ends the warm-up; a cancelled pass does
// not.
func (s *Snapshotter) Refresh(ctx context.Context) error {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	s.checkGit(ctx)
	var discovered []discover.Repo
	listErr := catch(func() (err error) {
		discovered, err = s.collectors.Repositories.Repositories(ctx)
		return err
	})
	s.startRemote(ctx)
	if listErr != nil {
		failures := s.refreshMachineState(ctx)
		s.mu.Lock()
		s.listError = ErrorRepositoriesUnreadable
		s.publishLocked()
		s.mu.Unlock()
		return errors.Join(append(failures, fmt.Errorf("list repositories: %w", listErr))...)
	}
	s.beginCodeIndex()
	ids := s.track(discovered)
	var machineFailures []error
	var beside sync.WaitGroup
	beside.Add(1)
	go func() {
		defer beside.Done()
		machineFailures = s.refreshMachineState(ctx)
	}()
	failures := s.forEach(ctx, ids, s.scanOne)
	beside.Wait()
	s.mu.Lock()
	s.passing = false
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.complete = true
	s.publishLocked()
	return errors.Join(append(failures, machineFailures...)...)
}

// checkGit reads the Git version once, the first time a pass runs with a live
// context, and records whether Git is too old (or unreadable) to run. Until it
// has answered Git is assumed usable only by the pass that asked.
func (s *Snapshotter) checkGit(ctx context.Context) {
	if s.gitChecked || s.collectors.Git == nil || ctx.Err() != nil {
		return
	}
	usable := false
	checkCtx, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	if catch(func() error { usable = s.collectors.Git.GitUsable(checkCtx); return nil }) != nil || !usable {
		s.logf("cockpit fleet: Git is too old or unreadable; Git-backed reads are off (%s)", ErrorGitTooOld)
		s.mu.Lock()
		s.gitOld = true
		s.mu.Unlock()
	}
	s.gitChecked = true
}

// beginCodeIndex reads the indexer receipts for a pass and keeps the result
// for the repositories it reads. With no collector, or with Git too old to
// compare a receipt with HEAD, or when the receipts cannot be read, the pass
// is nil and no entry carries a code index; a failure is logged when it
// changes, not on every pass. It returns the pass.
func (s *Snapshotter) beginCodeIndex() CodeIndexPass {
	var pass CodeIndexPass
	failure := ""
	if s.collectors.CodeIndex != nil && !s.gitTooOld() {
		if err := catch(func() (err error) {
			pass, err = s.collectors.CodeIndex.Begin()
			return err
		}); err != nil {
			pass, failure = nil, err.Error()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if failure != s.codeErr && failure != "" {
		s.logf("cockpit fleet: code-index freshness is off: %s", failure)
	}
	s.codePass, s.codeErr = pass, failure
	return pass
}

// codeIndexPassForRead is the pass a repository read uses: the one the
// running pass began, or a fresh one for a read outside a pass.
func (s *Snapshotter) codeIndexPassForRead() CodeIndexPass {
	s.mu.RLock()
	pass, passing := s.codePass, s.passing
	s.mu.RUnlock()
	if passing {
		return pass
	}
	return s.beginCodeIndex()
}

// hostOfIdentity is the forge host of a host/owner/name identity, empty for
// none.
func hostOfIdentity(identity string) string {
	host, _, _ := strings.Cut(identity, "/")
	return host
}

// codeIndexOf is the code-index state of a repository's checkouts. Its Git
// state is read again only when Git state was just read or the receipts, the
// queue or the checkouts for it are not what the held state was computed
// from, or a provider answer is still owed; otherwise the held state is reused
// and no Git command runs. ctx is the repository's Git budget and parent the
// pass's context, from which the provider's own budget is derived.
func (s *Snapshotter) codeIndexOf(ctx, parent context.Context, held repoState, recorded []recordedWorktree, gitRead bool) (key string, indexed bool, index localCodeIndex) {
	pass := s.codeIndexPassForRead()
	if pass == nil {
		return "", false, localCodeIndex{}
	}
	identity := held.repo.Identity()
	if s.collectors.Identity != nil {
		// The worker names a repository by its origin, whatever the layout, so
		// a clone whose origin names no forge has no indexer.
		if identity = held.identity; identity == "" {
			return "", false, localCodeIndex{}
		}
	}
	paths := []string{held.repo.Path}
	for _, item := range recorded {
		paths = append(paths, item.linked.Path)
	}
	key = pass.Key(identity, paths)
	if !gitRead && held.indexed && key == held.indexKey && !held.codeIndex.retry {
		return key, true, held.codeIndex
	}
	var states map[string][]CodeIndex
	if err := catch(func() error { states, indexed = pass.States(ctx, identity, paths); return nil }); err != nil {
		return "", false, localCodeIndex{}
	}
	asked, retry := s.attachStatistics(parent, states, held.codeIndex.asked)
	// The receipt key has done its work; the held states carry only what the
	// document does.
	for _, items := range states {
		for index := range items {
			items[index].receiptKey = ""
		}
	}
	return key, indexed, localCodeIndex{repository: states[held.repo.Path], byPath: states, asked: asked, retry: retry}
}

// attachStatistics puts the provider's statistics on the code-index entry of
// the indexer the provider follows, for each checkout in states, and returns
// what was answered and whether an answer is still owed.
//
// Only a checkout the indexer has a receipt for is asked: the provider's
// command opens the index read-write and may run Git and scan the tree, which
// the snapshotter's read-only rule forbids for a checkout WB's hook never
// indexed (it could hold an index a hostile repository committed). Such a
// checkout is reported as not indexed and no process starts.
//
// A checkout whose receipt (time, commit and status) is the one the held answer
// was for keeps that answer, so the provider is asked once per receipt, here
// inside the fingerprint-gated read and never on a request. A failed answer is
// asked again on later passes, at most maxProviderAttempts per receipt, then
// the code stays. An ask that never ran or was cut short by the budget ending
// records nothing. A checkout no longer in states drops out of the answers.
func (s *Snapshotter) attachStatistics(parent context.Context, states map[string][]CodeIndex, held map[string]askedStatistics) (asked map[string]askedStatistics, retry bool) {
	provider := s.collectors.CodeIndexProvider
	if provider == nil {
		return nil, false
	}
	budget, cancel := context.WithTimeout(parent, s.providerBudget)
	defer cancel()
	asked = map[string]askedStatistics{}
	for path, items := range states {
		for index := range items {
			item := &items[index]
			if item.Indexer != provider.Indexer() {
				continue
			}
			if item.ReceiptAt.IsZero() {
				item.Statistics = &CodeStatistics{Kinds: []KindCount{}}
				continue
			}
			previous, found := held[path]
			same := found && previous.receipt.Equal(item.ReceiptAt) && previous.key == item.receiptKey
			if !same || (!previous.final && previous.attempts < maxProviderAttempts) {
				stats, answered := CodeStatistics{}, false
				if budget.Err() == nil {
					stats, answered = askProvider(budget, provider, path, s.providerTimeout)
				}
				switch {
				case answered:
					attempts := 1
					if same {
						attempts = previous.attempts + 1
					}
					previous = askedStatistics{receipt: item.ReceiptAt, key: item.receiptKey, stats: stats, attempts: attempts, final: stats.Error == ""}
					retry = retry || (!previous.final && attempts < maxProviderAttempts)
				case same:
					retry = true
				default:
					retry = true
					continue
				}
			}
			asked[path] = previous
			statistics := previous.stats
			item.Statistics = &statistics
		}
	}
	return asked, retry
}

// track makes the repositories discovered the ones the snapshotter holds,
// keeping what it knows of those it already had and forgetting the rest, and
// returns their ids in order. It starts a pass.
func (s *Snapshotter) track(discovered []discover.Repo) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]*repoState, len(discovered))
	for _, repo := range discovered {
		if repo.Path == "" {
			continue
		}
		id := localRepositoryID(s.machine, repo)
		state := s.repos[id]
		if state == nil {
			state = &repoState{}
		}
		state.repo = repo
		next[id] = state
	}
	s.repos = next
	s.listError, s.passing = "", true
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// forEach runs work for every id with at most s.workers at a time and returns
// the errors. It starts no new work once ctx ends. A panic in work is recovered
// and recorded on the repository, never propagated.
func (s *Snapshotter) forEach(ctx context.Context, ids []string, work func(context.Context, string) error) []error {
	var (
		group    sync.WaitGroup
		failures []error
		recorded sync.Mutex
	)
	slots := make(chan struct{}, s.workers)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			err := catch(func() error { return work(ctx, id) })
			if errors.Is(err, errPanicked) {
				s.markFailed(id)
			}
			if err != nil {
				recorded.Lock()
				failures = append(failures, err)
				recorded.Unlock()
			}
		}()
	}
	group.Wait()
	return failures
}

// markFailed records a repository whose read panicked outside a collector
// with an error code, keeping what it last held.
func (s *Snapshotter) markFailed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, found := s.repos[id]; found {
		if state.scanned {
			state.entries.repository.Error = ErrorReadFailed
		} else {
			state.entries = mapLocalRepository(s.machine, state.repo, "", nil, nil, ErrorReadFailed, localCodeIndex{}, "", s.now())
			state.branches = nil
		}
		state.scanned = true
	}
}

// fingerprintOf is a clone's fingerprint, empty when it cannot be computed.
func (s *Snapshotter) fingerprintOf(path string) string {
	fingerprint, err := s.fingerprint(path)
	if err != nil {
		return ""
	}
	return fingerprint
}

// scanOne reads one repository as part of a pass. Its fingerprint is computed
// once, here. A repository whose Git state moved is read through
// RefreshRepository, which is handed that fingerprint; one whose Git state is
// unchanged costs only its worktrees' records.
func (s *Snapshotter) scanOne(ctx context.Context, id string) error {
	s.mu.RLock()
	state := s.repos[id]
	path, last, read := state.repo.Path, state.fingerprint, state.gitRead
	s.mu.RUnlock()
	current := s.fingerprintOf(path)
	s.mu.Lock()
	state.passPrint = current
	s.mu.Unlock()
	if !read || current == "" || current != last {
		return s.RefreshRepository(ctx, id)
	}
	err := s.scanRepository(ctx, id, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishMaybeLocked()
	return err
}

// RefreshRepository reads one repository now, its Git state included whatever
// its fingerprint, and republishes the document (at once outside a pass, within
// the publication rate during one), so the daemon can reflect a finished
// operation without waiting for the interval. A repository that could not be
// read is recorded with an error code, shown in the republished document, and
// its failure is returned.
func (s *Snapshotter) RefreshRepository(ctx context.Context, id string) error {
	err := s.scanRepository(ctx, id, true)
	if errors.Is(err, ErrUnknownRepository) {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishMaybeLocked()
	return err
}

// scanRepository reads one repository under its own timeout and takes a ticket
// for it. Its Git state is read when force is set, when it has never been read,
// or when its fingerprint is not the one it was read at; otherwise the Git
// state held is reused and no Git work runs. Its worktrees' records are read in
// every case. A failure, a timeout or a panic in a collector is recorded as an
// error code on the repository, which keeps its last Git state, and returned
// for the log; the code carries no path or command output.
func (s *Snapshotter) scanRepository(parent context.Context, id string, force bool) error {
	s.mu.Lock()
	state, found := s.repos[id]
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownRepository, id)
	}
	state.ticket++
	ticket, held, current := state.ticket, *state, state.passPrint
	state.passPrint = ""
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, s.repositoryTimeout)
	defer cancel()
	// The fingerprint is taken before the read, so a change made during the
	// read is seen by the next pass instead of being recorded as read.
	if current == "" {
		current = s.fingerprintOf(held.repo.Path)
	}
	var readErr error
	gitRead := false
	if force || !held.gitRead || current == "" || current != held.fingerprint {
		var linked []LinkedWorktree
		var refs []BranchRef
		var defaultBranch, identity string
		readErr = catch(func() (err error) {
			if linked, err = s.collectors.Worktrees.Worktrees(ctx, held.repo); err != nil {
				return err
			}
			if s.gitTooOld() {
				return nil
			}
			if refs, err = s.collectors.Branches.Branches(ctx, held.repo); err != nil {
				return err
			}
			defaultBranch = s.collectors.Branches.DefaultBranch(ctx, held.repo)
			if s.collectors.Identity != nil {
				identity, err = s.collectors.Identity.Identity(ctx, held.repo)
			}
			return err
		})
		if readErr == nil {
			held.linked, held.refs, held.defaultBranch, held.identity, held.fingerprint, held.gitRead = linked, refs, defaultBranch, identity, current, true
			gitRead = true
		}
	}
	if err := parent.Err(); err != nil {
		return err
	}
	var recorded []recordedWorktree
	if recordErr := catch(func() error {
		for _, linked := range held.linked {
			if record, ok := s.collectors.Records.Record(linked.Path); ok {
				recorded = append(recorded, recordedWorktree{linked: linked, record: record})
			}
		}
		return nil
	}); readErr == nil {
		readErr = recordErr
	}
	indexKey, indexed, codeIndex := s.codeIndexOf(ctx, parent, held, recorded, gitRead)
	errCode := ""
	if readErr != nil {
		errCode = ErrorReadFailed
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			errCode = ErrorTimeout
		}
	}
	entries := mapLocalRepository(s.machine, held.repo, defaultBranchOf(held.repo, held.defaultBranch), recorded, held.refs, errCode, codeIndex, hostOfIdentity(held.identity), s.now())
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, found := s.repos[id]; found && ticket > state.applied {
		state.applied = ticket
		state.fingerprint, state.gitRead, state.linked, state.refs, state.defaultBranch, state.identity = held.fingerprint, held.gitRead, held.linked, held.refs, held.defaultBranch, held.identity
		state.scanned, state.entries, state.branches = true, entries, nil
		state.indexKey, state.indexed, state.codeIndex = indexKey, indexed, codeIndex
	}
	if errCode != "" {
		return fmt.Errorf("%s: %s", held.repo.Slug(), errCode)
	}
	return nil
}

// refreshMachineState re-reads what is not per repository and is local: the
// agents on this machine and the pull requests recorded locally. Each source
// has its own timeout, a panic in it is a failure of that source, and one that
// fails keeps its last value.
func (s *Snapshotter) refreshMachineState(parent context.Context) []error {
	ctx, cancel := context.WithTimeout(parent, sourceTimeout)
	defer cancel()
	var failures []error
	at := s.now()
	var sessions []session.View
	var runs []agents.Result
	if err := catch(func() (err error) {
		if sessions, err = s.collectors.Sessions.Sessions(ctx); err != nil {
			return err
		}
		runs, err = s.collectors.Runs.Runs(ctx)
		return err
	}); err != nil {
		failures = append(failures, fmt.Errorf("read agents: %w", err))
	} else {
		mapped, truncated := mapAgents(s.machine, sessions, runs, at)
		s.mu.Lock()
		s.agents, s.truncated = mapped, truncated
		s.mu.Unlock()
	}
	var bindings []worktrees.RegisteredPullRequestBinding
	if err := catch(func() (err error) {
		bindings, err = s.collectors.PullRequests.PullRequests(ctx)
		return err
	}); err != nil {
		failures = append(failures, fmt.Errorf("read pull request records: %w", err))
	} else {
		s.mu.Lock()
		s.bindings, s.boundAt = bindings, at
		s.mu.Unlock()
	}
	return failures
}

// startRemote reads the other machines' snapshots in a goroutine of its own,
// under its own timeout, unless a read is still running. The pass does not wait
// for it: its result is published when it arrives, and a failure is only
// logged.
func (s *Snapshotter) startRemote(ctx context.Context) {
	if s.collectors.Remote == nil || !s.remoteBusy.CompareAndSwap(false, true) {
		return
	}
	s.side.Add(1)
	go func() {
		defer s.side.Done()
		defer s.remoteBusy.Store(false)
		ctx, cancel := context.WithTimeout(ctx, sourceTimeout)
		defer cancel()
		var entries []remotestate.Entry
		if err := catch(func() (err error) {
			entries, err = s.collectors.Remote.Machines(ctx)
			return err
		}); err != nil {
			s.logf("cockpit fleet: read other machines: %v", err)
			return
		}
		view := mapRemote(s.machine, s.login, s.projectsRoot, entries)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.remote, s.remoteBranches = view, nil
		s.publishMaybeLocked()
	}()
}

// publishMaybeLocked publishes unless a pass is running and the last
// publication was less than publishInterval ago; the pass publishes once more
// when it ends. The caller holds s.mu.
func (s *Snapshotter) publishMaybeLocked() {
	if s.passing && s.publishes > 0 && s.now().Sub(s.lastPublish) < publishInterval {
		return
	}
	s.publishLocked()
}

// publishLocked rebuilds the document from what the snapshotter holds: the
// repositories scanned so far, the agents, the pull-request records and the
// other machines. The caller holds s.mu.
func (s *Snapshotter) publishLocked() {
	now := s.now()
	document := emptyDocument(s.interval)
	document.WarmingUp, document.SnapshotAt, document.Error = !s.complete, now, s.listError
	if s.gitOld && document.Error == "" {
		document.Error = ErrorGitTooOld
	}
	document.RepositoriesTotal = len(s.repos)
	if provider := s.collectors.CodeIndexProvider; provider != nil {
		document.CodeIndexProvider = provider.Name()
	}
	document.AgentsTruncated = s.truncated
	ids := make([]string, 0, len(s.repos))
	idsBySlug := map[string][]string{}
	for id, state := range s.repos {
		idsBySlug[state.repo.Slug()] = append(idsBySlug[state.repo.Slug()], id)
		if state.scanned {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	document.RepositoriesScanned = len(ids)
	worktreesOf := map[string][]Worktree{}
	for _, id := range ids {
		worktreesOf[id] = s.repos[id].entries.worktrees
	}
	pullRequests, diagnostics := mapPullRequests(s.machine, s.bindings, s.boundAt, idsBySlug, worktreesOf)
	document.Diagnostics = diagnostics
	activeOf := map[string]int{}
	for _, record := range s.agents {
		if owners := idsBySlug[record.slug]; record.slug != "" && record.agent.State == runningState && len(owners) == 1 {
			activeOf[owners[0]]++
		}
	}
	for _, id := range ids {
		entries := s.repos[id].entries
		repository := entries.repository
		localBranches := countWhere(entries.branches, func(branch Branch) bool { return branch.Scope == BranchLocal })
		remoteBranches := countWhere(entries.branches, func(branch Branch) bool { return branch.Scope == BranchRemote })
		active := activeOf[id]
		repository.WorktreeCount = len(entries.worktrees)
		repository.LocalBranchCount, repository.RemoteBranchCount, repository.ActiveAgentCount = &localBranches, &remoteBranches, &active
		document.Repositories = append(document.Repositories, repository)
		document.Worktrees = append(document.Worktrees, entries.worktrees...)
	}
	document.PullRequests = append(document.PullRequests, pullRequests...)
	for _, record := range s.agents {
		agent := record.agent
		if owners := idsBySlug[record.slug]; len(owners) == 1 {
			agent.Repository = owners[0]
		}
		document.Agents = append(document.Agents, agent)
	}
	document.Machines = append(document.Machines, Machine{
		Entry:     localEntry(localMachineID(s.machine), s.machine, now),
		WBVersion: s.version, RepositoryCount: len(document.Repositories), WorktreeCount: len(document.Worktrees),
		OS: s.hardware.OS, Arch: s.hardware.Arch, CPUCount: s.hardware.CPUCount, BootTime: s.hardware.BootTime,
	})
	document.Machines = append(document.Machines, s.remote.machines...)
	document.Repositories = append(document.Repositories, s.remote.repositories...)
	document.Worktrees = append(document.Worktrees, s.remote.worktrees...)
	document.PullRequests = append(document.PullRequests, s.remote.pullRequests...)
	sortByName(document.Machines, func(item Machine) string { return item.Machine }, func(item Machine) string { return item.ID })
	sortByName(document.Repositories, func(item Repository) string { return item.Name }, func(item Repository) string { return item.ID })
	sortByName(document.Worktrees, func(item Worktree) string { return item.Task }, func(item Worktree) string { return item.ID })
	sortByName(document.PullRequests, func(item PullRequest) string { return fmt.Sprintf("%09d", item.Number) }, func(item PullRequest) string { return item.ID })
	sortByName(document.Agents, func(item Agent) string { return item.Kind }, func(item Agent) string { return item.ID })
	s.store(document)
	s.lastPublish, s.publishes = now, s.publishes+1
}
