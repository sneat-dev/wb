package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

// SetUmbrellaResult summarizes attaching an umbrella reference to a task.
type SetUmbrellaResult struct {
	Task         string   `json:"task"`
	Umbrella     string   `json:"umbrella"`
	Claims       int      `json:"claims"`
	Repositories []string `json:"repositories"`
}

// ResolveUmbrellaDefaultRegistry resolves the default umbrella registry by
// layering user/fleet config (~/.config/wb/worktrees.yaml) and any optional
// repository configs (<repoPath>/.wb/worktrees.yaml). Repository configuration
// overrides fleet configuration; conflicting repository defaults are refused.
func ResolveUmbrellaDefaultRegistry(repoPaths ...string) (string, error) {
	return resolveUmbrellaDefaultRegistry(configuredUserWorktreesConfig, repoPaths...)
}

func resolveUmbrellaDefaultRegistry(loadUser func() (branchConfigFile, bool, string, error), repoPaths ...string) (string, error) {
	registry := umbrella.DefaultRegistry
	userCfg, found, _, err := loadUser()
	if err != nil {
		return "", err
	}
	if found && userCfg.Umbrella.DefaultRegistry != nil {
		registry = *userCfg.Umbrella.DefaultRegistry
	}
	var repoRegistry, repoSource string
	for _, repoPath := range repoPaths {
		trimmed := strings.TrimSpace(repoPath)
		if trimmed == "" {
			continue
		}
		cfgPath := filepath.Join(trimmed, ".wb", "worktrees.yaml")
		cfg, ok, loadErr := loadBranchConfigFile(cfgPath)
		if loadErr != nil {
			return "", loadErr
		}
		if !ok || cfg.Umbrella.DefaultRegistry == nil {
			continue
		}
		value := *cfg.Umbrella.DefaultRegistry
		if repoRegistry != "" && repoRegistry != value {
			return "", fmt.Errorf("repositories disagree on umbrella.default_registry (%s sets %q, %s sets %q); pass an explicit <registry>:<id> reference",
				repoSource, repoRegistry, cfgPath, value)
		}
		repoRegistry = value
		repoSource = cfgPath
	}
	if repoRegistry != "" {
		return repoRegistry, nil
	}
	return registry, nil
}

// ResolveUmbrellaDefaultRegistryForSlugs resolves the default umbrella registry
// using fleet configuration and each repository slug's canonical checkout when
// present under projectsRoot.
func ResolveUmbrellaDefaultRegistryForSlugs(projectsRoot string, slugs []string) (string, error) {
	paths := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if canonical, err := CanonicalRepositoryPath(projectsRoot, slug); err == nil && canonical != "" {
			paths = append(paths, canonical)
		}
	}
	return ResolveUmbrellaDefaultRegistry(paths...)
}

// SetTaskUmbrella attaches a validated canonical umbrella reference to every
// recorded Work Log claim of a task under one per-task operation lock. Target
// may be a linked worktree path (including ".") or a task name.
func SetTaskUmbrella(ctx context.Context, projectsRoot, target string, ref umbrella.Ref) (SetUmbrellaResult, error) {
	return setTaskUmbrellaWithHooks(ctx, projectsRoot, target, ref, nil, nil)
}

