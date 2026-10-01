package fleet

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// The collectors are the snapshotter's only way to read a source, so a test
// replaces each with a fake and a request-time test can prove none runs. The
// local ones are read-only and local by construction: they read files, and run
// a few Git commands (for-each-ref, symbolic-ref, ls-tree, cat-file), which
// neither contact a network nor write, each through the hardened helper in
// git.go. mapping.go is what turns what they return into the
// closed document, and nothing outside it reads a collected value.

// LinkedWorktree is one linked worktree a repository's Git state registers.
type LinkedWorktree struct {
	// Path is the worktree's directory and Branch the branch it has checked
	// out, empty when detached. Both stay inside the daemon.
	Path   string
	Branch string
}

// BranchRef is one local or remote branch ref.
type BranchRef struct {
	Name         string
	Scope        string
	Upstream     string
	Ahead        int
	Behind       int
	UpstreamGone bool
	CommittedAt  time.Time
}

// WorktreeRecord is what a WB worktree records about itself locally: its
// manifest and its heartbeat.
type WorktreeRecord struct {
	Task        string
	Branch      string
	CreatedAt   time.Time
	HeartbeatAt time.Time
}

// GitGate says whether Git is new enough to be run against untrusted
// repositories at all: before 2.45 GIT_NO_LAZY_FETCH is ignored, so a
// repository's promisor remote could be contacted. The snapshotter asks once.
type GitGate interface {
	GitUsable(ctx context.Context) bool
}

// RepositoryCollector lists this machine's canonical clones.
type RepositoryCollector interface {
	Repositories(ctx context.Context) ([]discover.Repo, error)
}

// WorktreeCollector lists the linked worktrees one repository's Git state
// registers. It reads Git state, so the snapshotter asks only when the
// repository's fingerprint moved.
type WorktreeCollector interface {
	Worktrees(ctx context.Context, repository discover.Repo) ([]LinkedWorktree, error)
}

// BranchCollector lists one repository's local and remote branches and names
// its default branch (empty when it has none). Like WorktreeCollector it is
// asked only when the fingerprint moved.
type BranchCollector interface {
	Branches(ctx context.Context, repository discover.Repo) ([]BranchRef, error)
	DefaultBranch(ctx context.Context, repository discover.Repo) string
}

// IdentityCollector names the repository a clone's origin points at, as the
// lower-case host/owner/name identity the lifecycle-hook worker puts in its
// receipts. The origin URL stays inside the collector: only the identity comes
// back, and it is empty for a clone with no origin or one that names no forge.
// Like the other Git readers it is asked only when the fingerprint moved.
type IdentityCollector interface {
	Identity(ctx context.Context, repository discover.Repo) (string, error)
}

// ReadmeCollector reads the README.md committed at the tip of a branch from
// Git's object store, never from the working tree. It fails with
// errReadmeAbsent, errReadmeNotRegular or errReadmeTooLarge.
type ReadmeCollector interface {
	Readme(ctx context.Context, repository discover.Repo, branch string) ([]byte, error)
}

// RecordCollector reads a worktree's own manifest and heartbeat. They live
// outside Git state, so the snapshotter asks on every refresh. The result is
// false for a directory that is not a WB task worktree.
type RecordCollector interface {
	Record(worktree string) (WorktreeRecord, bool)
}

// PullRequestCollector lists the pull requests recorded locally against active
// Work Log claims, without any network call.
type PullRequestCollector interface {
	PullRequests(ctx context.Context) ([]worktrees.RegisteredPullRequestBinding, error)
}

// SessionCollector lists the registered agent sessions.
type SessionCollector interface {
	Sessions(ctx context.Context) ([]session.View, error)
}

// RunCollector lists the dispatched agent runs with their state resolved.
type RunCollector interface {
	Runs(ctx context.Context) ([]agents.Result, error)
}

// RemoteCollector reads the snapshots other machines published, from what this
// machine already holds locally.
type RemoteCollector interface {
	Machines(ctx context.Context) ([]remotestate.Entry, error)
}

