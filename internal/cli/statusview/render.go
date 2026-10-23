package statusview

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/repostatus"
)

func Markdown(report repostatus.Index, details bool, title string) string {
	var out strings.Builder
	out.WriteString(title)
	if len(report.Repositories) == 0 && report.HiddenClean > 0 {
		if report.HiddenClean == 1 {
			out.WriteString("The inspected repository is clean.\n")
		} else {
			fmt.Fprintf(&out, "All %d inspected repositories are clean.\n", report.HiddenClean)
		}
		return out.String()
	}
	out.WriteString("| Repository | Status | Summary |\n|---|---|---|\n")
	for _, repository := range report.Repositories {
		summary := repository.Summary
		if repository.Error != "" {
			summary = repository.Error
		}
		if summary == "" {
			summary = "—"
		}
		fmt.Fprintf(&out, "| `%s` | `%s` | %s |\n", repository.Repository, repository.Status, summary)
		if details {
			writeStatusDetails(&out, repository)
		}
	}
	if report.HiddenClean > 0 {
		fmt.Fprintf(&out, "\n%s\n", statusHiddenNote(report.HiddenClean))
	}
	return out.String()
}

// statusHiddenNote keeps the default filter honest: a report that left rows
// out says so, and says which flag brings them back.
func statusHiddenNote(count int) string {
	if count == 1 {
		return "_1 clean repository hidden; pass `--all` to include it._"
	}
	return fmt.Sprintf("_%d clean repositories hidden; pass `--all` to include them._", count)
}

func writeStatusDetails(out *strings.Builder, repository repostatus.Row) {
	for _, group := range []struct {
		name  string
		items []string
	}{
		{"Modified", repository.Modified},
		{"Untracked", repository.Untracked},
		{"Conflicted", repository.Conflicted},
	} {
		writeStatusDetailGroup(out, repository.Repository, group.name, group.items)
	}
	if len(repository.UnpushedBranches) == 0 {
		writeStatusDetailGroup(out, repository.Repository, "Unpushed", repository.Unpushed)
	} else {
		fmt.Fprintf(out, "\n%s — Unpushed:\n", repository.Repository)
		for _, branch := range repository.UnpushedBranches {
			if branch.Worktree == "" {
				fmt.Fprintf(out, "- Branch `%s`:\n", branch.Branch)
			} else {
				fmt.Fprintf(out, "- Branch `%s` in worktree `%s`:\n", branch.Branch, branch.Worktree)
			}
			for _, commit := range branch.Commits {
				fmt.Fprintf(out, "  - `%s`\n", commit)
			}
		}
	}
	writeStatusDetailGroup(out, repository.Repository, "Stashed", repository.Stashed)
}

func writeStatusDetailGroup(out *strings.Builder, repository, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s — %s:\n", repository, name)
	for _, item := range items {
		fmt.Fprintf(out, "- `%s`\n", item)
	}
}
