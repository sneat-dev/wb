// Package canonicalrescue moves uncommitted work out of a canonical clone
// onto a branch, without discarding anything and without disturbing the clone.
//
// # Why preservation is the whole design
//
// On 2026-08-27 a complete, unlanded 42-line lesson sat untracked in a
// canonical clone. It survived by luck: the next routine `git checkout` would
// have taken it, and no copy existed anywhere else. That is the cost this
// package exists to avoid, and it is why nothing here discards by default and
// why the discarding step is separated from the preserving one by an explicit
// flag and a proved receipt.
//
// # How the content is captured without touching the clone
//
// The obvious approaches are all unsafe here. `git stash` writes to a
// repository-global stash stack that every linked worktree shares, so a rescue
// in one clone shows up as a surprise entry for whoever looks next.
// `git checkout -b` moves the clone's HEAD, which is the state WB's own guard
// requires to stay put. And `git stash create` does not capture untracked
// files, which is exactly what nearly went missing.
//
// So the capture runs entirely through a temporary index. The clone's real
// index is copied to a scratch file, the working tree is staged into that copy,
// a tree is written from it, and a commit is created with `git commit-tree`
// parented on HEAD. The result is a real branch holding the exact content —
// modified, staged, and untracked alike — while the clone's HEAD, branch,
// index, and working tree are all still byte-for-byte what they were.
package canonicalrescue

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/console"
)

const (
	// PushBranchEnv and PushCommitEnv attest that a pre-push invocation was
	// opened by WB's rescue transport. The hook still proves the exact ref,
	// commit parent, and full captured tree before accepting this route.
	PushBranchEnv = "WB_CANONICAL_RESCUE_BRANCH"
	PushCommitEnv = "WB_CANONICAL_RESCUE_COMMIT"
)

// Change is one path the clone holds that HEAD does not.
type Change struct {
	// Status is the two-character porcelain code, "??" for untracked.
	Status string
	Path   string
}

// Untracked reports whether Git knows nothing about this path, which is the
// case where a discard is unrecoverable.
func (c Change) Untracked() bool { return c.Status == "??" }

// Report is what a canonical clone currently holds.
type Report struct {
	Path       string `json:"path"`
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Head       string `json:"head,omitempty"`
	// Changes excludes ignored paths, so a generated .worktree.md never reads
	// as work needing rescue.
	Changes []Change `json:"changes,omitempty"`
	// UntrackedCount is called out separately because untracked content is the
	// part no reflog, no stash, and no remote can bring back.
	UntrackedCount int `json:"untracked_count"`
	// RescueBranch is the branch a rescue would create or did create.
	RescueBranch string `json:"rescue_branch,omitempty"`
	// RescueCommit is set once the content has been captured.
	RescueCommit string `json:"rescue_commit,omitempty"`
	// Pushed is true once the rescue branch exists on origin.
	Pushed bool `json:"pushed"`
	// Restored is true once the clone was returned to a clean base.
	Restored bool `json:"restored"`
}

// Dirty reports whether there is anything to rescue.
func (r Report) Dirty() bool { return len(r.Changes) > 0 }

// Options configures Inspect and Rescue.
type Options struct {
	ProjectsRoot string
	// Branch is the rescue branch name; empty derives one from the clock.
	Branch string
	// Now supplies the derived branch's timestamp; nil means time.Now.
	Now func() time.Time
}

// Inspect reports what a canonical clone holds, and refuses any other path.
//
// Refusing a linked worktree is deliberate. A worktree with uncommitted work is
// simply work in progress; there is nothing to rescue and nothing at risk.
func Inspect(ctx context.Context, path string, options Options) (Report, error) {
	location := agentguard.Classify(options.ProjectsRoot, path)
	if location.Kind != agentguard.KindCanonical {
		return Report{}, fmt.Errorf(
			"%s is not a canonical clone under %s; rescue exists for the shared clone every worktree is cut from, and a linked worktree's uncommitted work is simply work in progress",
			path, options.ProjectsRoot,
		)
	}
	report := Report{Path: location.Root, Repository: location.Slug()}
	branch, err := git(ctx, location.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Report{}, err
	}
	report.Branch = branch
	head, err := git(ctx, location.Root, "rev-parse", "HEAD")
	if err != nil {
		return Report{}, err
	}
	report.Head = head
	// --porcelain never lists an ignored path, so WB's own generated marker is
	// correctly absent from what a rescue would capture.
	// Read status WITHOUT trimming: porcelain v1 encodes the index state in
	// column one, so a leading space is data. Trimming it shifts every path by
	// one character, which silently turns README.md into EADME.md and makes the
	// restore gate refuse a capture that was in fact complete.
	status, err := gitRaw(ctx, location.Root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return Report{}, err
	}
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		change := Change{Status: line[:2], Path: strings.TrimSpace(line[3:])}
		if change.Untracked() {
			report.UntrackedCount++
		}
		report.Changes = append(report.Changes, change)
	}
	report.RescueBranch = rescueBranchName(options)
	return report, nil
}

