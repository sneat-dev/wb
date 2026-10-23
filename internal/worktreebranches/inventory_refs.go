package worktreebranches

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

func (service InventoryService) DecorateBranchCommits(ctx context.Context, repositoryPath string, entries []BranchEntry) {
	if len(entries) == 0 {
		return
	}
	args := []string{"show", "-s", "--format=%H%x1f%an%x1f%s%x1e"}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.SHA != "" && !seen[entry.SHA] {
			args = append(args, entry.SHA)
			seen[entry.SHA] = true
		}
	}
	if len(args) == 3 {
		return
	}
	output, err := service.Ports.Git(ctx, repositoryPath, args...)
	if err != nil {
		return
	}
	metadata := make(map[string][2]string, len(entries))
	for _, record := range strings.Split(output, "\x1e") {
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) == 3 {
			metadata[strings.TrimSpace(fields[0])] = [2]string{strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])}
		}
	}
	for index := range entries {
		if detail, ok := metadata[entries[index].SHA]; ok {
			entries[index].Author, entries[index].Title = detail[0], detail[1]
		}
	}
}

func (service InventoryService) DecorateBranchCommit(ctx context.Context, repositoryPath string, entry *BranchEntry) {
	entries := []BranchEntry{*entry}
	service.DecorateBranchCommits(ctx, repositoryPath, entries)
	*entry = entries[0]
}

func (service InventoryService) BranchEvidenceHost() string {
	host, err := service.Ports.HostName()
	if err != nil || strings.TrimSpace(host) == "" {
		return "unknown"
	}
	return strings.TrimSpace(host)
}

func (service InventoryService) CheckedOutLocalBranches(ctx context.Context, repositoryPath string) (map[string]bool, string) {
	output, err := service.Ports.Git(ctx, repositoryPath, "worktree", "list", "--porcelain")
	if err != nil {
		return map[string]bool{}, fmt.Sprintf("enumerate linked worktrees: %v", err)
	}
	checkedOut := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			checkedOut[branch] = true
		}
	}
	return checkedOut, ""
}

func (service InventoryService) ListLocalRefs(ctx context.Context, repositoryPath string) ([]BranchRef, string) {
	return service.ListRefs(ctx, repositoryPath, "refs/heads/", "")
}

func (service InventoryService) ListRemoteRefs(ctx context.Context, repositoryPath string) ([]BranchRef, string) {
	return service.FetchRemoteRefs(ctx, repositoryPath, "+refs/heads/*:refs/remotes/origin/*", "refs/remotes/origin/", "fetch --prune origin")
}

func (service InventoryService) ListRetiredRemoteRefs(ctx context.Context, repositoryPath string) ([]BranchRef, string) {
	return service.FetchRemoteRefs(ctx, repositoryPath, "+refs/heads/retired/*:refs/remotes/origin/retired/*", "refs/remotes/origin/retired/", "fetch --prune origin retired namespace")
}

func (service InventoryService) FetchRemoteRefs(ctx context.Context, repositoryPath, refspec, refPrefix, failureLabel string) ([]BranchRef, string) {
	if _, err := service.Ports.Git(ctx, repositoryPath, "fetch", "--prune", "origin", refspec); err != nil {
		return nil, fmt.Sprintf("%s: %v", failureLabel, err)
	}
	return service.ListRefs(ctx, repositoryPath, refPrefix, "origin/")
}

func (service InventoryService) ListRetiredTags(ctx context.Context, repositoryPath string, remote, metadata bool) ([]BranchRef, string) {
	if !remote {
		return service.ListRefs(ctx, repositoryPath, "refs/tags/retired/", "")
	}
	output, err := service.Ports.Git(ctx, repositoryPath, "ls-remote", "--tags", "--refs", "origin", "refs/tags/retired/*")
	if err != nil {
		return nil, fmt.Sprintf("ls-remote retired tags: %v", err)
	}
	refs := []BranchRef{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasSuffix(fields[1], "^{}") || !strings.HasPrefix(fields[1], "refs/tags/retired/") || !worktreeproof.IsGitObjectID(fields[0]) {
			continue
		}
		refs = append(refs, BranchRef{Name: strings.TrimPrefix(fields[1], "refs/tags/"), SHA: fields[0], UnknownDate: true})
	}
	if !metadata || len(refs) == 0 {
		return refs, ""
	}
	origin, err := service.Ports.Git(ctx, repositoryPath, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Sprintf("resolve origin for retired tags: %v", err)
	}
	temporary, err := service.Ports.TempDir()
	if err != nil {
		return nil, fmt.Sprintf("create retired tag metadata repository: %v", err)
	}
	defer func() { _ = service.Ports.RemoveDir(temporary) }()
	if _, err := service.Ports.Git(ctx, temporary, "init", "--bare"); err != nil {
		return nil, fmt.Sprintf("initialize retired tag metadata repository: %v", err)
	}
	if _, err := service.Ports.Git(ctx, temporary, "fetch", "--no-tags", strings.TrimSpace(origin), "+refs/tags/retired/*:refs/tags/retired/*"); err != nil {
		return nil, fmt.Sprintf("fetch retired tag metadata: %v", err)
	}
	for i := range refs {
		observed, err := service.Ports.Git(ctx, temporary, "rev-parse", "refs/tags/"+refs[i].Name)
		if err != nil || observed != refs[i].SHA {
			return nil, fmt.Sprintf("retired tag %s changed during metadata fetch", refs[i].Name)
		}
		const separator = "\x1f"
		commit, err := service.Ports.Git(ctx, temporary, "show", "-s", "--format=%cI%x1f%an%x1f%s", "refs/tags/"+refs[i].Name+"^{commit}")
		if err != nil {
			return nil, fmt.Sprintf("read retired tag commit metadata: %v", err)
		}
		parts := strings.SplitN(commit, separator, 3)
		if len(parts) != 3 {
			return nil, "invalid retired tag commit metadata"
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Sprintf("parse retired tag commit date: %v", err)
		}
		refs[i].CommitterDate, refs[i].UnknownDate = parsed, false
		refs[i].Author, refs[i].Title = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	return refs, ""
}

func (service InventoryService) ListRefs(ctx context.Context, repositoryPath, refPrefix, namePrefix string) ([]BranchRef, string) {
	const separator = "\x1f"
	format := strings.Join([]string{"%(refname:short)", "%(objectname)", "%(committerdate:iso-strict)"}, separator)
	output, err := service.Ports.Git(ctx, repositoryPath, "for-each-ref", "--format="+format, refPrefix)
	if err != nil {
		return nil, fmt.Sprintf("enumerate %s: %v", refPrefix, err)
	}
	var refs []BranchRef
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, separator)
		if len(fields) != 3 {
			continue
		}
		shortName := strings.TrimPrefix(fields[0], namePrefix)
		if namePrefix != "" && shortName == fields[0] {
			continue
		}
		if shortName == "HEAD" {
			continue
		}
		committerDate, _ := time.Parse(time.RFC3339, fields[2])
		refs = append(refs, BranchRef{Name: shortName, SHA: fields[1], CommitterDate: committerDate})
	}
	return refs, ""
}

func ReportBranchProgress(out io.Writer, index, total int, repository string) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "[%d/%d] scanning %s\n", index, total, repository)
}

func ReportBranchSummary(out io.Writer, totals map[string]int, elapsed time.Duration) {
	if out == nil {
		return
	}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, totals[name]))
	}
	_, _ = fmt.Fprintf(out, "done in %s: %s\n", elapsed.Round(time.Millisecond), strings.Join(parts, " "))
}