func setTaskUmbrellaWithHooks(
	ctx context.Context,
	projectsRoot, target string,
	ref umbrella.Ref,
	afterLock func(),
	writeSidecar func(dir, name string, data []byte) error,
) (SetUmbrellaResult, error) {
	if ref.IsZero() {
		return SetUmbrellaResult{}, fmt.Errorf("umbrella reference is required")
	}
	if err := worktreeclaims.ValidateUmbrellaBindingFields(worktreeclaims.ClaimUmbrellaBinding{Umbrella: ref.String()}); err != nil {
		return SetUmbrellaResult{}, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return SetUmbrellaResult{}, err
	}
	taskName, err := resolveTaskForUmbrellaSet(ctx, home, target)
	if err != nil {
		return SetUmbrellaResult{}, err
	}
	if !validSafeSegment(taskName) {
		return SetUmbrellaResult{}, fmt.Errorf("invalid task name %q", taskName)
	}
	operation, err := prepareOperationRoot(home, taskName, nil)
	if err != nil {
		return SetUmbrellaResult{}, fmt.Errorf("prepare task lock for %s: %w", taskName, err)
	}
	defer operation.close()
	lock, err := acquireLockAt(operation.Directory, taskName)
	if err != nil {
		return SetUmbrellaResult{}, fmt.Errorf("lock task %s for umbrella update: %w", taskName, err)
	}
	defer func() { _ = lock.release() }()
	if afterLock != nil {
		afterLock()
	}

	records, err := collectClaimsInHome(home)
	if err != nil {
		return SetUmbrellaResult{}, err
	}
	var matching []claimRecord
	for _, rec := range records {
		if rec.claim.Task == taskName || rec.claim.EffortID == taskName {
			matching = append(matching, rec)
		}
	}
	if len(matching) == 0 {
		return SetUmbrellaResult{}, fmt.Errorf("no work-log claims found for task %q", taskName)
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].claimsDir != matching[j].claimsDir {
			return matching[i].claimsDir < matching[j].claimsDir
		}
		return matching[i].claim.ClaimID < matching[j].claim.ClaimID
	})

	if writeSidecar == nil {
		writeSidecar = func(dir, name string, data []byte) error {
			return filewrite.WriteBytesAtomic(dir, name, data, 0o600)
		}
	}

	now := time.Now().UTC()
	binding := worktreeclaims.ClaimUmbrellaBinding{Umbrella: ref.String(), RecordedAt: now}
	encoded, _ := json.MarshalIndent(binding, "", "  ")
	encoded = append(encoded, '\n')

	type priorSidecar struct {
		rec         claimRecord
		sidecarName string
		prev        *worktreeclaims.ClaimUmbrellaBinding
	}
	updated := make([]priorSidecar, 0, len(matching))
	repoSet := map[string]bool{}
	for _, rec := range matching {
		sidecarName := rec.claim.ClaimID + worktreeclaims.UmbrellaBindingSuffix
		if writeErr := writeSidecar(rec.claimsDir, sidecarName, encoded); writeErr != nil {
			var partial []string
			for _, u := range updated {
				var rbErr error
				if u.prev != nil {
					prevEncoded, _ := json.MarshalIndent(u.prev, "", "  ")
					rbErr = writeSidecar(u.rec.claimsDir, u.sidecarName, append(prevEncoded, '\n'))
				} else {
					rbErr = os.Remove(filepath.Join(u.rec.claimsDir, u.sidecarName))
				}
				if rbErr != nil {
					partial = append(partial, fmt.Sprintf("%s (%s)", u.rec.claim.ClaimID, u.rec.claim.Repository))
				}
			}
			if len(partial) > 0 {
				return SetUmbrellaResult{}, fmt.Errorf("record umbrella binding for %s (%s): %w; rollback incomplete, partially updated claims: %s",
					taskName, rec.claim.Repository, writeErr, strings.Join(partial, ", "))
			}
			return SetUmbrellaResult{}, fmt.Errorf("record umbrella binding for %s (%s): %w", taskName, rec.claim.Repository, writeErr)
		}
		updated = append(updated, priorSidecar{
			rec:         rec,
			sidecarName: sidecarName,
			prev:        rec.umbrellaSidecar,
		})
		if rec.claim.Repository != "" {
			repoSet[rec.claim.Repository] = true
		}
	}
	repos := make([]string, 0, len(repoSet))
	for repo := range repoSet {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return SetUmbrellaResult{
		Task:         taskName,
		Umbrella:     ref.String(),
		Claims:       len(updated),
		Repositories: repos,
	}, nil
}

func resolveTaskForUmbrellaSet(ctx context.Context, home, target string) (string, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		trimmed = "."
	}
	if info, statErr := os.Stat(trimmed); statErr == nil && info.IsDir() {
		root, rootErr := RepositoryRootFor(ctx, trimmed)
		if rootErr == nil {
			if claim, _, _, claimErr := activeWorkLogClaimReadOnly(home, root); claimErr == nil && claim.Task != "" {
				return claim.Task, nil
			}
			if terminal, termErr := readWorkLogTerminalRecordReadOnly(home, root); termErr == nil && terminal != nil && terminal.Task != "" {
				return terminal.Task, nil
			}
			if manifest, manifestErr := ReadManifest(root); manifestErr == nil && manifest.EffortID != "" {
				return manifest.EffortID, nil
			}
		}
		return "", fmt.Errorf("directory %s is not a WB-managed worktree with a recorded task; pass a worktree path or task name", trimmed)
	}
	return trimmed, nil
}

// UmbrellaForWorktree returns the canonical umbrella reference bound to
// worktree's active claim, terminal claim, or manifest, or "".
func UmbrellaForWorktree(projectsRoot, worktree string) string {
	if home, err := wbhome.Root(projectsRoot); err == nil {
		if claim, _, _, claimErr := activeWorkLogClaimReadOnly(home, worktree); claimErr == nil {
			if ref := effectiveClaimUmbrella(home, claim); ref != "" {
				return ref
			}
		} else if terminal, termErr := readWorkLogTerminalRecordReadOnly(home, worktree); termErr == nil && terminal != nil {
			if ref := effectiveClaimUmbrella(home, terminal.Claim); ref != "" {
				return ref
			}
		}
	}
	if manifest, err := ReadManifest(worktree); err == nil {
		return strings.TrimSpace(manifest.Umbrella)
	}
	return ""
}

