// Package archiveprune audits and, only under explicit apply, deletes local
// clones of repositories confirmed archived on GitHub.
//
// Deleting a clone is destructive. What makes it acceptable here is narrow:
// an archived repository still exists on GitHub, so a deleted clone is
// re-clonable. That argument collapses the moment the clone holds something
// GitHub does not — an uncommitted edit, a stash, a commit or branch or tag
// that was never pushed, or a linked worktree or WB Work Log claim that still
// needs the clone to exist. Every check in Evaluate exists to protect that
// one argument; a check that cannot be completed (GitHub unreachable, a ref
// that cannot be resolved) makes the clone not deletable, never "probably
// fine" — failing closed is the only correct default for an operation this
// destructive.
package archiveprune

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// Options selects and drives one clean run.
type Options struct {
	ProjectsRoot string
	// Filter restricts to repositories whose "org/name" slug contains this
	// substring, matching the fleet-wide --filter convention used elsewhere.
	Filter string
	// Apply performs deletions. Without it, Clean only plans: it runs the
	// exact same evaluation and reports the exact same eligibility, but never
	// calls os.RemoveAll.
	Apply bool
	// DeleteUntracked authorizes the deliberately narrow exception to the usual
	// clean-working-tree predicate. It has no effect without Apply. Clean still
	// plans and itemizes untracked paths by default; this flag is the separate,
	// explicit authority required before it can delete them.
	DeleteUntracked bool
	// Progress, when set, receives one "[n/N] org/repo" line per repository as
	// it is evaluated, so a long fleet sweep is distinguishable from a hang.
	Progress io.Writer

	// beforeUntrackedRevalidation is a package-private test seam. Production
	// callers cannot set it; it lets the safety contract prove that a path
	// changed between plan and deletion is refused rather than guessed at.
	beforeUntrackedRevalidation func()
}

// UntrackedEntry is one exact untracked filesystem entry observed in a clone.
// Path is always relative to the clone root; Size is bytes for a regular file
// and the filesystem-reported size for a directory.
type UntrackedEntry struct {
	Path string `json:"path" yaml:"path"`
	Kind string `json:"kind" yaml:"kind"`
	Size int64  `json:"size" yaml:"size"`

	device uint64
	inode  uint64
	mode   uint32
	hash   string
}

// Result is the plan or outcome for one local clone.
type Result struct {
	Repository string `json:"repository" yaml:"repository"`
	Path       string `json:"path" yaml:"path"`
	// Eligible reports whether the clone satisfies every safety check,
	// independent of whether Apply was requested.
	Eligible bool `json:"eligible" yaml:"eligible"`
	// Applied is true only when Apply was requested, the clone was eligible,
	// and the deletion itself succeeded.
	Applied bool `json:"applied" yaml:"applied"`
	// Reason names, for an eligible clone, why it is safe; for an ineligible
	// one, every failing check, joined so the report is self-explanatory
	// without cross-referencing anything else.
	Reason string `json:"reason" yaml:"reason"`
	// Untracked itemizes every untracked file and directory found during the
	// plan. It is present for dry runs and refusals, never inferred from a count.
	Untracked []UntrackedEntry `json:"untracked,omitempty" yaml:"untracked,omitempty"`
	// ReceiptPath names the durable, itemized WB receipt written before an
	// explicitly authorised untracked deletion and finalized before pruning.
	ReceiptPath string `json:"receipt_path,omitempty" yaml:"receipt_path,omitempty"`
	// Error is set only when the deletion itself failed after the clone was
	// judged eligible.
	Error string `json:"error,omitempty" yaml:"error,omitempty"`

	untrackedOnly bool
}

// Outcome is a whole clean run.
type Outcome struct {
	Apply           bool     `json:"apply" yaml:"apply"`
	DeleteUntracked bool     `json:"delete_untracked" yaml:"delete_untracked"`
	Results         []Result `json:"results" yaml:"results"`
}

// Clean discovers local clones below options.ProjectsRoot, evaluates every
// one whose slug matches options.Filter, and — only under options.Apply —
// deletes the ones that pass every safety check. It never removes a clone it
// has not itself confirmed archived and clean in this exact run.
func Clean(ctx context.Context, options Options) (Outcome, error) {
	root, err := filepath.Abs(options.ProjectsRoot)
	if err != nil {
		return Outcome{}, err
	}
	repos, err := discover.ScanLocal(root)
	if err != nil {
		return Outcome{}, err
	}
	filtered := make([]discover.Repo, 0, len(repos))
	for _, repo := range repos {
		if options.Filter == "" || strings.Contains(repo.Slug(), options.Filter) {
			filtered = append(filtered, repo)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Slug() < filtered[j].Slug() })

	outcome := Outcome{Apply: options.Apply, DeleteUntracked: options.DeleteUntracked}
	for index, repo := range filtered {
		if options.Progress != nil {
			_, _ = fmt.Fprintf(options.Progress, "[%d/%d] %s\n", index+1, len(filtered), repo.Slug())
		}
		result := Evaluate(ctx, root, repo)
		if options.Apply {
			switch {
			case result.Eligible:
				if err := os.RemoveAll(repo.Path); err != nil {
					result.Error = err.Error()
				} else {
					result.Applied = true
				}
			case options.DeleteUntracked && result.untrackedOnly:
				result = cleanAuthorizedUntracked(ctx, root, repo, result, options)
			}
		}
		outcome.Results = append(outcome.Results, result)
	}
	return outcome, nil
}

