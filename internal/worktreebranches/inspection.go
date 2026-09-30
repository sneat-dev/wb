package worktreebranches

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RepositoryInspection func(context.Context, Repository, InventorySweep, map[string]string) ([]BranchEntry, string)

type repositoryInspectionResult struct {
	entries    []BranchEntry
	diagnostic string
}

func (service InventoryService) InspectRepositoryBranchesWithHeartbeat(ctx context.Context, repository Repository, sweep InventorySweep, inUse map[string]string, index, total int, interval time.Duration, inspect RepositoryInspection) ([]BranchEntry, string) {
	if sweep.Progress == nil || interval <= 0 {
		return inspect(ctx, repository, sweep, inUse)
	}
	result := make(chan repositoryInspectionResult, 1)
	started := time.Now()
	go func() {
		entries, diagnostic := inspect(ctx, repository, sweep, inUse)
		result <- repositoryInspectionResult{entries: entries, diagnostic: diagnostic}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case inspected := <-result:
			return inspected.entries, inspected.diagnostic
		case <-ticker.C:
			_, _ = fmt.Fprintf(sweep.Progress, "[%d/%d] still scanning %s (%s)\n", index, total, repository.Slug, time.Since(started).Round(time.Second))
		}
	}
}

func (service InventoryService) ClassifyFleetBranches(ctx context.Context, sweep InventorySweep) ([]BranchEntry, []string, error) {
	entries, diagnostics, _, err := service.ClassifyFleetBranchesWithPaths(ctx, sweep)
	return entries, diagnostics, err
}

func (service InventoryService) ClassifyFleetBranchesWithPaths(ctx context.Context, sweep InventorySweep) ([]BranchEntry, []string, map[string]string, error) {
	repositories, err := service.DiscoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("discover repositories below %s: %w", sweep.ProjectsRoot, err)
	}
	repositories, err = SelectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return nil, nil, nil, err
	}
	paths := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		paths[repository.Slug] = repository.Path
	}
	inUse, diagnostic := service.BranchInUseIndex(ctx, sweep.ProjectsRoot, sweep.Filter)
	var diagnostics []string
	if diagnostic != "" {
		diagnostics = append(diagnostics, diagnostic)
	}
	start := time.Now()
	var entries []BranchEntry
	total := len(repositories)
	for index, repository := range repositories {
		ReportBranchProgress(sweep.Progress, index+1, total, repository.Slug)
		repositoryEntries, diagnostic := service.InspectRepositoryBranchesWithHeartbeat(ctx, repository, sweep, inUse, index+1, total, 9*time.Second, service.InspectRepositoryBranches)
		entries = append(entries, repositoryEntries...)
		if diagnostic != "" {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	ReportBranchSummary(sweep.Progress, TallyDispositions(entries), time.Since(start))
	return entries, diagnostics, paths, nil
}

func (service InventoryService) InspectRepositoryBranches(ctx context.Context, repository Repository, sweep InventorySweep, inUse map[string]string) ([]BranchEntry, string) {
	targetSHA, err := service.Ports.FetchTarget(ctx, repository.Path, sweep.Base)
	if err != nil {
		return []BranchEntry{{Repository: repository.Slug, Base: sweep.Base, Disposition: BranchUnreadable,
				Evidence: fmt.Sprintf("fetch exact origin/%s target: %v", sweep.Base, err)}},
			fmt.Sprintf("%s: fetch exact origin/%s target: %v", repository.Slug, sweep.Base, err)
	}
	canonicalHEAD, _ := service.Ports.Git(ctx, repository.Path, "rev-parse", "--abbrev-ref", "HEAD")
	canonicalHEAD = strings.TrimSpace(canonicalHEAD)
	var entries []BranchEntry
	var diagnostics []string
	if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
		local, diagnostic := service.ListLocalRefs(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", repository.Slug, diagnostic))
		}
		checkedOut, diagnostic := service.CheckedOutLocalBranches(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", repository.Slug, diagnostic))
		}
		pullRequestCache := map[string][]GitHubPullRequest{}
		for _, ref := range local {
			if !BranchNameSelected(sweep.Policy(), ref.Name) {
				continue
			}
			if IsRetiredBranch(ref.Name) && !sweep.IncludeRetired && sweep.Only != BranchRetired && sweep.Branch != ref.Name && sweep.Name == "" {
				continue
			}
			if IsRetiredBranch(ref.Name) {
				entries = append(entries, RetiredBranchEntry(repository.Slug, sweep.Policy(), ref, BranchScopeLocal, targetSHA))
			} else {
				entries = append(entries, service.ClassifyBranch(ctx, repository, sweep, ref, BranchScopeLocal, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache))
			}
			service.DecorateBranchCommit(ctx, repository.Path, &entries[len(entries)-1])
		}
	}
	if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
		remote, diagnostic := service.ListRemoteRefs(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", repository.Slug, diagnostic))
		}
		checkedOut, checkedOutDiagnostic := service.CheckedOutLocalBranches(ctx, repository.Path)
		if checkedOutDiagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", repository.Slug, checkedOutDiagnostic))
		}
		pullRequestCache := map[string][]GitHubPullRequest{}
		branchPullRequestCache := map[string]PullRequestEvidence{}
		for _, ref := range remote {
			if !BranchNameSelected(sweep.Policy(), ref.Name) {
				continue
			}
			if IsRetiredBranch(ref.Name) && !sweep.IncludeRetired && sweep.Only != BranchRetired && sweep.Branch != ref.Name && sweep.Name == "" {
				continue
			}
			if IsRetiredBranch(ref.Name) {
				entries = append(entries, RetiredBranchEntry(repository.Slug, sweep.Policy(), ref, BranchScopeRemote, targetSHA))
			} else {
				entries = append(entries, service.ClassifyBranch(ctx, repository, sweep, ref, BranchScopeRemote, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache))
				entry := &entries[len(entries)-1]
				if (sweep.WithPRs && entry.Disposition != BranchProtected && entry.Disposition != BranchUnreadable) ||
					(sweep.Cleanup && EligibleBranchCleanupDisposition(*entry)) {
					service.DecorateRemoteBranchPullRequests(ctx, repository, ref, entry, branchPullRequestCache, sweep.WithPRs)
				}
			}
			service.DecorateBranchCommit(ctx, repository.Path, &entries[len(entries)-1])
		}
	}
	return entries, strings.Join(diagnostics, "; ")
}