func rescueBranchName(options Options) string {
	if strings.TrimSpace(options.Branch) != "" {
		return strings.TrimSpace(options.Branch)
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	return "rescue/canonical-" + now().UTC().Format("20060102-150405")
}

// Capture moves the clone's uncommitted content onto a branch and leaves the
// clone exactly as it found it.
//
// The clone stays dirty afterwards on purpose. Preserving and discarding are
// two decisions, and collapsing them into one is how a rescue turns into the
// loss it was meant to prevent. Restore performs the second, separately.
func Capture(ctx context.Context, report Report) (Report, error) {
	if !report.Dirty() {
		return report, fmt.Errorf("%s has nothing to rescue", report.Path)
	}
	existing, err := branchCommit(ctx, report.Path, report.RescueBranch)
	if err != nil {
		return report, err
	}

	tree, err := capturedTree(ctx, report)
	if err != nil {
		return report, err
	}
	// A named branch that already holds exactly this tree is the second run of
	// the two-step flow — capture, review, then restore — so it is reused
	// rather than refused. A branch holding anything else is somebody's work
	// and is never written over.
	if existing != "" {
		existingTree, treeErr := git(ctx, report.Path, "rev-parse", existing+"^{tree}")
		if treeErr != nil {
			return report, fmt.Errorf("read the tree of branch %s: %w", report.RescueBranch, treeErr)
		}
		if existingTree != tree {
			return report, fmt.Errorf(
				"branch %q already exists in %s and holds different content; pass --branch to name a different one",
				report.RescueBranch, report.Path,
			)
		}
		report.RescueCommit = existing
		report.Pushed = remoteHasCommit(ctx, report.Path, report.RescueBranch, existing)
		return report, nil
	}

	message := rescueMessage(report)
	commit, err := git(ctx, report.Path, "commit-tree", tree, "-p", report.Head, "-m", message)
	if err != nil {
		return report, fmt.Errorf("record the rescue commit for %s: %w", report.Path, err)
	}
	if _, err := git(ctx, report.Path, "branch", report.RescueBranch, commit); err != nil {
		return report, fmt.Errorf("create branch %s in %s: %w", report.RescueBranch, report.Path, err)
	}
	report.RescueCommit = commit
	return report, nil
}

// remoteHasCommit reports whether a remote-tracking ref already points at the
// rescue commit, which is what lets a second run know the content is off this
// machine without pushing again.
func remoteHasCommit(ctx context.Context, root, branch, commit string) bool {
	for _, remote := range []string{"origin"} {
		if resolved, err := git(ctx, root, "rev-parse", "--verify", "--quiet", remote+"/"+branch); err == nil && resolved == commit {
			return true
		}
	}
	return false
}

func rescueMessage(report Report) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "rescue: uncommitted work found in canonical clone %s\n\n", report.Path)
	fmt.Fprintf(&builder, "Captured from %s at %s without altering the clone's HEAD,\n", report.Branch, shortSHA(report.Head))
	builder.WriteString("branch, index, or working tree.\n\n")
	fmt.Fprintf(&builder, "%d path(s), %d of them untracked:\n", len(report.Changes), report.UntrackedCount)
	for index, change := range report.Changes {
		if index == 40 {
			fmt.Fprintf(&builder, "  … and %d more\n", len(report.Changes)-index)
			break
		}
		fmt.Fprintf(&builder, "  %s %s\n", change.Status, change.Path)
	}
	return builder.String()
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Push publishes the rescue branch, which is what turns a local capture into
// something a lost machine cannot take with it.
func Push(ctx context.Context, report Report, remote string) (Report, error) {
	if report.RescueCommit == "" {
		return report, fmt.Errorf("nothing has been captured yet")
	}
	remoteRef := "refs/heads/" + report.RescueBranch
	remoteCommit, err := remoteBranchCommit(ctx, report.Path, remote, remoteRef)
	if err != nil {
		return report, err
	}
	if remoteCommit == report.RescueCommit {
		report.Pushed = true
		return report, nil
	}
	if remoteCommit != "" {
		return report, fmt.Errorf("refusing to replace %s on %s: remote holds %s, rescue commit is %s", remoteRef, remote, remoteCommit, report.RescueCommit)
	}
	pushEnvironment := []string{
		PushBranchEnv + "=" + report.RescueBranch,
		PushCommitEnv + "=" + report.RescueCommit,
	}
	lease := "--force-with-lease=" + remoteRef + ":"
	refspec := remoteRef + ":" + remoteRef
	if _, err := gitWithEnvironment(ctx, report.Path, pushEnvironment, "push", lease, remote, refspec); err != nil {
		return report, fmt.Errorf("push %s to %s: %w", report.RescueBranch, remote, err)
	}
	remoteCommit, err = remoteBranchCommit(ctx, report.Path, remote, remoteRef)
	if err != nil {
		return report, err
	}
	if remoteCommit != report.RescueCommit {
		return report, fmt.Errorf("push %s to %s returned without exact remote receipt: observed %q, want %s", report.RescueBranch, remote, remoteCommit, report.RescueCommit)
	}
	report.Pushed = true
	return report, nil
}