// Evaluate judges a single clone against the full safety predicate. It never
// mutates anything; Clean is the only mutator, and only under Apply.
func Evaluate(ctx context.Context, projectsRoot string, repo discover.Repo) Result {
	result := Result{Repository: repo.Slug(), Path: repo.Path}

	skip, err := gitops.SkipSync(repo.Path)
	if err != nil {
		result.Reason = fmt.Sprintf("could not read wb.skip-sync marker: %v", err)
		return result
	}
	if skip {
		result.Reason = "marked wb.skip-sync; wb will not delete a clone you told it to leave alone"
		return result
	}

	archived, err := discover.IsArchived(repo.Slug())
	if err != nil {
		result.Reason = fmt.Sprintf("could not confirm archived status on GitHub: %v", err)
		return result
	}
	if !archived {
		result.Reason = "not archived on GitHub"
		return result
	}

	var blockers []string

	status, err := gitops.Status(repo.Path)
	if err != nil {
		result.Reason = fmt.Sprintf("could not read git status: %v", err)
		return result
	}
	if len(status.Untracked) > 0 {
		entries, planErr := planUntracked(repo.Path, status.Untracked)
		if planErr != nil {
			result.Reason = fmt.Sprintf("could not safely itemize untracked paths: %v", planErr)
			return result
		}
		result.Untracked = entries
	}
	blockers = append(blockers, workingTreeBlockers(status, false)...)
	if len(result.Untracked) > 0 {
		blockers = append(blockers, fmt.Sprintf("%s (pass --apply --delete-untracked to explicitly delete exactly the itemized paths)", plural(len(result.Untracked), "untracked path")))
	}
	blockers = append(blockers, unpushedBranchBlockers(status)...)
	if len(status.Stashed) > 0 {
		blockers = append(blockers, plural(len(status.Stashed), "stash entry"))
	}

	localOnly, err := localOnlyBranches(ctx, repo.Path)
	if err != nil {
		result.Reason = fmt.Sprintf("could not resolve remote branches: %v", err)
		return result
	}
	for _, branch := range localOnly {
		blockers = append(blockers, fmt.Sprintf("local-only branch %q does not exist on origin", branch))
	}

	unpushedTags, err := unpushedTagNames(ctx, repo.Path)
	if err != nil {
		result.Reason = fmt.Sprintf("could not resolve remote tags: %v", err)
		return result
	}
	for _, tag := range unpushedTags {
		blockers = append(blockers, fmt.Sprintf("local tag %q does not exist on origin", tag))
	}

	linked, err := linkedWorktreePaths(ctx, repo.Path)
	if err != nil {
		result.Reason = fmt.Sprintf("could not read linked worktrees: %v", err)
		return result
	}
	for _, worktree := range linked {
		blockers = append(blockers, fmt.Sprintf("linked worktree at %s", worktree))
	}

	claims, err := nonTerminalClaims(projectsRoot, repo.Slug())
	if err != nil {
		result.Reason = fmt.Sprintf("could not read WB Work Log claims: %v", err)
		return result
	}
	blockers = append(blockers, claims...)

	if len(blockers) > 0 {
		result.untrackedOnly = len(result.Untracked) > 0 && len(blockers) == 1
		result.Reason = strings.Join(blockers, "; ")
		return result
	}

	result.Eligible = true
	result.Reason = "archived and clean"
	return result
}

func workingTreeBlockers(status gitops.RepoStatus, includeUntracked bool) []string {
	var blockers []string
	if n := len(status.Modified); n > 0 {
		blockers = append(blockers, plural(n, "modified file"))
	}
	if n := len(status.Untracked); includeUntracked && n > 0 {
		blockers = append(blockers, plural(n, "untracked file"))
	}
	if n := len(status.Conflicted); n > 0 {
		blockers = append(blockers, plural(n, "conflicted file"))
	}
	return blockers
}

