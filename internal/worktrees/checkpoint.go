package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// checkpointRefPrefix is the dedicated non-branch namespace every checkpoint
// commit lives under. Nothing here is ever a Git branch, so a checkpoint can
// never accidentally become a landable commit or grow a stray pull request.
const checkpointRefPrefix = "refs/wb/checkpoints/"

// checkpointSubjectPrefix marks a checkpoint commit unmistakably, even if it
// is ever seen out of its dedicated namespace (e.g. in `git log --all`).
const checkpointSubjectPrefix = "wip(checkpoint): "

// Checkpoint commits deliberately do not carry the caller's Git identity: a
// checkpoint is not an authored contribution, and using a fixed identity
// means it never depends on user.name/user.email being configured.
const (
	checkpointAuthorName  = "wb checkpoint"
	checkpointAuthorEmail = "wb-checkpoint@localhost"
)

const defaultCheckpointRemote = "origin"

// CheckpointOptions configures wb worktree checkpoint.
type CheckpointOptions struct {
	Worktree string
	Message  string
	// Remote defaults to "origin" when empty.
	Remote string
	// Push defaults to true at the CLI layer; the caller decides.
	Push bool
}

// CheckpointResult is the receipt for one checkpoint attempt.
type CheckpointResult struct {
	Worktree        string   `json:"worktree"`
	Scope           string   `json:"scope"`
	Ref             string   `json:"ref"`
	Commit          string   `json:"commit"`
	Created         bool     `json:"created"`
	Dirty           bool     `json:"dirty"`
	Head            string   `json:"head,omitempty"`
	Pushed          bool     `json:"pushed"`
	PushSkipped     bool     `json:"push_skipped,omitempty"`
	PushError       string   `json:"push_error,omitempty"`
	UpstreamGone    bool     `json:"upstream_gone,omitempty"`
	UnpushedCommits int      `json:"unpushed_commits,omitempty"`
	Message         string   `json:"message,omitempty"`
	Notes           []string `json:"notes,omitempty"`
}

// Checkpoint captures the exact current state of one worktree — tracked
// changes, deletions, and untracked files not excluded by .gitignore — as a
// commit under refs/wb/checkpoints/<scope>/<timestamp>. It never runs build,
// test, or lint, never touches refs/heads/*, and never mutates the caller's
// real index or working tree. See spec/features/worktree-checkpoint.
func Checkpoint(ctx context.Context, options CheckpointOptions) (CheckpointResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return CheckpointResult{}, err
	}
	scope, err := checkpointScope(ctx, root)
	if err != nil {
		return CheckpointResult{}, err
	}
	headSHA := ""
	if head, headErr := git(ctx, root, "rev-parse", "HEAD"); headErr == nil {
		headSHA = strings.TrimSpace(head)
	}
	headTree := emptyTreeSHA
	if headSHA != "" {
		if tree, treeErr := git(ctx, root, "rev-parse", headSHA+"^{tree}"); treeErr == nil {
			headTree = strings.TrimSpace(tree)
		}
	}
	snapshotTree, err := buildCheckpointTree(ctx, root, headSHA)
	if err != nil {
		return CheckpointResult{}, fmt.Errorf("snapshot worktree for checkpoint: %w", err)
	}
	dirty := snapshotTree != headTree

	result := CheckpointResult{Worktree: root, Scope: scope, Dirty: dirty, Head: headSHA}

	existingRef, existingCommit, existingTree, existingParent, findErr := latestCheckpointRef(ctx, root, scope)
	if findErr != nil {
		return CheckpointResult{}, findErr
	}

	reuse := existingRef != "" && existingTree == snapshotTree && existingParent == headSHA
	if reuse {
		result.Ref = existingRef
		result.Commit = existingCommit
		result.Created = false
		result.Notes = append(result.Notes, "unchanged since last checkpoint; reused "+existingRef)
	} else {
		message := strings.TrimSpace(options.Message)
		if message == "" {
			message = "checkpoint"
		}
		body := checkpointSubjectPrefix + message + "\n\n" +
			"WB-Checkpoint-Scope: " + scope + "\n" +
			"WB-Checkpoint-Head: " + headSHAOrNone(headSHA) + "\n" +
			"WB-Checkpoint-Dirty: " + strconv.FormatBool(dirty) + "\n" +
			"WB-Checkpoint-At: " + time.Now().UTC().Format(time.RFC3339) + "\n"

		commitArgs := []string{"commit-tree", snapshotTree}
		if headSHA != "" {
			commitArgs = append(commitArgs, "-p", headSHA)
		}
		commitArgs = append(commitArgs, "-m", body)
		commitSHA, commitErr := gitWithEnv(ctx, root, checkpointIdentityEnv(), commitArgs...)
		if commitErr != nil {
			return CheckpointResult{}, fmt.Errorf("write checkpoint commit: %w", commitErr)
		}
		commitSHA = strings.TrimSpace(commitSHA)

		refName := checkpointRefPrefix + scope + "/" + checkpointTimestampLeaf(time.Now())
		if _, err := git(ctx, root, "update-ref", refName, commitSHA); err != nil {
			return CheckpointResult{}, fmt.Errorf("record checkpoint ref: %w", err)
		}
		result.Ref = refName
		result.Commit = commitSHA
		result.Created = true
		result.Message = message
	}

	if options.Push {
		remote := strings.TrimSpace(options.Remote)
		if remote == "" {
			remote = defaultCheckpointRemote
		}
		refspec := result.Commit + ":" + result.Ref
		if _, pushErr := gitWithEnv(ctx, root, nil, "push", "--no-verify", remote, refspec); pushErr != nil {
			result.PushError = pushErr.Error()
		} else {
			result.Pushed = true
		}
	} else {
		result.PushSkipped = true
	}

	gone, unpushed := checkpointUpstreamGone(ctx, root, scope)
	result.UpstreamGone = gone
	result.UnpushedCommits = unpushed

	return result, nil
}

