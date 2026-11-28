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
// recorded Work Log claim of a task. Target may be a linked worktree path
// (including ".") or a task name.
func SetTaskUmbrella(ctx context.Context, projectsRoot, target string, ref umbrella.Ref) (SetUmbrellaResult, error) {
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
	records, err := collectClaimsInHome(home)
	if err != nil {
		return SetUmbrellaResult{}, err
	}
	now := time.Now().UTC()
	binding := worktreeclaims.ClaimUmbrellaBinding{Umbrella: ref.String(), RecordedAt: now}
	encoded, _ := json.MarshalIndent(binding, "", "  ")
	encoded = append(encoded, '\n')

	repoSet := map[string]bool{}
	written := 0
	for _, rec := range records {
		if rec.claim.Task != taskName && rec.claim.EffortID != taskName {
			continue
		}
		sidecarName := rec.claim.ClaimID + worktreeclaims.UmbrellaBindingSuffix
		if err := filewrite.WriteBytesAtomic(rec.claimsDir, sidecarName, encoded, 0o600); err != nil {
			return SetUmbrellaResult{}, fmt.Errorf("record umbrella binding for %s (%s): %w", taskName, rec.claim.Repository, err)
		}
		written++
		if rec.claim.Repository != "" {
			repoSet[rec.claim.Repository] = true
		}
	}
	if written == 0 {
		return SetUmbrellaResult{}, fmt.Errorf("no work-log claims found for task %q", taskName)
	}
	repos := make([]string, 0, len(repoSet))
	for repo := range repoSet {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return SetUmbrellaResult{
		Task:         taskName,
		Umbrella:     ref.String(),
		Claims:       written,
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
// pull request using cleaned tasks, PR bindings, and repository/branch claims.
func UmbrellaForPullRequest(projectsRoot, repository string, pullRequest int, headRef string, cleanedTasks []string) (umbrella.Ref, string, bool) {
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
	// 2. Cleaned task match.
	for _, rec := range records {
		if taskSet[rec.claim.Task] && strings.EqualFold(rec.claim.Repository, repository) {
			if ref, task, ok := pick(rec); ok {
				return ref, task, true
			}
		}
	}
	// 3. Repository + branch match.
	for _, rec := range records {
		if strings.EqualFold(rec.claim.Repository, repository) && rec.claim.Branch == headRef {
			if ref, task, ok := pick(rec); ok {
				return ref, task, true
			}
		}
	}
	return umbrella.Ref{}, "", false
}

// RecordPullRequestLandedSHA records the verified merge commit SHA onto the
// existing pull-request binding sidecar of any matching claim so umbrella
// status and close checks retain exact landing proof even when --keep is used.
func RecordPullRequestLandedSHA(projectsRoot, repository string, pullRequest int, headRef, landedSHA string) {
	landedSHA = strings.TrimSpace(landedSHA)
	if landedSHA == "" || pullRequest <= 0 {
		return
	}
	records, err := collectAllClaims(projectsRoot)
	if err != nil {
		return
	}
	for _, rec := range records {
		if !strings.EqualFold(rec.claim.Repository, repository) {
			continue
		}
		matchesPR := rec.pr != nil && rec.pr.PullRequest == pullRequest
		matchesBranch := headRef != "" && rec.claim.Branch == headRef
		if !matchesPR && !matchesBranch {
			continue
		}
		binding := worktreeclaims.ClaimPullRequestBinding{
			Repository:  rec.claim.Repository,
			PullRequest: pullRequest,
			URL:         fmt.Sprintf("https://github.com/%s/pull/%d", rec.claim.Repository, pullRequest),
			LandedSHA:   landedSHA,
			RecordedAt:  time.Now().UTC(),
		}
		if rec.pr != nil {
			binding = *rec.pr
			binding.LandedSHA = landedSHA
		}
		encoded, _ := json.MarshalIndent(binding, "", "  ")
		_ = filewrite.WriteBytesAtomic(rec.claimsDir, rec.claim.ClaimID+worktreeclaims.PullRequestBindingSuffix, append(encoded, '\n'), 0o600)
	}
}

// UmbrellaMembers returns every recorded member (task, repository, PR, state,
// landed SHA / archive restore pointer, and close eligibility) associated with
// ref across all WB homes under projectsRoot.
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
	for _, rec := range records {
		if strings.EqualFold(rec.umbrella, target) {
			matched = append(matched, rec)
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
	home      string
	runDir    string
	claimsDir string
	claim     workLogClaim
	umbrella  string
	pr        *worktreeclaims.ClaimPullRequestBinding
	terminal  *workLogTerminalRecord
	cleanup   *workLogCleanupRecord
	retire    *worktreeretire.Transaction
}

func effectiveClaimUmbrella(home string, claim workLogClaim) string {
	sidecarPath := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+worktreeclaims.UmbrellaBindingSuffix)
	if raw, err := os.ReadFile(sidecarPath); err == nil {
		var binding worktreeclaims.ClaimUmbrellaBinding
		if json.Unmarshal(raw, &binding) == nil && strings.TrimSpace(binding.Umbrella) != "" {
			return strings.TrimSpace(binding.Umbrella)
		}
	}
	return strings.TrimSpace(claim.Umbrella)
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

func collectClaimsInHome(home string) ([]claimRecord, error) {
	worklogsDir := filepath.Join(home, "worklogs")
	efforts, err := os.ReadDir(worklogsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worklogs directory %s: %w", worklogsDir, err)
	}
	var records []claimRecord
	for _, effort := range efforts {
		if !effort.IsDir() || !validSafeSegment(effort.Name()) {
			continue
		}
		runsDir := filepath.Join(worklogsDir, effort.Name(), "runs")
		runs, runsErr := os.ReadDir(runsDir)
		if runsErr != nil {
			continue
		}
		for _, run := range runs {
			if !run.IsDir() || !validSafeSegment(run.Name()) {
				continue
			}
			runPath := filepath.Join(runsDir, run.Name())
			claimsDir := filepath.Join(runPath, "claims")
			entries, claimsErr := os.ReadDir(claimsDir)
			if claimsErr != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				claimID := strings.TrimSuffix(entry.Name(), ".json")
				if !validClaimID(claimID) {
					continue
				}
				raw, readErr := os.ReadFile(filepath.Join(claimsDir, entry.Name()))
				if readErr != nil {
					continue
				}
				var claim workLogClaim
				if json.Unmarshal(raw, &claim) != nil || claim.ClaimID != claimID {
					continue
				}
				rec := claimRecord{
					home:      home,
					runDir:    runPath,
					claimsDir: claimsDir,
					claim:     claim,
					umbrella:  effectiveClaimUmbrella(home, claim),
				}
				if prRaw, prErr := os.ReadFile(filepath.Join(claimsDir, claimID+worktreeclaims.PullRequestBindingSuffix)); prErr == nil {
					var pr worktreeclaims.ClaimPullRequestBinding
					if json.Unmarshal(prRaw, &pr) == nil && pr.PullRequest > 0 {
						rec.pr = &pr
					}
				}
				if termRaw, termErr := os.ReadFile(filepath.Join(runPath, "terminals", claimID+".json")); termErr == nil {
					var term workLogTerminalRecord
					if json.Unmarshal(termRaw, &term) == nil {
						rec.terminal = &term
					}
				}
				if cleanRaw, cleanErr := os.ReadFile(filepath.Join(runPath, "cleanups", claimID+".json")); cleanErr == nil {
					var clean workLogCleanupRecord
					if json.Unmarshal(cleanRaw, &clean) == nil {
						rec.cleanup = &clean
					}
				}
				retirePath := worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: claim.Task, Repository: claim.Repository})
				if tx, retireErr := worktreeretire.ReadReport(retirePath); retireErr == nil && (tx.ClaimID == "" || tx.ClaimID == claimID) {
					rec.retire = &tx
				}
				records = append(records, rec)
			}
		}
	}
	return records, nil
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