func unpushedBranchBlockers(status gitops.RepoStatus) []string {
	if len(status.UnpushedBranches) == 0 {
		if len(status.Unpushed) > 0 {
			// UnpushedWork found commits but could not attribute them to a
			// branch (should not normally happen); still refuse, generically.
			return []string{plural(len(status.Unpushed), "unpushed commit")}
		}
		return nil
	}
	blockers := make([]string, 0, len(status.UnpushedBranches))
	for _, branch := range status.UnpushedBranches {
		blockers = append(blockers, fmt.Sprintf("%s on branch %q", plural(len(branch.Commits), "unpushed commit"), branch.Branch))
	}
	return blockers
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// runGit runs git in dir with output captured, disabling every interactive
// prompt so a missing credential or unknown host key fails fast instead of
// blocking on a terminal nobody is watching.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = console.Env()
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// localOnlyBranches returns every local branch that does not exist by name on
// origin, regardless of whether its commits are also reachable via another
// ref: deleting the clone would otherwise silently discard a branch pointer
// GitHub never saw.
func localOnlyBranches(ctx context.Context, repoPath string) ([]string, error) {
	localOut, err := runGit(ctx, repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	remote, err := remoteRefNames(ctx, repoPath, "--heads", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(localOut, "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" {
			continue
		}
		if !remote[branch] {
			missing = append(missing, branch)
		}
	}
	return missing, nil
}

// unpushedTagNames returns every local tag that does not exist by name on
// origin.
func unpushedTagNames(ctx context.Context, repoPath string) ([]string, error) {
	localOut, err := runGit(ctx, repoPath, "tag")
	if err != nil {
		return nil, err
	}
	remote, err := remoteRefNames(ctx, repoPath, "--tags", "refs/tags/")
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, line := range strings.Split(localOut, "\n") {
		tag := strings.TrimSpace(line)
		if tag == "" {
			continue
		}
		if !remote[tag] {
			missing = append(missing, tag)
		}
	}
	return missing, nil
}

// remoteRefNames returns the short names origin publishes for the given
// `git ls-remote` scope flag (--heads or --tags), stripping the given prefix
// and any annotated-tag peel suffix ("^{}").
func remoteRefNames(ctx context.Context, repoPath, scopeFlag, prefix string) (map[string]bool, error) {
	out, err := runGit(ctx, repoPath, "ls-remote", scopeFlag, "origin")
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := strings.TrimSuffix(fields[1], "^{}")
		names[strings.TrimPrefix(ref, prefix)] = true
	}
	return names, nil
}

// linkedWorktreePaths returns every worktree `git worktree list` registers
// against repoPath other than the canonical checkout itself.
func linkedWorktreePaths(ctx context.Context, repoPath string) ([]string, error) {
	out, err := runGit(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		paths   []string
		primary = true
		current string
	)
	flush := func() {
		if current == "" {
			return
		}
		if primary {
			primary = false // the first record is the canonical checkout itself
		} else {
			paths = append(paths, current)
		}
		current = ""
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			flush()
			current = filepath.Clean(strings.TrimPrefix(line, "worktree "))
		}
	}
	flush()
	return paths, nil
}

// worklogClaim is the subset of a claim JSON's fields needed to decide
// whether it still references a repository. See internal/worktrees/worklog.go
// for the authoritative writer of this file.
type worklogClaim struct {
	ClaimID    string `json:"claim_id"`
	Repository string `json:"repository"`
	Task       string `json:"task"`
	Worktree   string `json:"worktree"`
	Lifecycle  string `json:"lifecycle"`
}

type worklogTerminal struct {
	ClaimID    string `json:"claim_id"`
	Repository string `json:"repository"`
	Task       string `json:"task"`
	Worktree   string `json:"worktree"`
	Lifecycle  string `json:"lifecycle"`
}

// nonTerminalClaims scans every WB home layout (current and, when present,
// legacy) for a Work Log claim recorded against slug whose lifecycle is not
// yet terminal, and returns one human-readable blocker per match. A claim
// still in this state names a task WB or its operator has not finished with
// this repository, whether or not its worktree directory still physically
// exists — an active claim pointing at a worktree someone deleted by hand is
// exactly the case a git-only check like linkedWorktreePaths cannot see.
func nonTerminalClaims(projectsRoot, slug string) ([]string, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	seenHome := map[string]bool{}
	var blockers []string
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if seenHome[home] {
			continue
		}
		seenHome[home] = true
		matches, err := filepath.Glob(filepath.Join(home, "worklogs", "*", "runs", "*", "claims", "*.json"))
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read claim %s: %w", path, err)
			}
			var claim worklogClaim
			if err := json.Unmarshal(raw, &claim); err != nil {
				return nil, fmt.Errorf("parse claim %s: %w", path, err)
			}
			if claim.Repository != slug {
				continue
			}
			terminalPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "terminals", claim.ClaimID+".json")
			if claim.ClaimID == "" {
				return nil, fmt.Errorf("claim %s has no claim_id", path)
			}
			terminalRaw, terminalErr := os.ReadFile(terminalPath)
			if terminalErr == nil {
				var terminal worklogTerminal
				if err := json.Unmarshal(terminalRaw, &terminal); err != nil {
					return nil, fmt.Errorf("parse terminal seal %s: %w", terminalPath, err)
				}
				if terminal.ClaimID != claim.ClaimID || terminal.Repository != claim.Repository || terminal.Task != claim.Task || terminal.Worktree != claim.Worktree || terminal.Lifecycle != "terminal" {
					return nil, fmt.Errorf("terminal seal %s does not match claim %s", terminalPath, path)
				}
				continue
			}
			if !os.IsNotExist(terminalErr) {
				return nil, fmt.Errorf("read terminal seal %s: %w", terminalPath, terminalErr)
			}
			if claim.Lifecycle == "terminal" {
				continue
			}
			blockers = append(blockers, fmt.Sprintf(
				"non-terminal Work Log claim for task %q (worktree %s)", claim.Task, claim.Worktree))
		}
	}
	sort.Strings(blockers)
	return blockers, nil
}