// UmbrellaForTask returns the canonical umbrella reference recorded on any
// claim belonging to task, or "".
func UmbrellaForTask(projectsRoot, task string) string {
	task = strings.TrimSpace(task)
	if task == "" {
		return ""
	}
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return ""
	}
	for _, rec := range records {
		if (rec.claim.Task == task || rec.claim.EffortID == task) && rec.umbrella != "" {
			return rec.umbrella
		}
	}
	return ""
}

// UmbrellaForPullRequest resolves the umbrella reference and task name for a
// pull request using explicit PR bindings, cleaned tasks, and (for unbound
// claims with a matching head repository) repository/branch claims.
func UmbrellaForPullRequest(projectsRoot, repository string, pullRequest int, headRepo, headRef string, cleanedTasks []string) (umbrella.Ref, string, bool) {
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return umbrella.Ref{}, "", false
	}
	taskSet := make(map[string]bool, len(cleanedTasks))
	for _, t := range cleanedTasks {
		taskSet[t] = true
	}
	pick := func(rec claimRecord) (umbrella.Ref, string, bool) {
		if rec.umbrella == "" {
			return umbrella.Ref{}, "", false
		}
		ref, parseErr := umbrella.Parse(rec.umbrella, "")
		if parseErr != nil {
			return umbrella.Ref{}, "", false
		}
		return ref, rec.claim.Task, true
	}
	// 1. Exact PR binding match.
	for _, rec := range records {
		if rec.pr != nil && strings.EqualFold(rec.pr.Repository, repository) && rec.pr.PullRequest == pullRequest {
			if ref, task, ok := pick(rec); ok {
				return ref, task, true
			}
		}
	}
	// 2. Cleaned task match (never overriding a conflicting explicit PR binding).
	for _, rec := range records {
		if taskSet[rec.claim.Task] && strings.EqualFold(rec.claim.Repository, repository) &&
			(rec.pr == nil || rec.pr.PullRequest == pullRequest) {
			if ref, task, ok := pick(rec); ok {
				return ref, task, true
			}
		}
	}
	// 3. Repository + branch fallback only for unbound claims with matching head repository.
	if headRepo != "" && strings.EqualFold(headRepo, repository) && headRef != "" {
		for _, rec := range records {
			if rec.pr == nil && strings.EqualFold(rec.claim.Repository, repository) && rec.claim.Branch == headRef {
				if ref, task, ok := pick(rec); ok {
					return ref, task, true
				}
			}
		}
	}
	return umbrella.Ref{}, "", false
}

// RecordPullRequestLandedSHA records the verified merge commit SHA onto the
// pull-request binding sidecar of matching claims so umbrella status and close
// checks retain exact landing proof even when --keep is used. Explicit PR
// bindings are matched first and never overwritten for a different PR number;
// branch fallback applies only to claims with no PR binding when headRepo
// matches repository.
func RecordPullRequestLandedSHA(projectsRoot, repository string, pullRequest int, headRepo, headRef, landedSHA string) {
	landedSHA = strings.TrimSpace(landedSHA)
	if landedSHA == "" || !worktreeproof.IsGitObjectID(landedSHA) || pullRequest <= 0 || strings.TrimSpace(repository) == "" {
		return
	}
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return
	}
	// 1. Match explicit PR bindings first.
	matchedExplicit := false
	for _, rec := range records {
		if rec.pr != nil && strings.EqualFold(rec.pr.Repository, repository) && rec.pr.PullRequest == pullRequest {
			binding := *rec.pr
			binding.LandedSHA = landedSHA
			encoded, _ := json.MarshalIndent(binding, "", "  ")
			_ = filewrite.WriteBytesAtomic(rec.claimsDir, rec.claim.ClaimID+worktreeclaims.PullRequestBindingSuffix, append(encoded, '\n'), 0o600)
			matchedExplicit = true
		}
	}
	if matchedExplicit {
		return
	}
	// 2. Branch fallback only for unbound claims with matching head repository.
	if headRef == "" || headRepo == "" || !strings.EqualFold(headRepo, repository) {
		return
	}
	for _, rec := range records {
		if rec.pr != nil || !strings.EqualFold(rec.claim.Repository, repository) || rec.claim.Branch != headRef {
			continue
		}
		if rec.terminal != nil && rec.terminal.SuccessorClaimID != "" {
			continue
		}
		binding := worktreeclaims.ClaimPullRequestBinding{
			Repository:  rec.claim.Repository,
			PullRequest: pullRequest,
			URL:         fmt.Sprintf("https://github.com/%s/pull/%d", rec.claim.Repository, pullRequest),
			LandedSHA:   landedSHA,
			RecordedAt:  time.Now().UTC(),
		}
		encoded, _ := json.MarshalIndent(binding, "", "  ")
		_ = filewrite.WriteBytesAtomic(rec.claimsDir, rec.claim.ClaimID+worktreeclaims.PullRequestBindingSuffix, append(encoded, '\n'), 0o600)
	}
}