// Collectors is every source the snapshotter reads. Remote may be nil: a
// machine with no remote provider configured has no other machines.
type Collectors struct {
	Git          GitGate
	Repositories RepositoryCollector
	Worktrees    WorktreeCollector
	Branches     BranchCollector
	Readme       ReadmeCollector
	Identity     IdentityCollector
	Records      RecordCollector
	PullRequests PullRequestCollector
	Sessions     SessionCollector
	Runs         RunCollector
	Remote       RemoteCollector
	// CodeIndex reads indexer receipts; nil means no code-index freshness.
	CodeIndex CodeIndexCollector
	// CodeIndexProvider reports the statistics of a checkout's index; nil means
	// no provider is configured, which the document reports.
	CodeIndexProvider CodeIndexProvider
}

// localIndexMaxAge bounds how long the persisted clone inventory is reused
// while no owner directory changed; ScanLocalIndexed documents the bound.
const localIndexMaxAge = 15 * time.Minute

// LocalCollectors reads this machine. Nothing it does contacts a network or
// writes inside a repository; the clone inventory cache, when IndexCachePath
// is set, is written outside every repository.
type LocalCollectors struct {
	// ProjectsRoot is the projects root and Home the WB home directory.
	ProjectsRoot string
	Home         string
	// IndexCachePath is where the clone inventory is cached between scans;
	// empty means a fresh scan every refresh.
	IndexCachePath string
	// Git is the Git binary; empty means "git". A test supplies its own.
	Git string
	// CodeIndex reads the indexer receipts; nil means entries carry no code
	// index.
	CodeIndex *LocalCodeIndex
	// CodeIndexProvider is the configured code-index provider; nil means none.
	CodeIndexProvider CodeIndexProvider
}

// GitUsable reads `git version` through the hardened helper and reports
// whether it names Git 2.45 or newer.
func (c LocalCollectors) GitUsable(ctx context.Context) bool {
	out, err := c.git(ctx, ".", "version")
	return err == nil && gitVersionUsable(string(out))
}

// git runs the Git binary through the hardened helper.
func (c LocalCollectors) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitOutput(ctx, firstNonEmpty(c.Git, "git"), dir, args...)
}

// Collectors is c as the snapshotter's local sources, with remote as the
// other machines' source (nil for none).
func (c LocalCollectors) Collectors(remote RemoteCollector) Collectors {
	collectors := Collectors{Git: c, Repositories: c, Worktrees: c, Branches: c, Readme: c, Identity: c, Records: c, PullRequests: c, Sessions: c, Runs: c, Remote: remote, CodeIndexProvider: c.CodeIndexProvider}
	if c.CodeIndex != nil {
		collectors.CodeIndex = *c.CodeIndex
	}
	return collectors
}

// Repositories scans the projects root, reusing the cached inventory while its
// fingerprint holds.
func (c LocalCollectors) Repositories(ctx context.Context) ([]discover.Repo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := discover.ScanLocalIndexed(c.ProjectsRoot, discover.LocalIndexOptions{CachePath: c.IndexCachePath, MaxAge: localIndexMaxAge})
	return result.Repositories, err
}

// Worktrees reads the linked worktrees from the administrative directories Git
// keeps under .git/worktrees: each holds the worktree's path (gitdir, relative
// to that directory when not absolute) and its HEAD. It runs no Git command. An
// entry whose gitdir is gone is a worktree being created or pruned, and one
// whose directory does not exist or lies outside the projects root and the
// worktree stores is not WB's; both are skipped. The path returned has no
// symbolic link in it.
func (c LocalCollectors) Worktrees(ctx context.Context, repository discover.Repo) ([]LinkedWorktree, error) {
	admin := filepath.Join(repository.Path, ".git", "worktrees")
	entries, err := os.ReadDir(admin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	roots := c.worktreeRoots()
	var linked []LinkedWorktree
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		gitdir, err := os.ReadFile(filepath.Join(admin, entry.Name(), "gitdir"))
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(gitdir))
		if !filepath.IsAbs(path) {
			path = filepath.Join(admin, entry.Name(), path)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
		if info, statErr := os.Stat(resolved); err != nil || statErr != nil || !info.IsDir() || !within(roots, resolved) {
			continue
		}
		head, _ := os.ReadFile(filepath.Join(admin, entry.Name(), "HEAD"))
		branch, _ := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
		if strings.TrimSpace(string(head)) == branch {
			branch = "" // detached: HEAD holds a commit, not a branch ref
		}
		linked = append(linked, LinkedWorktree{Path: resolved, Branch: branch})
	}
	return linked, nil
}

