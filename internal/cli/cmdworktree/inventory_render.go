package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"strings"
	"time"
)

func writeInventoryList(out io.Writer, results []worktrees.ListResult) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(out, "no WB worktrees")
		return err
	}
	for _, result := range results {
		state := "active"
		switch {
		case result.TerminalResult == "success":
			state = "finalized-success"
		case result.TerminalResult == "failure":
			state = "finalized-failure"
		case !result.Clean:
			state = "dirty"
		case result.Locked:
			state = "locked"
		case result.OpenPullRequest != nil:
			state = "open-pr"
		case result.AbsorbedAtOrigin:
			state = "absorbed"
		case result.MergedPullRequest != nil:
			state = "merged"
		case result.LocallyMerged:
			state = "locally-merged"
		}
		pr := "-"
		if result.OpenPullRequest != nil {
			pr = result.OpenPullRequest.URL
		} else if result.MergedPullRequest != nil {
			pr = result.MergedPullRequest.URL
		}
		branch := result.Branch
		if result.Detached {
			branch = "DETACHED"
			if state == "active" {
				state = "detached"
			}
		}
		age := inventoryAgeLabel(result)
		placement := result.Placement
		if placement == "" {
			placement = "unknown"
		}
		line := fmt.Sprintf(
			"%s  %s  %s  %s  placement=%s  owner=%s  age=%s  %s",
			result.Task, result.Repository, branch, state, placement, result.Owner, age, pr,
		)
		if result.TerminalResult != "" {
			report := result.ReportPath
			if report == "" {
				report = "-"
			}
			line += fmt.Sprintf("  report=%s", report)
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func inventoryAgeLabel(result worktrees.ListResult) string {
	if result.AgeSeconds <= 0 {
		return "-"
	}
	age := (time.Duration(result.AgeSeconds) * time.Second).Truncate(time.Minute).String()
	if result.Expired {
		return age + "!"
	}
	return age
}

func writeInventorySummary(out io.Writer, task string, results []worktrees.ListResult, withGitHub bool) error {
	if _, err := fmt.Fprintf(out, "# WB worktree summary: %s\n\n", task); err != nil {
		return err
	}
	if len(results) == 0 {
		_, err := fmt.Fprintln(out, "no live worktrees for this task")
		return err
	}
	if _, err := fmt.Fprintf(out, "%d worktree(s)\n\n", len(results)); err != nil {
		return err
	}
	for index, result := range results {
		state := inventorySummaryState(result)
		head := result.HeadSHA
		if len(head) > 12 {
			head = head[:12]
		}
		if _, err := fmt.Fprintf(out, "## %s\n", result.Repository); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "worktree: %s\n", result.WorktreeDir); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "branch:   %s\n", result.Branch); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "head:     %s\n", head); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "state:    %s\n", state); err != nil {
			return err
		}
		integration := "not integrated at origin/" + result.Base
		switch {
		case result.IntegratedAtOrigin:
			integration = "integrated at origin/" + result.Base
		case result.AbsorbedAtOrigin:
			integration = "absorbed at origin/" + result.Base
		case result.RebaseMergedAtOrigin:
			integration = "rebase-merged at origin/" + result.Base
		case result.LocallyMerged:
			integration = "locally merged; awaiting push"
		}
		if _, err := fmt.Fprintf(out, "target:   %s\n", integration); err != nil {
			return err
		}
		if result.TerminalResult != "" {
			if _, err := fmt.Fprintf(out, "finalize: %s at %s\n", result.TerminalResult, result.FinalizedAt.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			if result.TerminalMessage != "" {
				if _, err := fmt.Fprintf(out, "message:  %s\n", result.TerminalMessage); err != nil {
					return err
				}
			}
			if result.ReportPath != "" {
				if _, err := fmt.Fprintf(out, "report:   %s\n", result.ReportPath); err != nil {
					return err
				}
			}
		}
		switch {
		case result.OpenPullRequest != nil:
			if _, err := fmt.Fprintf(out, "pr:       open #%d %s\n", result.OpenPullRequest.Number, result.OpenPullRequest.URL); err != nil {
				return err
			}
		case result.MergedPullRequest != nil:
			if _, err := fmt.Fprintf(out, "pr:       merged #%d %s\n", result.MergedPullRequest.Number, result.MergedPullRequest.URL); err != nil {
				return err
			}
		case withGitHub:
			if _, err := fmt.Fprintln(out, "pr:       none"); err != nil {
				return err
			}
		}
		if index+1 < len(results) {
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}

func inventorySummaryState(result worktrees.ListResult) string {
	parts := make([]string, 0, 3)
	if result.TerminalResult != "" {
		parts = append(parts, "finalized-"+result.TerminalResult)
	}
	if !result.Clean {
		parts = append(parts, "dirty")
	} else {
		parts = append(parts, "clean")
	}
	if result.Locked {
		parts = append(parts, "locked")
	}
	switch {
	case result.OpenPullRequest != nil:
		parts = append(parts, "open-pr")
	case result.AbsorbedAtOrigin:
		parts = append(parts, "absorbed")
	case result.MergedPullRequest != nil:
		parts = append(parts, "merged")
	case result.LocallyMerged:
		parts = append(parts, "locally-merged")
	default:
		parts = append(parts, "active")
	}
	return strings.Join(parts, ",")
}