// RecordClaimLanding persists claim-bound landing proof independently of
// worktree cleanup so `wb worktree land --cleanup=false` makes every landed
// source claim eligible for `wb umbrella close`.
func RecordClaimLanding(projectsRoot, repository, task, worktree, branch, target, landedSHA string, pullRequest int, pullRequestURL string) error {
	landedSHA = strings.TrimSpace(landedSHA)
	if !worktreeproof.IsGitObjectID(landedSHA) {
		return fmt.Errorf("invalid landed commit %q", landedSHA)
	}
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, rec := range records {
		if !strings.EqualFold(rec.claim.Repository, repository) {
			continue
		}
		if rec.claim.Task != task && rec.claim.EffortID != task {
			continue
		}
		if worktree != "" && filepath.Clean(rec.claim.Worktree) != filepath.Clean(worktree) {
			continue
		}
		if branch != "" && rec.claim.Branch != branch {
			continue
		}
		if rec.terminal != nil && rec.terminal.SuccessorClaimID != "" {
			continue
		}
		effectiveTarget := strings.TrimSpace(target)
		if effectiveTarget == "" {
			effectiveTarget = rec.claim.Base
		}
		binding := worktreeclaims.ClaimLandingBinding{
			ClaimID:        rec.claim.ClaimID,
			Task:           rec.claim.Task,
			Repository:     rec.claim.Repository,
			Target:         effectiveTarget,
			LandedSHA:      landedSHA,
			PullRequest:    pullRequest,
			PullRequestURL: strings.TrimSpace(pullRequestURL),
			RecordedAt:     now,
		}
		if err := worktreeclaims.ValidateLandingBindingFields(binding); err != nil {
			return err
		}
		encoded, _ := json.MarshalIndent(binding, "", "  ")
		if err := filewrite.WriteBytesAtomic(rec.claimsDir, rec.claim.ClaimID+worktreeclaims.LandingBindingSuffix, append(encoded, '\n'), 0o600); err != nil {
			return fmt.Errorf("record landing binding for %s (%s): %w", rec.claim.Task, rec.claim.Repository, err)
		}
	}
	return nil
}

// UmbrellaMembers returns every recorded member (task, repository, PR, state,
// landed SHA / archive restore pointer, and close eligibility) associated with
// ref across all WB homes under projectsRoot. It refuses if any membership
// evidence cannot be completely validated or if a member task has split
// umbrella references across its claims.
func UmbrellaMembers(projectsRoot string, ref umbrella.Ref) ([]umbrella.Member, error) {
	if ref.IsZero() {
		return nil, fmt.Errorf("umbrella reference is required")
	}
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return nil, err
	}
	target := ref.String()
	matched := make([]claimRecord, 0)
	matchedTasks := map[string]bool{}
	for _, rec := range records {
		if strings.EqualFold(rec.umbrella, target) {
			matched = append(matched, rec)
			matchedTasks[rec.claim.Task] = true
		}
	}
	for _, rec := range records {
		if matchedTasks[rec.claim.Task] && !strings.EqualFold(rec.umbrella, target) {
			return nil, fmt.Errorf("inconsistent umbrella attachments for task %s: claim %s (%s) has %q, expected %q",
				rec.claim.Task, filepath.Join(rec.claimsDir, rec.claim.ClaimID+".json"), rec.claim.Repository, rec.umbrella, target)
		}
	}
	return buildMembersFromClaims(matched), nil
}

// TaskUmbrellaMembers returns the umbrella reference (if any) and every
// recorded member of task (including already-landed or archived ones).
func TaskUmbrellaMembers(projectsRoot, task string) (string, []umbrella.Member, error) {
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return "", nil, err
	}
	matched := make([]claimRecord, 0)
	umbrellaRef := ""
	for _, rec := range records {
		if rec.claim.Task == task || rec.claim.EffortID == task {
			matched = append(matched, rec)
			if umbrellaRef == "" && rec.umbrella != "" {
				umbrellaRef = rec.umbrella
			}
		}
	}
	return umbrellaRef, buildMembersFromClaims(matched), nil
}

// UmbrellaReadyToClose reports whether ref has at least one recorded member
// and every recorded member has landed or been archived with a restore pointer.
func UmbrellaReadyToClose(projectsRoot string, ref umbrella.Ref) (bool, []umbrella.Member, error) {
	members, err := UmbrellaMembers(projectsRoot, ref)
	if err != nil {
		return false, nil, err
	}
	if len(members) == 0 {
		return false, members, nil
	}
	for _, member := range members {
		if !member.ReadyToClose {
			return false, members, nil
		}
	}
	return true, members, nil
}

