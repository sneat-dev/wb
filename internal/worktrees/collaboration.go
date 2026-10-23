package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

type collaborationCheckoutPorts struct {
	home           func(string) (string, error)
	openDirectory  func(string, bool) (*os.File, error)
	readJSON       func(*os.File, string, any) error
	repositoryRoot func(context.Context, string) (string, error)
	resolvePath    func(string) (string, error)
	gitDirs        func(context.Context, string) (string, string, error)
	stat           func(string) (os.FileInfo, error)
	registered     func(context.Context, string) ([]string, error)
}

var ErrCollaborationCanonicalClone = errors.New("coordination requires a linked worktree, not its canonical clone")

func defaultCollaborationCheckoutPorts() collaborationCheckoutPorts {
	return collaborationCheckoutPorts{
		home: wbhome.Root, openDirectory: worktreesecure.OpenAbsoluteDirectoryNoFollow,
		readJSON: filewrite.ReadJSONAt, repositoryRoot: RepositoryRootFor,
		resolvePath: filepath.EvalSymlinks, gitDirs: gitDirectories, stat: os.Stat,
		registered: registeredCollaborationWorktrees,
	}
}

// registeredCollaborationWorktrees discovers current Git registrations from
// local canonical clones. ID lookup before opt-in never guesses a pathname
// from the digest or trusts a former coordination snapshot.
func registeredCollaborationWorktrees(ctx context.Context, projectsRoot string) ([]string, error) {
	repositories, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, repository := range repositories {
		output, err := git(ctx, repository.Path, "worktree", "list", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("list registered worktrees of %s: %w", repository.Path, err)
		}
		paths = append(paths, worktreePathsFromPorcelain(output)...)
	}
	return paths, nil
}

// ResolveCollaborationCheckout binds either a path or a previously displayed
// checkout ID to the same live linked Git worktree. The ID is derived from
// Git's exact per-worktree and common directories, rather than a mutable task
// name or a pathname that can be aliased or moved.
func ResolveCollaborationCheckout(ctx context.Context, projectsRoot, idOrPath string) (worktreecollab.Checkout, error) {
	return defaultCollaborationCheckoutPorts().resolve(ctx, projectsRoot, idOrPath)
}

func (ports collaborationCheckoutPorts) resolve(ctx context.Context, projectsRoot, idOrPath string) (worktreecollab.Checkout, error) {
	if strings.HasPrefix(idOrPath, "wt-") && len(idOrPath) == 67 && !strings.ContainsRune(idOrPath, filepath.Separator) {
		home, err := ports.home(projectsRoot)
		if err != nil {
			return worktreecollab.Checkout{}, err
		}
		directory, err := ports.openDirectory(filepath.Join(home, "worktree-collaboration"), false)
		if errors.Is(err, os.ErrNotExist) {
			return ports.resolveUninitializedID(ctx, projectsRoot, idOrPath)
		}
		if err != nil {
			return worktreecollab.Checkout{}, err
		}
		defer func() { _ = directory.Close() }()
		var recorded worktreecollab.State
		if err := ports.readJSON(directory, idOrPath+".json", &recorded); errors.Is(err, os.ErrNotExist) {
			return ports.resolveUninitializedID(ctx, projectsRoot, idOrPath)
		} else if err != nil {
			return worktreecollab.Checkout{}, err
		}
		if recorded.Checkout.ID != idOrPath || recorded.Validate(recorded.Checkout) != nil {
			return worktreecollab.Checkout{}, fmt.Errorf("coordination ID does not bind a valid snapshot")
		}
		actual, err := ports.resolvePathCheckout(ctx, recorded.Checkout.Root)
		if err != nil {
			return worktreecollab.Checkout{}, err
		}
		if actual != recorded.Checkout {
			return worktreecollab.Checkout{}, fmt.Errorf("coordination checkout identity changed since publication")
		}
		return actual, nil
	}
	return ports.resolvePathCheckout(ctx, idOrPath)
}

func (ports collaborationCheckoutPorts) resolveUninitializedID(ctx context.Context, projectsRoot, id string) (worktreecollab.Checkout, error) {
	paths, err := ports.registered(ctx, projectsRoot)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	var matched worktreecollab.Checkout
	for _, path := range paths {
		candidate, err := ports.resolvePathCheckout(ctx, path)
		if errors.Is(err, ErrCollaborationCanonicalClone) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return worktreecollab.Checkout{}, err
		}
		if candidate.ID != id {
			continue
		}
		if matched.ID != "" && matched != candidate {
			return worktreecollab.Checkout{}, fmt.Errorf("coordination ID matches multiple live checkouts")
		}
		matched = candidate
	}
	if matched.ID == "" {
		return worktreecollab.Checkout{}, fmt.Errorf("coordination ID %s is not a registered linked worktree", id)
	}
	return matched, nil
}

