package repositoryevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/runqueue"
)

const queueSchema = 1

type job struct {
	Schema      int                   `json:"schema"`
	Sequence    uint64                `json:"sequence"`
	Event       repositoryevent.Event `json:"event"`
	EventDigest string                `json:"event_digest"`
	State       string                `json:"state"`
	Attempts    int                   `json:"attempts"`
	Progress    string                `json:"progress,omitempty"`
	Error       string                `json:"error,omitempty"`
	QueuedAt    time.Time             `json:"queued_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	RetryAt     time.Time             `json:"retry_at,omitempty"`
	Supersedes  []string              `json:"supersedes,omitempty"`
}

type Processor interface {
	Process(context.Context, repositoryevent.Event) (string, error)
}

type Queue struct {
	mu            sync.Mutex
	root          string
	directory     string
	jobs          map[string]*job
	changed       chan struct{}
	now           func() time.Time
	progressEvery time.Duration
	retryDelay    time.Duration
	progress      func(string)
	workers       int
	activeRepos   map[string]bool
	acquire       func(context.Context) (func(), error)
	nextSequence  uint64
}

func NewQueue(projectsRoot string) (*Queue, error) {
	directory := filepath.Join(projectsRoot, ".wb", "runtime", "daemon", "repository-events", "jobs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create repository event queue: %w", err)
	}
	queue := &Queue{root: projectsRoot, directory: directory, jobs: map[string]*job{}, changed: make(chan struct{}), now: func() time.Time { return time.Now().UTC() }, progressEvery: 10 * time.Second, retryDelay: 30 * time.Second, workers: runqueue.Budget(), activeRepos: map[string]bool{}}
	queue.acquire = func(ctx context.Context) (func(), error) {
		lease, _, err := runqueue.Acquire(ctx, queue.root, 1, runqueue.Budget())
		if err != nil {
			return nil, err
		}
		return lease.Release, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	seenSequence := make(map[uint64]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item job
		if err := json.Unmarshal(raw, &item); err != nil || validateJobRecord(item) != nil {
			return nil, fmt.Errorf("repository event queue record %s is invalid", entry.Name())
		}
		if seenSequence[item.Sequence] {
			return nil, fmt.Errorf("repository event queue record %s duplicates sequence %d", entry.Name(), item.Sequence)
		}
		seenSequence[item.Sequence] = true
		if item.Sequence >= queue.nextSequence {
			queue.nextSequence = item.Sequence + 1
		}
		if item.State == "running" {
			item.State = "queued"
			item.Progress = "resumed after daemon handoff"
			item.UpdatedAt = queue.now()
			if err := queue.persist(&item); err != nil {
				return nil, err
			}
		}
		queue.jobs[item.Event.ID] = &item
	}
	if err := queue.reconcileSuperseded(); err != nil {
		return nil, err
	}
	return queue, nil
}

func validateJobRecord(item job) error {
	if item.Schema != queueSchema || item.Sequence == 0 || item.Event.Validate() != nil || item.EventDigest != eventDigest(item.Event) || item.QueuedAt.IsZero() || item.UpdatedAt.IsZero() {
		return errors.New("invalid repository event queue record")
	}
	switch item.State {
	case "queued", "running", "succeeded", "superseded":
	default:
		return errors.New("invalid repository event queue state")
	}
	if len(item.Supersedes) > repositoryevent.MaxLimit || (len(item.Supersedes) > 0 && item.Event.Reason != repositoryevent.ReasonDefaultBranchUpdated) {
		return errors.New("invalid repository event supersession")
	}
	for _, id := range item.Supersedes {
		if repositoryevent.ValidateEventID(id) != nil || id == item.Event.ID {
			return errors.New("invalid repository event supersession")
		}
	}
	return nil
}

func (queue *Queue) Enqueue(_ context.Context, event repositoryevent.Event) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	digest := eventDigest(event)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if existing := queue.jobs[event.ID]; existing != nil {
		if existing.EventDigest != digest {
			return "", errors.New("repository event id belongs to a different payload")
		}
		if err := queue.applySupersedes(existing); err != nil {
			return "", err
		}
		return event.ID, nil
	}
	now := queue.now()
	if queue.nextSequence == 0 {
		queue.nextSequence = 1
	}
	item := &job{Schema: queueSchema, Sequence: queue.nextSequence, Event: event, EventDigest: digest, State: "queued", QueuedAt: now, UpdatedAt: now}
	queue.nextSequence++
	if event.Reason == repositoryevent.ReasonDefaultBranchUpdated {
		renameBarrier := queue.lastRenameSequence(event.Repository)
		for _, candidate := range queue.jobs {
			if candidate.State == "queued" && candidate.Event.Reason == repositoryevent.ReasonDefaultBranchUpdated &&
				candidate.Sequence > renameBarrier && normalizeRepository(candidate.Event.Repository) == normalizeRepository(event.Repository) && candidate.Event.Ref == event.Ref {
				item.Supersedes = append(item.Supersedes, candidate.Event.ID)
			}
		}
		sort.Strings(item.Supersedes)
	}
	if err := queue.persist(item); err != nil {
		return "", err
	}
	queue.jobs[event.ID] = item
	if err := queue.applySupersedes(item); err != nil {
		return "", err
	}
	queue.notifyLocked()
	return event.ID, nil
}

func (queue *Queue) lastRenameSequence(repository string) uint64 {
	normalized := normalizeRepository(repository)
	var sequence uint64
	for _, candidate := range queue.jobs {
		if candidate.Event.Reason != repositoryevent.ReasonRepositoryRenamed || candidate.Sequence <= sequence {
			continue
		}
		for _, alias := range repositoryAliases(candidate.Event) {
			if alias == normalized {
				sequence = candidate.Sequence
				break
			}
		}
	}
	return sequence
}

func (queue *Queue) Run(ctx context.Context, processor Processor, progress func(string)) {
	if processor == nil {
		return
	}
	queue.progress = progress
	workers := queue.workers
	if workers < 1 {
		workers = 1
	}
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer group.Done()
			queue.runWorker(ctx, processor)
		}()
	}
	group.Wait()
}

func (queue *Queue) runWorker(ctx context.Context, processor Processor) {
	for ctx.Err() == nil {
		item := queue.claimNext()
		if item == nil {
			queue.mu.Lock()
			changed := queue.changed
			queue.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			case <-time.After(queue.retryDelay):
				continue
			}
		}
		queue.runClaimed(ctx, processor, item.Event.ID)
	}
}

func (queue *Queue) claimNext() *job {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	items := make([]*job, 0)
	for _, item := range queue.jobs {
		if item.State == "queued" {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Sequence < items[j].Sequence
	})
	blockedAliases := map[string]bool{}
	var item *job
	for _, candidate := range items {
		aliases := repositoryAliases(candidate.Event)
		if candidate.RetryAt.After(queue.now()) || aliasesOverlap(queue.activeRepos, aliases) || aliasesOverlap(blockedAliases, aliases) {
			for _, alias := range aliases {
				blockedAliases[alias] = true
			}
			continue
		}
		item = candidate
		break
	}
	if item == nil {
		return nil
	}
	item.State = "running"
	item.Attempts++
	item.Progress = "waiting for sync admission for " + item.Event.Repository
	item.UpdatedAt = queue.now()
	if err := queue.persist(item); err != nil {
		item.State = "queued"
		item.Attempts--
		item.Error = err.Error()
		return nil
	}
	for _, alias := range repositoryAliases(item.Event) {
		queue.activeRepos[alias] = true
	}
	copy := *item
	return &copy
}

func (queue *Queue) runClaimed(ctx context.Context, processor Processor, id string) {
	done := make(chan struct{})
	go queue.heartbeat(ctx, id, done)
	defer close(done)
	release, err := queue.acquire(ctx)
	if err != nil {
		queue.finish(id, "", err)
		return
	}
	defer release()
	queue.mu.Lock()
	item := queue.jobs[id]
	if item == nil || item.State != "running" {
		queue.mu.Unlock()
		return
	}
	item.Progress = "syncing " + item.Event.Repository
	item.UpdatedAt = queue.now()
	if err := queue.persist(item); err != nil {
		queue.mu.Unlock()
		queue.finish(id, "", err)
		return
	}
	event := item.Event
	queue.mu.Unlock()
	detail, runErr := processor.Process(ctx, event)
	queue.finish(id, detail, runErr)
}

func (queue *Queue) finish(id, detail string, runErr error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	item := queue.jobs[id]
	if item == nil {
		return
	}
	for _, alias := range repositoryAliases(item.Event) {
		delete(queue.activeRepos, alias)
	}
	item.UpdatedAt = queue.now()
	if runErr != nil {
		item.State = "queued"
		item.Error = runErr.Error()
		item.Progress = "retry pending"
		item.RetryAt = item.UpdatedAt.Add(queue.retryDelay)
	} else {
		item.State = "succeeded"
		item.Error = ""
		item.Progress = detail
		item.RetryAt = time.Time{}
	}
	if err := queue.persist(item); err != nil {
		item.State = "queued"
		item.Error = "persist repository event completion: " + err.Error()
		item.Progress = "completion persistence failed; retry pending"
		item.RetryAt = item.UpdatedAt.Add(queue.retryDelay)
	}
	queue.notifyLocked()
	if queue.progress != nil {
		queue.progress("repository event " + id + ": " + item.Progress)
	}
}

func (queue *Queue) applySupersedes(item *job) error {
	for _, id := range item.Supersedes {
		candidate := queue.jobs[id]
		if candidate == nil || candidate.State == "superseded" {
			continue
		}
		if candidate.State != "queued" {
			continue
		}
		candidate.State = "superseded"
		candidate.Progress = "superseded by " + item.Event.ID
		candidate.UpdatedAt = queue.now()
		if err := queue.persist(candidate); err != nil {
			return err
		}
	}
	return nil
}

func (queue *Queue) reconcileSuperseded() error {
	items := make([]*job, 0, len(queue.jobs))
	for _, item := range queue.jobs {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	for _, item := range items {
		if err := queue.applySupersedes(item); err != nil {
			return err
		}
	}
	return nil
}

func repositoryAliases(event repositoryevent.Event) []string {
	current := normalizeRepository(event.Repository)
	if event.Reason != repositoryevent.ReasonRepositoryRenamed {
		return []string{current}
	}
	previous := normalizeRepository(event.PreviousRepository)
	if previous == current {
		return []string{current}
	}
	return []string{previous, current}
}

func normalizeRepository(repository string) string { return strings.ToLower(repository) }

func aliasesOverlap(active map[string]bool, aliases []string) bool {
	for _, alias := range aliases {
		if active[alias] {
			return true
		}
	}
	return false
}

func (queue *Queue) heartbeat(ctx context.Context, id string, done <-chan struct{}) {
	ticker := time.NewTicker(queue.progressEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			queue.mu.Lock()
			item := queue.jobs[id]
			if item != nil && item.State == "running" {
				item.UpdatedAt = queue.now()
				_ = queue.persist(item)
				if queue.progress != nil {
					queue.progress("repository event " + id + ": " + item.Progress)
				}
			}
			queue.mu.Unlock()
		}
	}
}

func (queue *Queue) persist(item *job) error {
	raw, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	name := eventFileName(item.Event.ID)
	path := filepath.Join(queue.directory, name+".json")
	temporary, err := os.CreateTemp(queue.directory, ".job-*")
	if err != nil {
		return err
	}
	tempName := temporary.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(queue.directory)
}

func (queue *Queue) notifyLocked() {
	close(queue.changed)
	queue.changed = make(chan struct{})
}

func eventDigest(event repositoryevent.Event) string {
	raw, _ := json.Marshal(event)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func eventFileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