// worktreeRoots are the directories a worktree may live under, symbolic links
// resolved: the projects root, the WB home and each layout's worktree store.
func (c LocalCollectors) worktreeRoots() []string {
	candidates := []string{c.ProjectsRoot, c.Home}
	if resolution, err := wbhome.Resolve(c.ProjectsRoot); err == nil {
		for _, layout := range resolution.Read {
			candidates = append(candidates, layout.WorktreesRoot, layout.Home)
		}
	}
	var roots []string
	for _, candidate := range candidates {
		if resolved, err := filepath.EvalSymlinks(candidate); candidate != "" && err == nil {
			roots = append(roots, resolved)
		}
	}
	return roots
}

// within reports whether path is one of roots or below one.
func within(roots []string, path string) bool {
	for _, root := range roots {
		if relative, err := filepath.Rel(root, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// refFormat is what for-each-ref prints for each ref: its full name, its
// upstream's short name, the tracking summary and the committer date, NUL
// separated.
const refFormat = "%(refname)%00%(upstream:short)%00%(upstream:track)%00%(committerdate:unix)"

// Branches runs one `git for-each-ref` over refs/heads and refs/remotes, which
// only reads the repository's refs.
func (c LocalCollectors) Branches(ctx context.Context, repository discover.Repo) ([]BranchRef, error) {
	out, err := c.git(ctx, repository.Path, "for-each-ref", "--format="+refFormat, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	return parseRefs(string(out)), nil
}

// DefaultBranch is the branch origin's HEAD names (refs/remotes/origin/HEAD),
// else the branch the clone has checked out, else empty.
func (c LocalCollectors) DefaultBranch(ctx context.Context, repository discover.Repo) string {
	if out, err := c.git(ctx, repository.Path, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "origin/"); ok && name != "" {
			return name
		}
	}
	if out, err := c.git(ctx, repository.Path, "symbolic-ref", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// Identity reads `remote.origin.url` from the repository's own configuration
// through the hardened helper (a local, read-only read; Git exits 1 when the key
// is absent) and turns it into the worker's identity with the worker's own
// function. A URL that names no forge gives an empty identity, not an error.
func (c LocalCollectors) Identity(ctx context.Context, repository discover.Repo) (string, error) {
	out, err := c.git(ctx, repository.Path, "config", "--get", "remote.origin.url")
	if notFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	identity, parseErr := lifecyclehooks.IdentityFromOrigin(strings.TrimSpace(string(out)))
	if parseErr != nil {
		return "", nil
	}
	return identity, nil
}

// The README failures.
var (
	errReadmeAbsent     = errors.New("README.md is not in the tree")
	errReadmeNotRegular = errors.New("README.md is not a regular file")
	errReadmeTooLarge   = errors.New("README.md is larger than the limit")
)

// MaxReadmeBytes caps the README the owner route serves: one mebibyte, far
// beyond any README worth rendering and small enough to hold in memory per
// request.
const MaxReadmeBytes = 1 << 20

// Readme reads README.md at the tip of refs/heads/<branch> from the object
// store: the tree entry is looked up, must be a regular blob (mode 100644 or
// 100755, so never a symbolic link, tree or submodule), is measured before it
// is read, and is read by its object id. No path in the working tree is
// opened.
func (c LocalCollectors) Readme(ctx context.Context, repository discover.Repo, branch string) ([]byte, error) {
	// A branch ref that does not exist is an absent README, not a failure:
	// show-ref exits 1 for it, and for nothing else.
	if _, err := c.git(ctx, repository.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		var exit exitError
		if errors.As(err, &exit) && exit.code == 1 {
			return nil, errReadmeAbsent
		}
		return nil, err
	}
	listing, err := c.git(ctx, repository.Path, "ls-tree", "-z", "refs/heads/"+branch, "--", "README.md")
	if err != nil {
		return nil, err
	}
	entry, found := strings.CutSuffix(string(listing), "\x00")
	if !found || entry == "" {
		return nil, errReadmeAbsent
	}
	meta, name, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if name != "README.md" || len(fields) != 3 || !isObjectID(fields[2]) {
		return nil, errGit
	}
	if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, errReadmeNotRegular
	}
	size, err := c.git(ctx, repository.Path, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, err
	}
	length, parseErr := strconv.Atoi(strings.TrimSpace(string(size)))
	if parseErr != nil {
		return nil, errGit
	}
	if length > MaxReadmeBytes {
		return nil, errReadmeTooLarge
	}
	// The read is capped too, so a blob larger than the size just measured is
	// never buffered whole.
	data, err := gitOutputLimited(ctx, firstNonEmpty(c.Git, "git"), repository.Path, MaxReadmeBytes, "cat-file", "blob", fields[2])
	if errors.Is(err, errGitOutputTooLarge) {
		return nil, errReadmeTooLarge
	}
	return data, err
}

// isObjectID reports whether text is a full SHA-1 or SHA-256 object id.
func isObjectID(text string) bool {
	if len(text) != 40 && len(text) != 64 {
		return false
	}
	for _, character := range text {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// parseRefs turns refFormat lines into branches. A line that does not parse,
// a tag and a remote's HEAD symbolic ref are skipped.
func parseRefs(output string) []BranchRef {
	var refs []BranchRef
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 4 {
			continue
		}
		ref := BranchRef{Upstream: fields[1]}
		if name, ok := strings.CutPrefix(fields[0], "refs/heads/"); ok {
			ref.Name, ref.Scope = name, BranchLocal
		} else if name, ok := strings.CutPrefix(fields[0], "refs/remotes/"); ok && !strings.HasSuffix(name, "/HEAD") {
			ref.Name, ref.Scope = name, BranchRemote
		} else {
			continue
		}
		if seconds, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
			ref.CommittedAt = time.Unix(seconds, 0).UTC()
		}
		ref.Ahead, ref.Behind, ref.UpstreamGone = parseTrack(fields[2])
		refs = append(refs, ref)
	}
	return refs
}

// parseTrack reads `%(upstream:track)`: "[ahead 1, behind 2]", "[ahead 1]",
// "[behind 2]", "[gone]" or empty.
func parseTrack(track string) (ahead, behind int, gone bool) {
	for _, part := range strings.Split(strings.Trim(track, "[]"), ", ") {
		switch {
		case part == "gone":
			gone = true
		case strings.HasPrefix(part, "ahead "):
			ahead, _ = strconv.Atoi(strings.TrimPrefix(part, "ahead "))
		case strings.HasPrefix(part, "behind "):
			behind, _ = strconv.Atoi(strings.TrimPrefix(part, "behind "))
		}
	}
	return ahead, behind, gone
}

// Record reads the worktree's manifest and heartbeat. Both reads only open
// files; a directory with no valid manifest is not a WB task worktree.
func (c LocalCollectors) Record(worktree string) (WorktreeRecord, bool) {
	manifest, err := worktrees.ReadManifest(worktree)
	if err != nil {
		return WorktreeRecord{}, false
	}
	return WorktreeRecord{Task: manifest.EffortID, Branch: manifest.Branch, CreatedAt: manifest.CreatedAt, HeartbeatAt: worktrees.HeartbeatAt(worktree)}, true
}

// PullRequests lists the pull requests `wb pr create` recorded beside active
// Work Log claims. It reads local files and calls no forge.
func (c LocalCollectors) PullRequests(ctx context.Context) ([]worktrees.RegisteredPullRequestBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return worktrees.ListRegisteredPullRequestBindings(c.ProjectsRoot)
}

// Sessions lists the registered sessions with their liveness.
func (c LocalCollectors) Sessions(context.Context) ([]session.View, error) {
	return session.List(filepath.Join(c.Home, session.DirName))
}

// Runs lists the dispatched runs, each rendered so that a run whose owner is
// gone reads as abandoned instead of running.
func (c LocalCollectors) Runs(context.Context) ([]agents.Result, error) {
	store := agents.NewStore(c.Home)
	records, err := store.List()
	if err != nil {
		return nil, err
	}
	results := make([]agents.Result, 0, len(records))
	for _, record := range records {
		results = append(results, store.Render(record))
	}
	return results, nil
}

// LocalStateReader reads other machines' snapshots from the copy of the remote
// state store this machine already holds, without fetching it.
type LocalStateReader interface {
	ReadLocal(ctx context.Context) ([]remotestate.Entry, error)
}

// LocalStateCollector reads other machines' snapshots through a
// LocalStateReader. It is the only source of other machines, and it contacts
// nothing: a machine whose state store has no local copy knows no others.
type LocalStateCollector struct {
	Reader LocalStateReader
}

// Machines reads the machines the local copy of the state store holds.
func (c LocalStateCollector) Machines(ctx context.Context) ([]remotestate.Entry, error) {
	return c.Reader.ReadLocal(ctx)
}