func (ports collaborationCheckoutPorts) resolvePathCheckout(ctx context.Context, path string) (worktreecollab.Checkout, error) {
	root, err := ports.repositoryRoot(ctx, path)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	root, err = ports.resolvePath(root)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	held, err := ports.openDirectory(root, false)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	defer func() { _ = held.Close() }()
	before, err := held.Stat()
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	gitDir, commonDir, err := ports.gitDirs(ctx, root)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	gitDir, err = ports.resolvePath(gitDir)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	commonDir, err = ports.resolvePath(commonDir)
	if err != nil {
		return worktreecollab.Checkout{}, err
	}
	if gitDir == commonDir {
		return worktreecollab.Checkout{}, ErrCollaborationCanonicalClone
	}
	after, err := ports.stat(root)
	if err != nil || !os.SameFile(before, after) {
		return worktreecollab.Checkout{}, fmt.Errorf("worktree root changed during identity resolution: %w", err)
	}
	sum := sha256.Sum256([]byte(gitDir + "\x00" + commonDir))
	return worktreecollab.Checkout{ID: "wt-" + hex.EncodeToString(sum[:]), Root: root,
		GitDir: gitDir, CommonDir: commonDir}, nil
}

// ObserveCollaborationLegacyOwner is called only beneath the coordination
// lock. It takes the existing local-journal lock second and rechecks the
// effective last custody record before the first explicit owner is published.
// Historical records are observations, not current owner membership.
func ObserveCollaborationLegacyOwner(worktree, sessionDirectory string) (worktreecollab.ObservedOwner, error) {
	return defaultCollaborationLegacyPorts().observe(worktree, sessionDirectory, true)
}

// ObserveCollaborationLegacyOwnerReadOnly reports existing custody evidence
// without creating a private Work Log directory merely because info was read.
func ObserveCollaborationLegacyOwnerReadOnly(worktree, sessionDirectory string) (worktreecollab.ObservedOwner, error) {
	ports := defaultCollaborationLegacyPorts()
	ports.openLocal = func(worktree string, _ bool) (*os.File, error) {
		return worktreejournal.OpenJournalSubdirectory(worktree, worklogDirectory, false)
	}
	ports.lock = func(*os.File) (func(), error) { return func() {}, nil }
	return ports.observe(worktree, sessionDirectory, false)
}

type collaborationLegacyPorts struct {
	openLocal    func(string, bool) (*os.File, error)
	lock         func(*os.File) (func(), error)
	readEvents   func(*os.File) ([]worktreejournal.LocalWorkLogEvent, bool, error)
	readManifest func(string) (Manifest, error)
	processAlive func(int) bool
	lookupExact  func(string, int) (session.Record, bool, error)
}

func defaultCollaborationLegacyPorts() collaborationLegacyPorts {
	return collaborationLegacyPorts{openLocal: openLocalWorkLogDir, lock: lockCollaborationLegacyJournal,
		readEvents: readLocalEventsForAppend, readManifest: ReadManifest,
		processAlive: session.ProcessAlive, lookupExact: session.LookupExact}
}

func (ports collaborationLegacyPorts) observe(worktree, sessionDirectory string, create bool) (worktreecollab.ObservedOwner, error) {
	directory, err := ports.openLocal(worktree, create)
	if !create && errors.Is(err, os.ErrNotExist) {
		return ports.observeManifest(worktree)
	}
	if err != nil {
		return worktreecollab.ObservedOwner{}, err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := ports.lock(directory)
	if err != nil {
		return worktreecollab.ObservedOwner{}, err
	}
	defer unlock()
	events, torn, err := ports.readEvents(directory)
	if err != nil {
		return worktreecollab.ObservedOwner{}, err
	}
	if torn {
		return worktreecollab.ObservedOwner{}, fmt.Errorf("legacy owner journal has an interrupted record")
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Owner == nil {
			continue
		}
		owner := event.Owner
		observation := worktreecollab.ObservedOwner{ID: legacyOwnerObservationID(event.ID, owner.Agent, owner.PID), Status: "unknown"}
		if owner.PID > 0 && !ports.processAlive(owner.PID) {
			observation.Status = "inactive"
			return observation, nil
		}
		record, live, err := ports.lookupExact(sessionDirectory, owner.PID)
		if err == nil && live && record.Lifecycle == "" && record.WBSessionID != "" &&
			(AgentIdentity{Runtime: record.Runtime, AgentID: record.NativeHarnessID}).Agent() == owner.Agent &&
			!record.StartedAt.After(owner.At) {
			observation.SessionID = record.WBSessionID
			observation.Status = "live"
		}
		return observation, nil
	}
	return ports.observeManifest(worktree)
}

func (ports collaborationLegacyPorts) observeManifest(worktree string) (worktreecollab.ObservedOwner, error) {
	manifest, err := ports.readManifest(worktree)
	if errors.Is(err, errManifestNotFound) {
		return worktreecollab.ObservedOwner{}, nil
	}
	if err != nil {
		return worktreecollab.ObservedOwner{}, err
	}
	if manifest.AgentID == "" && manifest.Initiator == "" {
		return worktreecollab.ObservedOwner{}, nil
	}
	return worktreecollab.ObservedOwner{ID: legacyOwnerObservationID(manifest.EffortID+"/"+manifest.RunID,
		manifest.AgentID+"/"+manifest.Initiator, 0), Status: "unknown"}, nil
}

func legacyOwnerObservationID(record, agent string, pid int) string {
	sum := sha256.Sum256([]byte(record + "\x00" + agent + "\x00" + strconv.Itoa(pid)))
	return "legacy-" + hex.EncodeToString(sum[:])
}
