// Package gitops wraps the git and gh commands needed to read the default
// branch of a repo and to land a change on it — pushing directly when allowed,
// or opening an auto-merge PR when the branch is protected. The flow mirrors
// the proven all-repos-codegrapher.sh script.
package gitops

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// run executes a command in dir and returns combined output. The child runs
// with prompting disabled so a missing credential or an unknown host key fails
// with a message instead of blocking on a terminal nobody is watching.
func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = console.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// DefaultBranch returns the remote's default branch (e.g. "main"). It first
// refreshes origin/HEAD from the remote, because a local clone's cached
// origin/HEAD can be stale (e.g. the repo was renamed master -> main after the
// clone was made), which would otherwise yield the wrong branch.
func DefaultBranch(repoPath string) (string, error) {
	_, _ = run(repoPath, "git", "remote", "set-head", "origin", "--auto")
	out, err := run(repoPath, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		return strings.TrimPrefix(ref, "refs/remotes/origin/"), nil
	}
	out, err = run(repoPath, "git", "remote", "show", "origin")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "HEAD branch") {
			f := strings.Fields(line)
			return f[len(f)-1], nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for %s", repoPath)
}

// Fetch updates remote refs.
func Fetch(repoPath string) error {
	_, err := run(repoPath, "git", "fetch", "--quiet", "origin")
	return err
}

// ShowFile returns the contents of path at ref (e.g. "origin/main"), and a
// false ok when the file does not exist at that ref.
func ShowFile(repoPath, ref, path string) (content string, ok bool, err error) {
	cmd := exec.Command("git", "show", ref+":"+path)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Missing file is an expected, non-fatal outcome.
		return "", false, nil
	}
	return string(out), true, nil
}

// Clone clones cloneURL into dest (creating parent dirs).
func Clone(cloneURL, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_, err := run(filepath.Dir(dest), "git", "clone", "--quiet", cloneURL, dest)
	return err
}

// LandOptions configures how a change is committed and pushed.
type LandOptions struct {
	DefaultBranch string
	CommitMessage string
	PRBranch      string
	PRTitle       string
	PRBody        string
}

// Mutator edits files inside the worktree and reports whether it changed
// anything along with a human-readable detail.
type Mutator func(worktreePath string) (changed bool, detail string, err error)

// Outcome describes what Land did.
type Outcome struct {
	Changed bool
	Detail  string
	PRURL   string
}