// VerifyAttestedPush proves that a pre-push operation is publishing only the
// exact rescue commit which captures the canonical clone's complete dirty
// state. It is the rescue route through managed hooks, not a hook bypass.
func VerifyAttestedPush(ctx context.Context, root, projectsRoot, branch, commit string, input io.Reader) error {
	branch, commit = strings.TrimSpace(branch), strings.TrimSpace(commit)
	if !strings.HasPrefix(branch, "rescue/") || commit == "" {
		return fmt.Errorf("invalid canonical rescue push attestation")
	}
	if _, err := git(ctx, root, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid canonical rescue branch %q: %w", branch, err)
	}
	report, err := Inspect(ctx, root, Options{ProjectsRoot: projectsRoot, Branch: branch})
	if err != nil {
		return err
	}
	if !report.Dirty() {
		return fmt.Errorf("canonical rescue push attestation requires the dirty clone it preserves")
	}
	localCommit, err := branchCommit(ctx, root, branch)
	if err != nil {
		return err
	}
	if localCommit != commit {
		return fmt.Errorf("canonical rescue branch %s points to %s, attestation names %s", branch, localCommit, commit)
	}
	parents, err := git(ctx, root, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return err
	}
	parentFields := strings.Fields(parents)
	if len(parentFields) != 2 || parentFields[0] != commit || parentFields[1] != report.Head {
		return fmt.Errorf("canonical rescue commit %s is not a single-parent capture of canonical HEAD %s", commit, report.Head)
	}
	captured, err := capturedTree(ctx, report)
	if err != nil {
		return err
	}
	commitTree, err := git(ctx, root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return err
	}
	if captured != commitTree {
		return fmt.Errorf("canonical rescue commit %s tree %s does not equal the clone's complete captured tree %s", commit, commitTree, captured)
	}
	contents, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("read canonical rescue pre-push refs: %w", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return fmt.Errorf("canonical rescue push must update exactly one ref, observed %d", len(lines))
	}
	fields := strings.Fields(lines[0])
	wantRef := "refs/heads/" + branch
	if len(fields) != 4 || fields[0] != wantRef || fields[1] != commit || fields[2] != wantRef {
		return fmt.Errorf("canonical rescue push must publish only %s at %s", wantRef, commit)
	}
	return nil
}

func PushAttestationFromEnvironment() (branch, commit string, present bool, err error) {
	branch, commit = os.Getenv(PushBranchEnv), os.Getenv(PushCommitEnv)
	if branch == "" && commit == "" {
		return "", "", false, nil
	}
	if branch == "" || commit == "" {
		return "", "", true, fmt.Errorf("incomplete canonical rescue push attestation")
	}
	return branch, commit, true, nil
}