type claimRecord struct {
	home            string
	runDir          string
	claimsDir       string
	claim           workLogClaim
	umbrella        string
	umbrellaSidecar *worktreeclaims.ClaimUmbrellaBinding
	pr              *worktreeclaims.ClaimPullRequestBinding
	landing         *worktreeclaims.ClaimLandingBinding
	terminal        *workLogTerminalRecord
	cleanup         *workLogCleanupRecord
	retire          *worktreeretire.Transaction
}

// effectiveUmbrella returns the validated effective umbrella reference for
// claim (sidecar wins over claim.Umbrella). Any unreadable or invalid sidecar
// returns an error naming the sidecar path.
func effectiveUmbrella(home string, claim workLogClaim) (string, error) {
	if !validSafeSegment(claim.EffortID) || !validSafeSegment(claim.RunID) || !validClaimID(claim.ClaimID) {
		return strings.TrimSpace(claim.Umbrella), nil
	}
	runDir, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if errors.Is(err, os.ErrNotExist) {
		return strings.TrimSpace(claim.Umbrella), nil
	}
	if err != nil {
		return "", fmt.Errorf("open work-log run %s: %w", filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID), err)
	}
	defer func() { _ = runDir.Close() }()
	claimsDir, err := openPrivateChild(runDir, "claims", false)
	if errors.Is(err, os.ErrNotExist) {
		return strings.TrimSpace(claim.Umbrella), nil
	}
	if err != nil {
		return "", fmt.Errorf("open work-log claims %s: %w", filepath.Join(runPath, "claims"), err)
	}
	defer func() { _ = claimsDir.Close() }()
	return effectiveUmbrellaAt(claimsDir, filepath.Join(runPath, "claims"), claim)
}

func effectiveUmbrellaAt(claimsDir *os.File, claimsPath string, claim workLogClaim) (string, error) {
	binding, found, err := worktreeclaims.ReadValidatedUmbrellaBinding(claimsDir, claim, readJSONAt)
	if err != nil {
		return "", fmt.Errorf("validate umbrella binding %s: %w", filepath.Join(claimsPath, claim.ClaimID+worktreeclaims.UmbrellaBindingSuffix), err)
	}
	if found {
		return strings.TrimSpace(binding.Umbrella), nil
	}
	return strings.TrimSpace(claim.Umbrella), nil
}

func effectiveClaimUmbrella(home string, claim workLogClaim) string {
	ref, err := effectiveUmbrella(home, claim)
	if err != nil {
		return ""
	}
	return ref
}

func collectAllClaims(projectsRoot string) ([]claimRecord, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var all []claimRecord
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if seen[home] {
			continue
		}
		seen[home] = true
		records, readErr := collectClaimsInHome(home)
		if readErr != nil {
			return nil, readErr
		}
		all = append(all, records...)
	}
	return all, nil
}

func readSortedDirNames(dir *os.File, path string, readNames func(*os.File) ([]string, error)) ([]string, error) {
	names, err := readNames(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", path, err)
	}
	sort.Strings(names)
	return names, nil
}

func collectClaimsInHome(home string) ([]claimRecord, error) {
	return collectClaimsInHomeWithReadNames(home, func(dir *os.File) ([]string, error) { return dir.Readdirnames(-1) })
}

func collectClaimsInHomeWithReadNames(home string, readNames func(*os.File) ([]string, error)) ([]claimRecord, error) {
	worklogsPath := filepath.Join(home, "worklogs")
	worklogs, err := openAbsoluteDirectoryNoFollow(worklogsPath, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worklogs directory %s: %w", worklogsPath, err)
	}
	defer func() { _ = worklogs.Close() }()
	efforts, err := readSortedDirNames(worklogs, worklogsPath, readNames)
	if err != nil {
		return nil, err
	}
	var records []claimRecord
	for _, effort := range efforts {
		effortPath := filepath.Join(worklogsPath, effort)
		if !validSafeSegment(effort) {
			return nil, fmt.Errorf("unsafe work-log effort entry %s", effortPath)
		}
		effortDir, openErr := openPrivateChild(worklogs, effort, false)
		if openErr != nil {
			return nil, fmt.Errorf("open work-log effort directory %s: %w", effortPath, openErr)
		}
		runsPath := filepath.Join(effortPath, "runs")
		runsDir, runsErr := openPrivateChild(effortDir, "runs", false)
		_ = effortDir.Close()
		if errors.Is(runsErr, os.ErrNotExist) {
			continue
		}
		if runsErr != nil {
			return nil, fmt.Errorf("open work-log runs directory %s: %w", runsPath, runsErr)
		}
		runNames, readErr := readSortedDirNames(runsDir, runsPath, readNames)
		if readErr != nil {
			_ = runsDir.Close()
			return nil, readErr
		}
		for _, run := range runNames {
			runPath := filepath.Join(runsPath, run)
			if !validSafeSegment(run) {
				_ = runsDir.Close()
				return nil, fmt.Errorf("unsafe work-log run entry %s", runPath)
			}
			runDir, runOpenErr := openPrivateChild(runsDir, run, false)
			if runOpenErr != nil {
				_ = runsDir.Close()
				return nil, fmt.Errorf("open work-log run directory %s: %w", runPath, runOpenErr)
			}
			runRecords, collectErr := collectClaimsInRun(home, effort, run, runDir, runPath, readNames)
			_ = runDir.Close()
			if collectErr != nil {
				_ = runsDir.Close()
				return nil, collectErr
			}
			records = append(records, runRecords...)
		}
		_ = runsDir.Close()
	}
	return records, nil
}

