package worktreebranches

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RetiredInventory struct {
	Host                                 string
	GeneratedAt                          time.Time
	Repository, Org, Branch, Base, Scope string
	Entries                              []BranchEntry
	Diagnostics                          []string
	Totals                               map[string]int
	RetiredRefs                          map[string]int
	RetiredBranches                      int
	RetiredTags                          map[string]int
	RetiredTagNames                      int
	RetiredRemoteUnavailable             bool
	ElapsedMS                            int64
}

func (service InventoryService) DiscoverBranchRepositories(projectsRoot, filter string) ([]Repository, error) {
	repositories, err := service.Ports.Discover(projectsRoot)
	if err != nil {
		return nil, err
	}
	if filter == "" {
		return repositories, nil
	}
	filtered := make([]Repository, 0, len(repositories))
	for _, repository := range repositories {
		if strings.Contains(repository.Slug, filter) {
			filtered = append(filtered, repository)
		}
	}
	return filtered, nil
}

func SelectBranchRepositories(repositories []Repository, repositorySlug, org string) ([]Repository, error) {
	selected := make([]Repository, 0, len(repositories))
	for _, repository := range repositories {
		owner, _, _ := strings.Cut(repository.Slug, "/")
		if (repositorySlug == "" || repository.Slug == repositorySlug) && (org == "" || owner == org) {
			selected = append(selected, repository)
		}
	}
	if repositorySlug != "" && len(selected) == 0 {
		return nil, fmt.Errorf("selected repository %q was not discovered", repositorySlug)
	}
	return selected, nil
}

func (service InventoryService) BranchInUseIndex(ctx context.Context, projectsRoot, filter string) (map[string]string, string) {
	uses, err := service.Ports.ListInUse(ctx, projectsRoot, filter)
	if err != nil {
		return map[string]string{}, fmt.Sprintf("read WB worktree inventory: %v", err)
	}
	index := make(map[string]string, len(uses))
	for _, use := range uses {
		index[BranchInUseKey(use.Repository, use.Branch)] = use.Task
	}
	return index, ""
}

func (service InventoryService) AppendRetiredEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository Repository, sweep InventorySweep, refs []BranchRef, scope string) int {
	return service.AppendRetiredRefEntries(ctx, entries, names, repository, sweep, refs, scope, "branch")
}

func (service InventoryService) AppendRetiredTagEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository Repository, sweep InventorySweep, refs []BranchRef, scope string) int {
	return service.AppendRetiredRefEntries(ctx, entries, names, repository, sweep, refs, scope, "tag")
}

func (service InventoryService) AppendRetiredRefEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository Repository, sweep InventorySweep, refs []BranchRef, scope, refKind string) int {
	start, count := len(*entries), 0
	for _, ref := range refs {
		if !RetiredRefSelected(sweep.Policy(), ref) {
			continue
		}
		entry := RetiredBranchEntry(repository.Slug, sweep.Policy(), ref, scope, "")
		entry.RefKind = refKind
		*entries = append(*entries, entry)
		names[repository.Slug+"|"+ref.Name] = true
		count++
	}
	service.DecorateBranchCommits(ctx, repository.Path, (*entries)[start:])
	return count
}