// Land fetches, creates a detached worktree at origin/<default>, runs mutate,
// and — if it changed files — commits and pushes to the default branch, falling
// back to a PR with auto-merge when the push is rejected (protected branch).
func Land(repoPath string, opt LandOptions, mutate Mutator) (Outcome, error) {
	if err := Fetch(repoPath); err != nil {
		return Outcome{}, err
	}
	ref := "origin/" + opt.DefaultBranch

	wt, err := os.MkdirTemp("", "wb-wt-")
	if err != nil {
		return Outcome{}, err
	}
	defer func() {
		_, _ = run(repoPath, "git", "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}()

	if _, err := run(repoPath, "git", "worktree", "add", "--detach", "--quiet", wt, ref); err != nil {
		return Outcome{}, err
	}

	changed, detail, err := mutate(wt)
	if err != nil {
		return Outcome{}, err
	}
	if !changed {
		return Outcome{Changed: false, Detail: detail}, nil
	}

	if _, err := run(wt, "git", "add", "-A"); err != nil {
		return Outcome{}, err
	}
	if _, err := run(wt, "git", "commit", "-m", opt.CommitMessage); err != nil {
		return Outcome{}, err
	}

	// If the local working copy is dirty (uncommitted changes or unpushed
	// commits), never push to the default branch — open a PR so the user's
	// in-progress work is left untouched. Otherwise try a direct push first and
	// fall back to a PR when the branch is protected.
	if dirty, reason, derr := LocalState(repoPath); derr == nil && dirty {
		return openPR(wt, opt, "local has "+reason)
	} else if _, perr := run(wt, "git", "push", "origin", "HEAD:"+opt.DefaultBranch); perr == nil {
		return Outcome{Changed: true, Detail: "pushed to " + opt.DefaultBranch}, nil
	}
	return openPR(wt, opt, "protected branch")
}

// openPR pushes the change on a named branch and opens an auto-merge PR.
// reason is appended to the outcome detail for visibility. The worktree is
// detached, so we create a real local branch first: branch-name pre-push hooks
// read the local ref, and pushing from detached HEAD shows up as "HEAD" and is
// rejected by hooks that enforce a naming convention.
func openPR(wt string, opt LandOptions, reason string) (Outcome, error) {
	if _, err := run(wt, "git", "checkout", "-B", opt.PRBranch); err != nil {
		return Outcome{}, fmt.Errorf("create branch %s failed: %w", opt.PRBranch, err)
	}
	if _, err := run(wt, "git", "push", "--force-with-lease", "origin", opt.PRBranch); err != nil {
		return Outcome{}, fmt.Errorf("push to %s failed: %w", opt.PRBranch, err)
	}
	prOut, err := run(wt, "gh", "pr", "create",
		"--title", opt.PRTitle,
		"--body", opt.PRBody,
		"--base", opt.DefaultBranch,
		"--head", opt.PRBranch)
	if err != nil {
		return Outcome{}, fmt.Errorf("PR creation failed: %w", err)
	}
	prURL := lastLine(prOut)
	// Best-effort auto-merge; ignore failure (checks may not be configured).
	_, _ = run(wt, "gh", "pr", "merge", "--auto", "--squash", prURL)
	return Outcome{Changed: true, Detail: "PR " + prURL + " (" + reason + ")", PRURL: prURL}, nil
}

// LocalState reports whether repoPath has uncommitted changes (including
// untracked files) or local commits not present on any remote. The reason
// string names the first condition found.
func LocalState(repoPath string) (dirty bool, reason string, err error) {
	out, err := run(repoPath, "git", "status", "--porcelain")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(out) != "" {
		return true, "uncommitted changes", nil
	}
	out, err = run(repoPath, "git", "log", "--branches", "--not", "--remotes", "--oneline")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(out) != "" {
		return true, "unpushed commits", nil
	}
	return false, "", nil
}

// WorktreeChanged reports whether the worktree at dir has any uncommitted
// changes (tracked edits or untracked files), via `git status --porcelain`.
func WorktreeChanged(dir string) (bool, error) {
	out, err := run(dir, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Pull runs `git pull --ff-only --quiet` on the currently checked-out branch
// of repoPath.
//
// Fast-forward-only is deliberate. A canonical clone must never acquire a
// merge commit from an unattended fleet sync, and whether a plain `git pull`
// would merge, rebase, or refuse depends on the machine's pull.rebase
// setting — so without --ff-only the same divergence rewrites history on one
// machine and errors on another. Refusing is the only safe answer; the caller
// classifies the refusal.
func Pull(repoPath string) error {
	const attempts = 5
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		_, err = run(repoPath, "git", "pull", "--ff-only", "--quiet")
		if err == nil || !isTransientPullFailure(err) {
			return err
		}
		if attempt+1 < attempts {
			// A fleet sync has several concurrent SSH handshakes. GitHub can
			// occasionally close one of them under load. Exponential backoff,
			// staggered per checkout, gives the connection burst time to clear
			// without hiding real Git refusals.
			time.Sleep(pullRetryDelay(repoPath, attempt))
		}
	}
	return err
}

func pullRetryDelay(repoPath string, attempt int) time.Duration {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(repoPath))
	// 500ms, 1s, 2s, and 4s, each with up to 499ms deterministic jitter.
	// The delay is deterministic so an interrupted run behaves predictably,
	// unlike a random sleep that makes support incidents harder to reproduce.
	return (500*time.Millisecond)<<attempt + time.Duration(hash.Sum32()%500)*time.Millisecond
}

// isTransientPullFailure reports transport failures worth retrying. It stays
// deliberately narrow: branch deletion, authentication, and fast-forward
// refusals require a human decision and must remain visible to sync callers.
func isTransientPullFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection closed by",
		"connection to github.com closed",
		"connection reset by peer",
		"connection timed out",
		"operation timed out",
		"kex_exchange_identification",
		"expected flush after ref listing",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// RemoteHasBranches reports whether origin publishes any branch at all.
//
// A repository created on GitHub but never pushed to has none, and there is
// nothing for sync to pull from it — now or on any future run, until someone
// pushes. That is distinct from a remote that has branches but not the one
// the local branch tracks, which is a real problem (a renamed or deleted
// branch) and must keep failing loudly.
func RemoteHasBranches(repoPath string) (bool, error) {
	out, err := run(repoPath, "git", "ls-remote", "--heads", "origin")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// UnpushedCommits lists commits present on some local branch and on no
// remote-tracking branch, newest first, as `<short-sha> <subject>` lines.
//
// Deliberately every local branch, not just the checked-out one: work is
// abandoned on a side branch at least as often as on the default one, and a
// clone holding either is holding work that exists nowhere else.
func UnpushedCommits(repoPath string) ([]string, error) {
	// With no remote-tracking refs, `--branches --not --remotes` subtracts
	// nothing and returns the entire history — a clone that has never fetched
	// would be reported as holding thousands of unpushed commits. "Unpushed"
	// only means anything relative to a known remote state, so say nothing
	// rather than something false.
	refs, err := run(repoPath, "git", "for-each-ref", "--count=1", "refs/remotes")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(refs) == "" {
		return nil, nil
	}

	branches, err := unpushableBranches(repoPath)
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		return nil, nil
	}

	args := append([]string{"log", "--oneline"}, branches...)
	out, err := run(repoPath, "git", append(args, "--not", "--remotes")...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

// unpushableBranches lists the local branches whose commits could genuinely
// exist nowhere else.
//
// A branch whose upstream is configured but gone is excluded. Git reports
// "gone" only once a branch has had a remote counterpart that was later
// deleted — which is exactly what a merged pull request leaves behind, and
// which is proof the commits were pushed. Counting them as unpushed reports
// every completed piece of work as though it were at risk, and a warning that
// fires on success is one people learn to ignore.
//
// A branch with no upstream at all is kept: it may never have been pushed.
func unpushableBranches(repoPath string) ([]string, error) {
	out, err := run(repoPath, "git", "for-each-ref",
		"--format=%(refname)%09%(upstream)%09%(upstream:track,nobracket)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		ref, upstream, track := fields[0], fields[1], strings.TrimSpace(fields[2])
		if upstream != "" && track == "gone" {
			continue
		}
		branches = append(branches, ref)
	}
	return branches, nil
}

// TrackingState is the checked-out branch's relationship to its upstream, as
// of the last fetch. It is what sync needs in order to tell a divergence
// ("each side has commits the other lacks") apart from a genuine fault.
type TrackingState struct {
	Branch   string // checked-out branch, empty when HEAD is detached
	Upstream string // e.g. "origin/main", empty when it does not resolve
	Ahead    int    // local commits the upstream does not have
	Behind   int    // upstream commits the local branch does not have
	// Configured records whether the branch names an upstream in git config,
	// which is not the same as Upstream resolving. A branch configured to
	// track a ref the remote no longer publishes has Configured true and
	// Upstream empty — a renamed or deleted branch, and a real problem —
	// while a branch that names no upstream at all simply has nowhere to
	// pull from. Callers must not conflate the two.
	Configured bool
}

// Diverged reports whether the branch and its upstream have each moved on
// with commits the other lacks, so no fast-forward is possible.
func (t TrackingState) Diverged() bool { return t.Ahead > 0 && t.Behind > 0 }

// Summary renders the state as one short line for a sync report, e.g.
// "main is 1 ahead, 2 behind origin/main".
func (t TrackingState) Summary() string {
	switch {
	case t.Branch == "":
		return "detached HEAD"
	case t.Configured && t.Upstream == "":
		return t.Branch + " tracks an upstream that no longer resolves"
	case t.Upstream == "":
		return t.Branch + " has no upstream"
	default:
		return fmt.Sprintf("%s is %d ahead, %d behind %s", t.Branch, t.Ahead, t.Behind, t.Upstream)
	}
}

// Tracking reads the checked-out branch's divergence from its upstream.
//
// The counts are only as current as the last fetch. Callers read it after a
// failed Pull, which has already fetched, so they see current numbers without
// a second round trip. A missing branch, upstream, or merge base is a state
// to report, not an error: the zero-ish TrackingState it returns describes
// exactly the situation the caller has to explain to a human.
func Tracking(repoPath string) (TrackingState, error) {
	var t TrackingState

	if out, err := run(repoPath, "git", "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		t.Branch = strings.TrimSpace(out)
	}
	if t.Branch != "" {
		if _, err := run(repoPath, "git", "config", "--get", "branch."+t.Branch+".merge"); err == nil {
			t.Configured = true
		}
	}

	out, err := run(repoPath, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return t, nil
	}
	t.Upstream = strings.TrimSpace(out)

	out, err = run(repoPath, "git", "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return t, nil
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return t, fmt.Errorf("unexpected rev-list output for %s: %q", repoPath, strings.TrimSpace(out))
	}
	if t.Ahead, err = strconv.Atoi(fields[0]); err != nil {
		return t, fmt.Errorf("unexpected ahead count for %s: %w", repoPath, err)
	}
	if t.Behind, err = strconv.Atoi(fields[1]); err != nil {
		return t, fmt.Errorf("unexpected behind count for %s: %w", repoPath, err)
	}
	return t, nil
}

// SkipSyncKey is the git config key marking a repo that wb sync must leave
// alone. It is always read and written with --local: a plain read falls back
// to ~/.gitconfig, where one stray key would silently disable sync for the
// whole fleet.
const SkipSyncKey = "wb.skip-sync"

// SkipSync reports whether repoPath is marked to be skipped by sync.
//
// Exactly one git exit status means "not marked": 1, for an absent key. Every
// other failure is a real error and is returned as one — notably 128, which
// covers both a malformed value and a path that is not a git repository.
// Swallowing those (as internal/hooks treats `git config --get` failures)
// would silently resume pulling a repo the user asked wb to leave alone.
func SkipSync(repoPath string) (bool, error) {
	out, err := run(repoPath, "git", "config", "--local", "--bool", SkipSyncKey)
	if err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// SetSkipSync marks repoPath to be skipped by sync.
func SetSkipSync(repoPath string) error {
	_, err := run(repoPath, "git", "config", "--local", SkipSyncKey, "true")
	return err
}

// UnsetSkipSync clears the skip marker from repoPath, and is idempotent.
//
// --unset-all rather than --unset: on a key that somehow holds multiple
// values, --unset exits 5 and leaves every value in place, so the caller
// would report success on a repo that is still marked. --unset-all clears
// them all, and exits 5 only when the key is genuinely absent — the no-op
// case, which is success here.
func UnsetSkipSync(repoPath string) error {
	_, err := run(repoPath, "git", "config", "--local", "--unset-all", SkipSyncKey)
	if err != nil && exitCode(err) == 5 {
		return nil
	}
	return err
}

// exitCode digs the process exit status out of an error wrapped by run.
// It returns -1 when err did not come from a finished process.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// CurrentBranch returns the checked-out branch name of repoPath. A detached
// HEAD reports an empty name rather than an error, because `git branch
// --show-current` succeeds there and prints nothing; callers that need a
// branch to push must reject the empty string themselves.
func CurrentBranch(repoPath string) (string, error) {
	out, err := run(repoPath, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HasCommits reports whether repoPath's HEAD resolves, i.e. whether the
// checked-out branch has any commits yet. --verify --quiet exits 1 silently
// on an unborn branch, where a bare `rev-parse HEAD` would exit 128 and print
// "fatal: ambiguous argument 'HEAD'" into the error text.
func HasCommits(repoPath string) (bool, error) {
	_, err := run(repoPath, "git", "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// OriginURL returns repoPath's origin remote URL after any url.<base>.insteadOf
// rewriting. Use it for the URL git will actually contact; use ConfiguredOriginURL
// when comparing against a URL the user configured elsewhere.
func OriginURL(repoPath string) (string, error) {
	out, err := run(repoPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ConfiguredOriginURL returns repoPath's remote.origin.url exactly as written in
// the repository config, before any url.<base>.insteadOf rewriting. Use it when
// comparing against a URL the user configured elsewhere; OriginURL returns the
// effective (rewritten) URL git will actually contact.
func ConfiguredOriginURL(repoPath string) (string, error) {
	out, err := run(repoPath, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitEmpty creates an empty commit, used to give an unborn branch
// something to push.
func CommitEmpty(repoPath, message string) error {
	_, err := run(repoPath, "git", "commit", "--allow-empty", "-m", message)
	return err
}

// PushSetUpstream pushes branch to origin and sets it as the upstream.
func PushSetUpstream(repoPath, branch string) error {
	_, err := run(repoPath, "git", "push", "-u", "origin", branch)
	return err
}

// RepoStatus is git working-tree/history state relevant to sync decisions
// and to reporting why a repo needs attention.
type RepoStatus struct {
	Modified   []string // tracked files with changes
	Untracked  []string // untracked files
	Conflicted []string // merge-conflict paths
	Unpushed   []string // `git log --branches --not --remotes --oneline` lines
	Stashed    []string // `git stash list` lines
}

// WorkingTreeDirty reports whether the working tree itself has changes
// (modified, untracked, or conflicted files) — the check used to decide
// whether it is safe to `git pull`.
func (s RepoStatus) WorkingTreeDirty() bool {
	return len(s.Modified) > 0 || len(s.Untracked) > 0 || len(s.Conflicted) > 0
}

// Dirty reports whether s represents any state — working tree, stash, or
// unpushed commits — that should block automatically removing an archived
// repo's local clone.
func (s RepoStatus) Dirty() bool {
	return s.WorkingTreeDirty() || len(s.Unpushed) > 0 || len(s.Stashed) > 0
}

// Summary renders a short human-readable description of s, e.g. "3 modified
// files, 1 untracked file". Empty when nothing is set.
func (s RepoStatus) Summary() string {
	var parts []string
	if n := len(s.Modified); n > 0 {
		parts = append(parts, plural(n, "modified file"))
	}
	if n := len(s.Untracked); n > 0 {
		parts = append(parts, plural(n, "untracked file"))
	}
	if n := len(s.Conflicted); n > 0 {
		parts = append(parts, plural(n, "conflict"))
	}
	if n := len(s.Unpushed); n > 0 {
		parts = append(parts, plural(n, "unpushed commit"))
	}
	if n := len(s.Stashed); n > 0 {
		parts = append(parts, plural(n, "stash entry"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// isConflictCode reports whether a two-character `git status --porcelain`
// status code indicates an unresolved merge conflict.
func isConflictCode(code string) bool {
	switch code {
	case "UU", "AA", "DD", "AU", "UA", "UD", "DU":
		return true
	default:
		return false
	}
}

// Status reads repoPath's working tree, stash, and unpushed-commit state.
func Status(repoPath string) (RepoStatus, error) {
	var s RepoStatus

	out, err := run(repoPath, "git", "status", "--porcelain")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		switch {
		case isConflictCode(code):
			s.Conflicted = append(s.Conflicted, path)
		case code == "??":
			s.Untracked = append(s.Untracked, path)
		default:
			s.Modified = append(s.Modified, path)
		}
	}

	out, err = run(repoPath, "git", "stash", "list")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s.Stashed = append(s.Stashed, line)
		}
	}

	out, err = run(repoPath, "git", "log", "--branches", "--not", "--remotes", "--oneline")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s.Unpushed = append(s.Unpushed, line)
		}
	}

	return s, nil
}

// PullRebase replays local commits on top of the freshly fetched upstream.
// It is the retry primitive for shared state repositories where several
// machines push small non-conflicting commits.
func PullRebase(repoPath string) error {
	_, err := run(repoPath, "git", "pull", "--rebase", "--quiet")
	return err
}

// Push publishes the current branch to its upstream.
func Push(repoPath string) error {
	_, err := run(repoPath, "git", "push", "--quiet")
	return err
}

// RebaseAbort aborts an in-progress rebase, restoring repoPath to the state
// it was in before the rebase started. Callers use it after a failed
// PullRebase so a conflict never leaves the clone wedged mid-rebase.
func RebaseAbort(repoPath string) error {
	_, err := run(repoPath, "git", "rebase", "--abort")
	return err
}

// AddCommit stages paths and commits them. It reports false without
// committing when the stage is empty, so callers can publish idempotently.
func AddCommit(repoPath, message string, paths ...string) (bool, error) {
	args := append([]string{"add", "--all", "--"}, paths...)
	if _, err := run(repoPath, "git", args...); err != nil {
		return false, err
	}
	_, err := run(repoPath, "git", "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	switch exitCode(err) {
	case 1:
		// staged changes exist
	default:
		return false, fmt.Errorf("inspect staged changes: %w", err)
	}
	if _, err := run(repoPath, "git", "commit", "--quiet", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// HasUpstream reports whether the checked-out branch has a resolvable
// upstream (`@{u}`). A branch that has never been pushed, or one cloned from
// a bare repository with no branches yet, resolves to false rather than an
// error — that is the ordinary state of a first publish into an empty store,
// not a fault.
func HasUpstream(repoPath string) (bool, error) {
	_, err := run(repoPath, "git", "rev-parse", "--verify", "--quiet", "@{u}")
	if err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RebaseInProgress reports whether repoPath has a rebase underway, by
// checking for the two directories git creates while one is in progress
// (rebase-merge for interactive/merge-based rebases, rebase-apply for the
// classic am-based one). It resolves the paths with `git rev-parse
// --git-path` rather than assuming <repoPath>/.git, so it also works from a
// linked worktree, whose git-dir lives elsewhere.
func RebaseInProgress(repoPath string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		out, err := run(repoPath, "git", "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		p := strings.TrimSpace(out)
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoPath, p)
		}
		if _, err := os.Stat(p); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

// HeadSHA returns the full SHA of HEAD.
func HeadSHA(repoPath string) (string, error) {
	out, err := run(repoPath, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// ResetHardUpstream discards local commits and tree state, returning the
// branch to its upstream. Used only on the state-repo clone to roll back a
// claim commit that lost its CAS race.
func ResetHardUpstream(repoPath string) error {
	_, err := run(repoPath, "git", "reset", "--hard", "@{u}")
	return err
}