func collectClaimsInRun(home, effort, run string, runDir *os.File, runPath string, readNames func(*os.File) ([]string, error)) ([]claimRecord, error) {
	claimsPath := filepath.Join(runPath, "claims")
	claimsDir, err := openPrivateChild(runDir, "claims", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open work-log claims directory %s: %w", claimsPath, err)
	}
	defer func() { _ = claimsDir.Close() }()

	entries, err := readSortedDirNames(claimsDir, claimsPath, readNames)
	if err != nil {
		return nil, err
	}
	claimFiles := make(map[string]bool, len(entries))
	for _, name := range entries {
		claimFiles[name] = true
	}

	terminalsPath := filepath.Join(runPath, "terminals")
	terminalsDir, termDirErr := openPrivateChild(runDir, "terminals", false)
	if termDirErr != nil && !errors.Is(termDirErr, os.ErrNotExist) {
		return nil, fmt.Errorf("open work-log terminals directory %s: %w", terminalsPath, termDirErr)
	}
	if terminalsDir != nil {
		defer func() { _ = terminalsDir.Close() }()
	}

	cleanupsPath := filepath.Join(runPath, "cleanups")
	cleanupsDir, cleanDirErr := openPrivateChild(runDir, "cleanups", false)
	if cleanDirErr != nil && !errors.Is(cleanDirErr, os.ErrNotExist) {
		return nil, fmt.Errorf("open work-log cleanups directory %s: %w", cleanupsPath, cleanDirErr)
	}
	if cleanupsDir != nil {
		defer func() { _ = cleanupsDir.Close() }()
	}

	var records []claimRecord
	for _, name := range entries {
		if strings.HasPrefix(name, ".") {
			continue // incomplete atomic-write scratch
		}
		entryPath := filepath.Join(claimsPath, name)
		if strings.HasSuffix(name, worktreeclaims.PullRequestBindingSuffix) {
			if err := worktreeclaims.ValidatePullRequestBindingEntry(claimsDir, name, claimFiles, readJSONAt); err != nil {
				return nil, fmt.Errorf("validate pull request binding %s: %w", entryPath, err)
			}
			continue
		}
		if strings.HasSuffix(name, worktreeclaims.UmbrellaBindingSuffix) {
			if err := worktreeclaims.ValidateUmbrellaBindingEntry(claimsDir, name, claimFiles, readJSONAt); err != nil {
				return nil, fmt.Errorf("validate umbrella binding %s: %w", entryPath, err)
			}
			continue
		}
		if strings.HasSuffix(name, worktreeclaims.LandingBindingSuffix) {
			if err := worktreeclaims.ValidateLandingBindingEntry(claimsDir, name, claimFiles, readJSONAt); err != nil {
				return nil, fmt.Errorf("validate landing binding %s: %w", entryPath, err)
			}
			continue
		}
		claimID := strings.TrimSuffix(name, ".json")
		if name != claimID+".json" || !validClaimID(claimID) {
			return nil, fmt.Errorf("unsafe work-log claim entry %s", entryPath)
		}
		var claim workLogClaim
		if err := readJSONAt(claimsDir, name, &claim); err != nil {
			return nil, fmt.Errorf("read work-log claim %s: %w", entryPath, err)
		}
		if claim.ClaimID != claimID || claim.EffortID != effort || claim.RunID != run ||
			strings.TrimSpace(claim.Task) == "" || strings.TrimSpace(claim.Repository) == "" {
			return nil, fmt.Errorf("corroborate work-log claim identity %s: mismatched claim_id, effort_id, run_id, task, or repository", entryPath)
		}

		umbrellaRef := strings.TrimSpace(claim.Umbrella)
		var umbrellaSidecar *worktreeclaims.ClaimUmbrellaBinding
		if binding, found, sidecarErr := worktreeclaims.ReadValidatedUmbrellaBinding(claimsDir, claim, readJSONAt); sidecarErr != nil {
			return nil, fmt.Errorf("validate umbrella binding %s: %w", filepath.Join(claimsPath, claimID+worktreeclaims.UmbrellaBindingSuffix), sidecarErr)
		} else if found {
			umbrellaRef = strings.TrimSpace(binding.Umbrella)
			umbrellaSidecar = &binding
		}
		rec := claimRecord{
			home:            home,
			runDir:          runPath,
			claimsDir:       claimsPath,
			claim:           claim,
			umbrella:        umbrellaRef,
			umbrellaSidecar: umbrellaSidecar,
		}

		if pr, found, err := worktreeclaims.ReadValidatedPullRequestBinding(claimsDir, claim, readJSONAt); err != nil {
			return nil, fmt.Errorf("validate pull request binding %s: %w", filepath.Join(claimsPath, claimID+worktreeclaims.PullRequestBindingSuffix), err)
		} else if found {
			rec.pr = &pr
		}

		if landing, found, err := worktreeclaims.ReadValidatedLandingBinding(claimsDir, claim, readJSONAt); err != nil {
			return nil, fmt.Errorf("validate landing binding %s: %w", filepath.Join(claimsPath, claimID+worktreeclaims.LandingBindingSuffix), err)
		} else if found {
			rec.landing = &landing
		}

		if terminalsDir != nil {
			termPath := filepath.Join(terminalsPath, claimID+".json")
			var term workLogTerminalRecord
			if termErr := readJSONAt(terminalsDir, claimID+".json", &term); termErr == nil {
				if err := corroborateTerminalForClaim(claim, term); err != nil {
					return nil, fmt.Errorf("validate work-log terminal %s: %w", termPath, err)
				}
				rec.terminal = &term
			} else if !errors.Is(termErr, os.ErrNotExist) {
				return nil, fmt.Errorf("read work-log terminal %s: %w", termPath, termErr)
			}
		}

		if cleanupsDir != nil {
			cleanPath := filepath.Join(cleanupsPath, claimID+".json")
			var clean workLogCleanupRecord
			if cleanErr := readJSONAt(cleanupsDir, claimID+".json", &clean); cleanErr == nil {
				if err := corroborateCleanupForClaim(claim, clean); err != nil {
					return nil, fmt.Errorf("validate work-log cleanup %s: %w", cleanPath, err)
				}
				rec.cleanup = &clean
			} else if !errors.Is(cleanErr, os.ErrNotExist) {
				return nil, fmt.Errorf("read work-log cleanup %s: %w", cleanPath, cleanErr)
			}
		}

		retirePath := worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: claim.Task, Repository: claim.Repository})
		if tx, retireErr := worktreeretire.ReadReport(retirePath); retireErr == nil {
			if tx.Task != claim.Task || tx.Repository != claim.Repository {
				return nil, fmt.Errorf("validate retirement receipt %s: mismatched task or repository", retirePath)
			}
			if tx.ClaimID == "" || tx.ClaimID == claimID {
				rec.retire = &tx
			}
		} else if !errors.Is(retireErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read retirement receipt %s: %w", retirePath, retireErr)
		}

		records = append(records, rec)
	}
	return records, nil
}