func (service InventoryService) InventoryRetiredNamespace(ctx context.Context, sweep InventorySweep, generatedAt time.Time) (RetiredInventory, error) {
	repositories, err := service.DiscoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return RetiredInventory{}, fmt.Errorf("discover repositories below %s: %w", sweep.ProjectsRoot, err)
	}
	entries := make([]BranchEntry, 0)
	retiredRefs, retiredTags := map[string]int{}, map[string]int{}
	retiredNames, retiredTagNames := map[string]bool{}, map[string]bool{}
	retiredRemoteUnavailable := false
	diagnostics := []string{fmt.Sprintf("retired namespace inventory skipped fetch of origin/%s", sweep.Base)}
	selected, err := SelectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return RetiredInventory{}, err
	}
	build := func() RetiredInventory {
		return RetiredInventory{Host: service.BranchEvidenceHost(), GeneratedAt: generatedAt, Repository: sweep.Repository, Org: sweep.Org, Branch: sweep.Branch,
			Base: sweep.Base, Scope: sweep.Scope, Entries: entries, Diagnostics: diagnostics, Totals: TallyDispositions(entries),
			RetiredRefs: retiredRefs, RetiredBranches: len(retiredNames), RetiredTags: retiredTags, RetiredTagNames: len(retiredTagNames),
			RetiredRemoteUnavailable: retiredRemoteUnavailable, ElapsedMS: time.Since(generatedAt).Milliseconds()}
	}
	if sweep.Only != "" && sweep.Only != BranchRetired {
		diagnostics = append(diagnostics, fmt.Sprintf("retired namespace cannot match --only %s", sweep.Only))
		return build(), nil
	}
	for index, repository := range selected {
		ReportBranchProgress(sweep.Progress, index+1, len(selected), repository.Slug)
		if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
			refs, diagnostic := service.ListRefs(ctx, repository.Path, "refs/heads/retired/", "")
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired local refs: %s", repository.Slug, diagnostic))
			} else {
				retiredRefs[BranchScopeLocal] += service.AppendRetiredEntries(ctx, &entries, retiredNames, repository, sweep, refs, BranchScopeLocal)
			}
			tags, diagnostic := service.ListRetiredTags(ctx, repository.Path, false, true)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired local tags: %s", repository.Slug, diagnostic))
			} else {
				retiredTags[BranchScopeLocal] += service.AppendRetiredTagEntries(ctx, &entries, retiredTagNames, repository, sweep, tags, BranchScopeLocal)
			}
		}
		if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
			refs, diagnostic := service.ListRetiredRemoteRefs(ctx, repository.Path)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired remote refs: %s", repository.Slug, diagnostic))
				retiredRemoteUnavailable = true
			} else {
				retiredRefs[BranchScopeRemote] += service.AppendRetiredEntries(ctx, &entries, retiredNames, repository, sweep, refs, BranchScopeRemote)
			}
			tags, tagDiagnostic := service.ListRetiredTags(ctx, repository.Path, true, true)
			if tagDiagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired remote tags: %s", repository.Slug, tagDiagnostic))
				retiredRemoteUnavailable = true
			} else {
				retiredTags[BranchScopeRemote] += service.AppendRetiredTagEntries(ctx, &entries, retiredTagNames, repository, sweep, tags, BranchScopeRemote)
			}
		}
	}
	SortBranchEntries(entries)
	ReportBranchSummary(sweep.Progress, TallyDispositions(entries), time.Since(generatedAt))
	return build(), nil
}

func (service InventoryService) CountRetiredBranches(ctx context.Context, sweep InventorySweep) (map[string]int, int, map[string]int, int, bool, []string) {
	repositories, err := service.DiscoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return nil, 0, nil, 0, false, []string{fmt.Sprintf("count retired branches: discover repositories: %v", err)}
	}
	repositories, err = SelectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return nil, 0, nil, 0, false, []string{fmt.Sprintf("count retired branches: %v", err)}
	}
	names, counts := map[string]bool{}, map[string]int{}
	tagNames, tagCounts := map[string]bool{}, map[string]int{}
	retiredRemoteUnavailable := false
	var diagnostics []string
	for _, repository := range repositories {
		if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
			refs, diagnostic := service.ListLocalRefs(ctx, repository.Path)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired local refs: %s", repository.Slug, diagnostic))
			}
			AccumulateRetiredCounts(sweep.Policy(), repository.Slug, refs, BranchScopeLocal, counts, names)
			tags, diagnostic := service.ListRetiredTags(ctx, repository.Path, false, true)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired local tags: %s", repository.Slug, diagnostic))
			}
			AccumulateRetiredCounts(sweep.Policy(), repository.Slug, tags, BranchScopeLocal, tagCounts, tagNames)
		}
		if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
			refs, diagnostic := service.ListRefs(ctx, repository.Path, "refs/remotes/origin/", "origin/")
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired remote refs: %s", repository.Slug, diagnostic))
				retiredRemoteUnavailable = true
			}
			AccumulateRetiredCounts(sweep.Policy(), repository.Slug, refs, BranchScopeRemote, counts, names)
			tags, diagnostic := service.ListRetiredTags(ctx, repository.Path, true, sweep.OlderThan > 0)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired remote tags: %s", repository.Slug, diagnostic))
				retiredRemoteUnavailable = true
			}
			AccumulateRetiredCounts(sweep.Policy(), repository.Slug, tags, BranchScopeRemote, tagCounts, tagNames)
		}
	}
	return counts, len(names), tagCounts, len(tagNames), retiredRemoteUnavailable, diagnostics
}