// headSHAOrNone renders an unborn-branch checkpoint's HEAD trailer legibly
// instead of leaving it silently empty.
func headSHAOrNone(head string) string {
	if head == "" {
		return "none"
	}
	return head
}

// checkpointScope names the namespace a checkpoint belongs to: the current
// branch (including an unborn one — HEAD is still a symbolic ref to it), or
// a stable detached-HEAD fallback.
func checkpointScope(ctx context.Context, root string) (string, error) {
	if branch, err := git(ctx, root, "branch", "--show-current"); err == nil {
		if trimmed := strings.TrimSpace(branch); trimmed != "" {
			return trimmed, nil
		}
	}
	head, err := git(ctx, root, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return "", fmt.Errorf("checkpoint requires a branch or a resolvable HEAD: %w", err)
	}
	return "detached-" + strings.TrimSpace(head), nil
}

// buildCheckpointTree stages the working tree exactly as found — tracked
// modifications, deletions, and untracked files not excluded by .gitignore —
// into a scratch index and returns the resulting tree object. It never
// touches the caller's real index (GIT_INDEX_FILE points at a private
// temporary file for the whole operation), so a checkpoint has zero effect
// on any staged-vs-unstaged distinction the caller was relying on.
func buildCheckpointTree(ctx context.Context, root, headSHA string) (string, error) {
	scratch, err := os.CreateTemp("", "wb-checkpoint-index-*")
	if err != nil {
		return "", err
	}
	scratchPath := scratch.Name()
	_ = scratch.Close()
	defer func() { _ = os.Remove(scratchPath) }()

	env := []string{"GIT_INDEX_FILE=" + scratchPath}
	if headSHA != "" {
		if _, err := gitWithEnv(ctx, root, env, "read-tree", headSHA); err != nil {
			return "", fmt.Errorf("seed scratch index from HEAD: %w", err)
		}
	}
	if _, err := gitWithEnv(ctx, root, env, "add", "--all", "--"); err != nil {
		return "", fmt.Errorf("stage working tree into scratch index: %w", err)
	}
	tree, err := gitWithEnv(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write scratch tree: %w", err)
	}
	return strings.TrimSpace(tree), nil
}

