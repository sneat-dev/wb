package cmdsync

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/syncrun"
)

type syncSummaryStyles struct {
	enabled    bool
	title      lipgloss.Style
	section    lipgloss.Style
	attention  lipgloss.Style
	failure    lipgloss.Style
	repository lipgloss.Style
	worktree   lipgloss.Style
}

func newSyncSummaryStyles(enabled bool) syncSummaryStyles {
	return syncSummaryStyles{
		enabled:    enabled,
		title:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4B5FD")),
		section:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7DD3FC")),
		attention:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FBBF24")),
		failure:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F87171")),
		repository: lipgloss.NewStyle().Bold(true),
		worktree:   lipgloss.NewStyle().Foreground(lipgloss.Color("#86EFAC")),
	}
}
func (s syncSummaryStyles) render(style lipgloss.Style, value string) string {
	if !s.enabled {
		return value
	}
	return style.Render(value)
}
func (s syncSummaryStyles) sectionHeading(section fleetsync.SummarySection) string {
	switch section {
	case fleetsync.SummaryAttention:
		return s.render(s.attention, string(section))
	case fleetsync.SummaryErrors:
		return s.render(s.failure, string(section))
	default:
		return s.render(s.section, string(section))
	}
}
func printSyncSummary(out io.Writer, results []fleetsync.Result, pruneArchived, styled bool) {
	groups := fleetsync.Summary(results)
	styles := newSyncSummaryStyles(styled)
	var summary strings.Builder
	_, _ = fmt.Fprintln(&summary)
	_, _ = fmt.Fprintln(&summary, styles.render(styles.title, "━━━ Summary ━━━"))
	printCount := func(group fleetsync.SummaryGroup) {
		line := fmt.Sprintf("%-24s%d", group.Label, len(group.Results))
		switch group.Section {
		case fleetsync.SummaryAttention:
			line = styles.render(styles.attention, line)
		case fleetsync.SummaryErrors:
			line = styles.render(styles.failure, line)
		}
		_, _ = fmt.Fprintln(&summary, line)
	}
	var section fleetsync.SummarySection
	for _, group := range groups {
		// An empty failure section followed by attention details reads as though
		// those warnings are errors. Omit it entirely when there are no errors.
		if group.Section == fleetsync.SummaryErrors && len(group.Results) == 0 {
			continue
		}
		if group.Section != section {
			section = group.Section
			_, _ = fmt.Fprintf(&summary, "\n%s\n", styles.sectionHeading(section))
		}
		printCount(group)
		switch group.Section {
		case fleetsync.SummaryAttention:
			for _, result := range group.Results {
				writeSyncAttention(&summary, styles, result)
			}
		case fleetsync.SummaryErrors:
			for _, result := range group.Results {
				marker := styles.render(styles.failure, "✗")
				repository := styles.render(styles.repository, result.Repo.Slug())
				_, _ = fmt.Fprintf(&summary, "    %s %s — %s\n", marker, repository, result.Err)
			}
		}
	}
	if pruneArchived {
		printArchivedPruning(&summary, results)
	}
	if styled {
		_, _ = lipgloss.Fprint(out, summary.String())
	} else {
		_, _ = io.WriteString(out, summary.String())
	}
}
func writeSyncAttention(out io.Writer, styles syncSummaryStyles, result fleetsync.Result) {
	marker := styles.render(styles.attention, "!")
	repository := styles.render(styles.repository, result.Repo.Slug())
	switch {
	case result.Status == fleetsync.Diverged, result.Status == fleetsync.NoUpstream:
		_, _ = fmt.Fprintf(out, "    %s %s — %s; not pulled\n", marker, repository, result.Tracking.Summary())
	case result.Status == fleetsync.Unpushed && len(result.Detail.UnpushedBranches) > 0:
		for _, branch := range result.Detail.UnpushedBranches {
			location := branch.Branch
			if branch.Worktree != "" && filepath.Clean(branch.Worktree) != filepath.Clean(result.Repo.Path) {
				location = "🌳 " + location
			}
			location = styles.render(styles.worktree, location)
			_, _ = fmt.Fprintf(out, "    %s %s %s — %s not yet pushed\n",
				marker, repository, location, commitCount(len(branch.Commits)))
		}
	case result.Status == fleetsync.Unpushed:
		_, _ = fmt.Fprintf(out, "    %s %s — %s not yet pushed\n",
			marker, repository, commitCount(len(result.Detail.Unpushed)))
	case result.Status == fleetsync.ArchivedUnlandable:
		_, _ = fmt.Fprintf(out, "    %s %s — archived, so its %s can never be pushed; discard them or unarchive\n",
			marker, repository, result.Detail.Summary())
	case result.Status == fleetsync.RepositoryTransferRequired:
		_, _ = fmt.Fprintf(out, "    %s %s → %s — %s\n", marker, result.Repo.TransferFrom, repository, result.Reason)
	case result.ArchivedNotPruned:
		_, _ = fmt.Fprintf(out, "    %s %s — archived; not pruned (pass --prune-archived to enable cleanup)\n", marker, repository)
	}
}
func commitCount(count int) string {
	if count == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", count)
}
func printArchivedPruning(out io.Writer, results []fleetsync.Result) {
	var archived []fleetsync.Result
	for _, r := range results {
		if r.Archived {
			archived = append(archived, r)
		}
	}
	if len(archived) == 0 {
		return
	}
	_, _ = fmt.Fprintln(out, "\nArchived (--prune-archived)")
	for _, r := range archived {
		switch r.Status {
		case fleetsync.RemovedArchived:
			// The receipt path is printed, not just written: a deletion the
			// operator can see but not later account for is the gap this
			// receipt exists to close.
			if r.ReceiptPath != "" {
				_, _ = fmt.Fprintf(out, "  deleted      %s — %s (receipt: %s)\n", r.Repo.Slug(), r.Reason, r.ReceiptPath)
			} else {
				_, _ = fmt.Fprintf(out, "  deleted      %s — %s\n", r.Repo.Slug(), r.Reason)
			}
		case fleetsync.KeptArchived, fleetsync.ArchivedUnlandable:
			_, _ = fmt.Fprintf(out, "  skipped      %s — %s\n", r.Repo.Slug(), r.Reason)
		case fleetsync.AbsentArchived:
			_, _ = fmt.Fprintf(out, "  absent       %s — not cloned locally; nothing to prune\n", r.Repo.Slug())
		case fleetsync.Failed:
			_, _ = fmt.Fprintf(out, "  failed       %s — %s\n", r.Repo.Slug(), r.Err)
		}
	}
}
func Presentation() syncrun.Presentation {
	return syncrun.Presentation{Interactive: func(ctx context.Context, repos []discover.Repo, orgTotal map[string]int, options syncrun.Options, errOut io.Writer) []fleetsync.Result {
		return runSyncTUI(ctx, repos, orgTotal, options.ProjectsRoot, options.Workers, options.DryRun, options.PruneArchived, errOut)
	}, Summary: printSyncSummary}
}