func corroborateTerminalForClaim(claim workLogClaim, term workLogTerminalRecord) error {
	if term.ClaimID != claim.ClaimID ||
		term.EffortID != claim.EffortID ||
		term.RunID != claim.RunID ||
		term.Task != claim.Task ||
		term.Repository != claim.Repository ||
		term.Branch != claim.Branch ||
		filepath.Clean(term.Worktree) != filepath.Clean(claim.Worktree) ||
		term.Lifecycle != "terminal" ||
		term.SealedAt.IsZero() {
		return fmt.Errorf("terminal identity does not match claim %s", claim.ClaimID)
	}
	if term.Landed != nil && !worktreeclaims.ValidLandedEvidence(term.Landed) {
		return fmt.Errorf("terminal landed evidence for claim %s is invalid", claim.ClaimID)
	}
	return nil
}

func corroborateCleanupForClaim(claim workLogClaim, clean workLogCleanupRecord) error {
	if clean.ClaimID != claim.ClaimID || !worktreeproof.IsGitObjectID(clean.FinalCommit) || clean.CleanedAt.IsZero() {
		return fmt.Errorf("cleanup identity does not match claim %s", claim.ClaimID)
	}
	return nil
}

func buildMembersFromClaims(records []claimRecord) []umbrella.Member {
	if len(records) == 0 {
		return nil
	}
	// Filter out handed-off predecessor claims whose successor claim is also present.
	byID := make(map[string]claimRecord, len(records))
	for _, rec := range records {
		byID[rec.claim.ClaimID] = rec
	}
	superseded := map[string]bool{}
	for _, rec := range records {
		if rec.terminal != nil && rec.terminal.SuccessorClaimID != "" {
			if _, hasSuccessor := byID[rec.terminal.SuccessorClaimID]; hasSuccessor {
				superseded[rec.claim.ClaimID] = true
			}
		}
	}
	// Deduplicate by (task, repository), keeping the latest recorded claim.
	type memberKey struct{ task, repo string }
	latest := make(map[memberKey]claimRecord, len(records))
	for _, rec := range records {
		if superseded[rec.claim.ClaimID] {
			continue
		}
		key := memberKey{task: rec.claim.Task, repo: rec.claim.Repository}
		prev, exists := latest[key]
		if !exists || rec.claim.RecordedAt.After(prev.claim.RecordedAt) {
			latest[key] = rec
		}
	}

	members := make([]umbrella.Member, 0, len(latest))
	for _, rec := range latest {
		members = append(members, classifyClaimMember(rec))
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].Task != members[j].Task {
			return members[i].Task < members[j].Task
		}
		return members[i].Repository < members[j].Repository
	})
	return members
}