// latestCheckpointRef returns the most recent checkpoint ref for scope, its
// commit, tree, and first parent (the HEAD it was taken against). All four
// are empty when no checkpoint exists yet for that scope.
func latestCheckpointRef(ctx context.Context, root, scope string) (ref, commit, tree, parent string, err error) {
	prefix := checkpointRefPrefix + scope + "/"
	out, listErr := git(ctx, root, "for-each-ref", "--sort=-refname", "--format=%(refname) %(objectname)", prefix)
	if listErr != nil {
		return "", "", "", "", fmt.Errorf("list checkpoint refs for %s: %w", scope, listErr)
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return "", "", "", "", nil
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 {
		return "", "", "", "", fmt.Errorf("unexpected checkpoint ref listing %q", lines[0])
	}
	ref, commit = fields[0], fields[1]
	treeOut, err := git(ctx, root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return "", "", "", "", fmt.Errorf("inspect checkpoint tree %s: %w", commit, err)
	}
	tree = strings.TrimSpace(treeOut)
	parentsOut, err := git(ctx, root, "log", "-1", "--format=%P", commit)
	if err != nil {
		return "", "", "", "", fmt.Errorf("inspect checkpoint parent %s: %w", commit, err)
	}
	parents := strings.Fields(strings.TrimSpace(parentsOut))
	if len(parents) > 0 {
		parent = parents[0]
	}
	return ref, commit, tree, parent, nil
}

// checkpointTimestampLeaf is deliberately fixed-width so lexical ref sorting
// (used by latestCheckpointRef and CheckpointList) matches chronological
// order, and includes nanoseconds so two checkpoints in the same second
// never collide.
func checkpointTimestampLeaf(at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("%s-%09d", at.Format("20060102T150405Z"), at.Nanosecond())
}

// checkpointUpstreamGone detects the exact shape of the incident this
// feature exists to prevent: a branch that still names a configured
// upstream, but whose remote-tracking ref no longer exists locally — most
// often because the remote branch was deleted and later pruned. Both checks
// use only already-known local refs; neither performs a new network round
// trip, so this stays cheap enough to run on every checkpoint.
func checkpointUpstreamGone(ctx context.Context, root, scope string) (gone bool, unpushed int) {
	upstream, err := git(ctx, root, "for-each-ref", "--format=%(upstream)", "refs/heads/"+scope)
	if err != nil {
		return false, 0
	}
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return false, 0
	}
	if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", upstream); err == nil {
		return false, 0
	}
	// The configured upstream ref is gone. Count commits on HEAD that no
	// other known remote-tracking ref carries, as a cheap, local-only proxy
	// for "commits nobody else has a copy of."
	countOut, err := git(ctx, root, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return true, 0
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(countOut))
	if convErr != nil {
		return true, 0
	}
	return true, count
}

func checkpointIdentityEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=" + checkpointAuthorName,
		"GIT_AUTHOR_EMAIL=" + checkpointAuthorEmail,
		"GIT_COMMITTER_NAME=" + checkpointAuthorName,
		"GIT_COMMITTER_EMAIL=" + checkpointAuthorEmail,
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// gitWithEnv runs Git with additional environment entries appended after the
// normal WB command environment, so callers can point GIT_INDEX_FILE at a
// scratch file or override commit identity without disturbing anything else.
func gitWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = append(console.Env(), extraEnv...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s in %s: %s", strings.Join(args, " "), dir, detail)
	}
	return strings.TrimSpace(string(output)), nil
}

// CheckpointAllOptions configures a fleet-wide sweep across every worktree
// WB already knows about — the primitive an external OS scheduler (launchd,
// cron, a systemd timer) calls on an interval. WB itself runs no daemon; see
// spec/features/worktree-checkpoint REQ: no-daemon.
type CheckpointAllOptions struct {
	ProjectsRoot string
	Push         bool
	Remote       string
}

// CheckpointAllResult pairs each known worktree with its own outcome so one
// failing repository never hides the rest.
type CheckpointAllResult struct {
	ListResult
	Checkpoint CheckpointResult `json:"checkpoint"`
	Error      string           `json:"error,omitempty"`
}

// CheckpointAll checkpoints every locally known WB worktree, continuing past
// a single repository's failure so a fleet-wide sweep is never all-or-nothing.
func CheckpointAll(ctx context.Context, options CheckpointAllOptions) ([]CheckpointAllResult, error) {
	entries, err := List(ctx, ListOptions{ProjectsRoot: options.ProjectsRoot})
	if err != nil {
		return nil, err
	}
	results := make([]CheckpointAllResult, 0, len(entries))
	for _, entry := range entries {
		outcome := CheckpointAllResult{ListResult: entry}
		checkpointResult, checkpointErr := Checkpoint(ctx, CheckpointOptions{
			Worktree: entry.WorktreeDir, Push: options.Push, Remote: options.Remote,
		})
		if checkpointErr != nil {
			outcome.Error = checkpointErr.Error()
		} else {
			outcome.Checkpoint = checkpointResult
		}
		results = append(results, outcome)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Task < results[j].Task })
	return results, nil
}

// emptyTreeSHA is Git's well-known SHA-1 empty tree object, used as the HEAD
// tree baseline for an unborn branch (no commits yet), so "dirty" is still
// well-defined before the first real commit exists.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