func capturedTree(ctx context.Context, report Report) (string, error) {
	scratch, err := os.MkdirTemp("", "wb-rescue-*")
	if err != nil {
		return "", fmt.Errorf("stage a temporary index: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	indexPath := filepath.Join(scratch, "index")
	if err := copyFile(filepath.Join(report.Path, ".git", "index"), indexPath); err != nil {
		if _, readTreeErr := gitWithIndex(ctx, report.Path, indexPath, "read-tree", "HEAD"); readTreeErr != nil {
			return "", fmt.Errorf("build a temporary index for %s: %w", report.Path, readTreeErr)
		}
	}
	if _, err := gitWithIndex(ctx, report.Path, indexPath, "add", "--all", "--", "."); err != nil {
		return "", fmt.Errorf("stage %s into the temporary index: %w", report.Path, err)
	}
	tree, err := gitWithIndex(ctx, report.Path, indexPath, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write a tree for %s: %w", report.Path, err)
	}
	return tree, nil
}

func remoteBranchCommit(ctx context.Context, root, remote, remoteRef string) (string, error) {
	output, err := git(ctx, root, "ls-remote", "--heads", "--", remote, remoteRef)
	if err != nil {
		return "", fmt.Errorf("read %s from %s: %w", remoteRef, remote, err)
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != remoteRef {
		return "", fmt.Errorf("unexpected ls-remote receipt for %s on %s: %q", remoteRef, remote, output)
	}
	return fields[0], nil
}

// Restore returns the clone to a clean checkout of its own HEAD, and refuses to
// unless the content is provably somewhere else first.
//
// `git clean` runs WITHOUT -x. Ignored paths — WB's own generated marker among
// them — are left alone; only the content the rescue commit now holds is
// removed.
func Restore(ctx context.Context, report Report, allowUnpushed bool) (Report, error) {
	if report.RescueCommit == "" {
		return report, fmt.Errorf("refusing to restore %s: nothing has been captured, so this would discard the only copy", report.Path)
	}
	if !report.Pushed && !allowUnpushed {
		return report, fmt.Errorf(
			"refusing to restore %s: the rescue branch %s exists only on this machine. Push it, or pass --allow-unpushed to accept that risk",
			report.Path, report.RescueBranch,
		)
	}
	if err := verifyCaptured(ctx, report); err != nil {
		return report, err
	}
	if _, err := git(ctx, report.Path, "reset", "--hard", report.Head); err != nil {
		return report, fmt.Errorf("reset %s: %w", report.Path, err)
	}
	if _, err := git(ctx, report.Path, "clean", "-fd"); err != nil {
		return report, fmt.Errorf("clean %s: %w", report.Path, err)
	}
	report.Restored = true
	return report, nil
}

// verifyCaptured re-reads the rescue commit and checks every path the report
// named is actually in it. Trusting the earlier write would make a partial
// capture indistinguishable from a complete one at the exact moment that
// difference destroys work.
func verifyCaptured(ctx context.Context, report Report) error {
	listing, err := git(ctx, report.Path, "ls-tree", "-r", "--name-only", report.RescueCommit)
	if err != nil {
		return fmt.Errorf("read back the rescue commit: %w", err)
	}
	captured := map[string]bool{}
	for _, path := range strings.Split(listing, "\n") {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			captured[trimmed] = true
		}
	}
	var missing []string
	for _, change := range report.Changes {
		// A deletion is captured by being absent; only content that still
		// exists must be present in the tree.
		if strings.HasPrefix(change.Status, "D") || strings.HasSuffix(change.Status, "D") {
			continue
		}
		path := strings.Trim(change.Path, `"`)
		if strings.HasSuffix(path, "/") {
			continue
		}
		if !captured[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"refusing to restore %s: %d path(s) are not in rescue commit %s, starting with %s",
			report.Path, len(missing), shortSHA(report.RescueCommit), missing[0],
		)
	}
	return nil
}

// branchCommit resolves a local branch to its commit, or "" when it does not
// exist. A failed lookup is not an error: `git rev-parse --verify --quiet`
// exits non-zero for an absent ref, which is the answer, not a fault.
func branchCommit(ctx context.Context, root, branch string) (string, error) {
	output, err := git(ctx, root, "branch", "--list", "--format=%(objectname)", branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func copyFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, contents, 0o600)
}

func git(ctx context.Context, root string, arguments ...string) (string, error) {
	return gitWithIndex(ctx, root, "", arguments...)
}

// gitRaw returns Git's output byte for byte, for the callers where leading
// whitespace carries meaning.
func gitRaw(ctx context.Context, root string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Env = console.Env()
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}

func gitWithIndex(ctx context.Context, root, indexPath string, arguments ...string) (string, error) {
	extraEnvironment := []string(nil)
	if indexPath != "" {
		extraEnvironment = append(extraEnvironment, "GIT_INDEX_FILE="+indexPath)
	}
	return gitWithEnvironment(ctx, root, extraEnvironment, arguments...)
}

func gitWithEnvironment(ctx context.Context, root string, extraEnvironment []string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(console.Env(), extraEnvironment...)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output)), nil
}