func classifyClaimMember(rec claimRecord) umbrella.Member {
	member := umbrella.Member{
		Task:       rec.claim.Task,
		Repository: rec.claim.Repository,
		Branch:     rec.claim.Branch,
		ClaimID:    rec.claim.ClaimID,
		State:      "active",
	}
	if rec.pr != nil {
		member.PullRequest = rec.pr.PullRequest
		member.PullRequestURL = rec.pr.URL
		if rec.pr.LandedSHA != "" {
			member.State = "landed"
			member.LandedSHA = rec.pr.LandedSHA
			member.ReadyToClose = true
		}
	}
	if rec.landing != nil {
		member.State = "landed"
		member.LandedSHA = rec.landing.LandedSHA
		if rec.landing.PullRequest > 0 && member.PullRequest == 0 {
			member.PullRequest = rec.landing.PullRequest
			member.PullRequestURL = rec.landing.PullRequestURL
		}
		member.ReadyToClose = true
	}
	if rec.retire != nil && rec.retire.Phase == "complete" {
		pointer := formatRetireRestorePointer(*rec.retire)
		if pointer != "" {
			member.State = "archived"
			member.ArchiveRef = rec.retire.ArchiveRef
			member.RestorePointer = pointer
			member.ReadyToClose = true
			return member
		}
	}
	if rec.terminal != nil {
		switch rec.terminal.Disposition {
		case "landed":
			if rec.terminal.Landed != nil && rec.terminal.Landed.LandedSHA != "" {
				member.State = "landed"
				member.LandedSHA = rec.terminal.Landed.LandedSHA
				if rec.terminal.Landed.PullRequest > 0 && member.PullRequest == 0 {
					member.PullRequest = rec.terminal.Landed.PullRequest
				}
				member.ReadyToClose = true
				return member
			}
			if rec.cleanup != nil && rec.cleanup.FinalCommit != "" {
				member.State = "landed"
				member.LandedSHA = rec.cleanup.FinalCommit
				member.ReadyToClose = true
				return member
			}
			if !member.ReadyToClose {
				member.State = "finalized"
				member.BlockingReason = "finalized locally without verified landing proof on target"
				return member
			}
		case "retired":
			if !member.ReadyToClose {
				member.State = "retired"
				member.BlockingReason = "retired without a verified archive restore pointer"
				return member
			}
		case "discarded":
			if !member.ReadyToClose {
				member.State = "discarded"
				member.BlockingReason = "aborted (discarded) without a landed SHA or archive restore pointer"
				return member
			}
		case "handoff", "not_landed", "orphaned", "removed":
			if !member.ReadyToClose {
				member.State = rec.terminal.Disposition
				member.BlockingReason = fmt.Sprintf("disposition %q is neither landed nor archived with a restore pointer", rec.terminal.Disposition)
				return member
			}
		default:
			if !member.ReadyToClose {
				member.State = rec.terminal.Disposition
				member.BlockingReason = fmt.Sprintf("state %q is neither landed nor archived with a restore pointer", rec.terminal.Disposition)
				return member
			}
		}
	}
	if !member.ReadyToClose {
		member.BlockingReason = "still active (not yet landed or archived)"
	}
	return member
}

func formatRetireRestorePointer(tx worktreeretire.Transaction) string {
	switch {
	case tx.ArchiveRepository != "" && tx.ArchiveRef != "":
		return tx.ArchiveRepository + ":" + tx.ArchiveRef
	case tx.RetiredRef != "":
		return worktreeretire.SourceRef(tx)
	case tx.ArchiveRef != "":
		return tx.ArchiveRef
	default:
		return ""
	}
}
