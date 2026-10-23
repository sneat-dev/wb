package cmdbranch

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"gopkg.in/yaml.v3"
	"io"
	"sort"
	"strings"
	"time"
)

func printBranchList(out io.Writer, outcome worktrees.BranchListOutcome) error {
	if len(outcome.Entries) == 0 {
		if _, err := fmt.Fprintln(out, "no branches matched (no active branches)"); err != nil {
			return err
		}
		return printRetiredBranchSummary(out, outcome)
	}
	currentRepository := ""
	if _, err := fmt.Fprintln(out, "  REPOSITORY         REF                              KIND     SHORT SHA    STATUS      LAST COMMIT           AUTHOR           TITLE                    SCOPE    EVIDENCE"); err != nil {
		return err
	}
	for _, entry := range outcome.Entries {
		if entry.Repository != currentRepository {
			currentRepository = entry.Repository
			if _, err := fmt.Fprintf(out, "\n%s\n", currentRepository); err != nil {
				return err
			}
		}
		date := "-"
		if !entry.CommitterDate.IsZero() {
			date = entry.CommitterDate.UTC().Format(time.RFC3339)
		}
		kind := entry.RefKind
		if kind == "" {
			kind = "branch"
		}
		evidence := entry.Evidence
		if entry.Scope == worktrees.BranchScopeRemote && kind == "branch" && entry.Branch != "" && entry.Disposition != worktrees.BranchRetired && (entry.PullRequestQueried || entry.PullRequestQueryFailed) {
			evidence += branchPullRequestSummary(entry)
		}
		if _, err := fmt.Fprintf(out, "  %-18s %-32s %-8s %-12s %-11s %-21s %-16s %-24s %-8s %s\n",
			entry.Repository, entry.Branch, kind, entry.ShortSHA, entry.Disposition, date, entry.Author, entry.Title, entry.Scope, evidence); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if err := printDispositionTotals(out, outcome.Totals); err != nil {
		return err
	}
	return printRetiredBranchSummary(out, outcome)
}

func branchPullRequestSummary(entry worktrees.BranchEntry) string {
	if entry.PullRequestQueryFailed {
		return "; PR query failed: " + entry.PullRequestQueryError
	}
	if len(entry.PullRequests) == 0 {
		return "; PRs: none"
	}
	var parts []string
	for _, request := range entry.PullRequests {
		parts = append(parts, fmt.Sprintf("%s #%d %s %s", request.Role, request.Number, request.State, request.URL))
	}
	return "; PRs: " + strings.Join(parts, ", ")
}

func printRetiredBranchSummary(out io.Writer, outcome worktrees.BranchListOutcome) error {
	if outcome.RetiredBranches == 0 && outcome.RetiredTagNames == 0 {
		return nil
	}
	_, err := fmt.Fprintf(out, "retired branches %d (refs local=%d remote=%s), tags %d (refs local=%d remote=%s); excluded from active backlog; use --only retired or --include-retired\n", outcome.RetiredBranches, outcome.RetiredRefs["local"], retiredRemoteRefCount(outcome), outcome.RetiredTagNames, outcome.RetiredTags["local"], retiredRemoteTagCount(outcome))
	return err
}

func printBranchCount(out io.Writer, outcome worktrees.BranchListOutcome) error {
	if _, err := fmt.Fprintln(out, "STATUS       REFS"); err != nil {
		return err
	}
	if err := printDispositionTotals(out, outcome.Totals); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "retired branches %d names (%d local refs, %s remote refs)\nretired tags     %d names (%d local refs, %s remote refs)\n", outcome.RetiredBranches, outcome.RetiredRefs["local"], retiredRemoteRefCount(outcome), outcome.RetiredTagNames, outcome.RetiredTags["local"], retiredRemoteTagCount(outcome))
	return err
}

func printBranchDiagnostics(out io.Writer, diagnostics []string) error {
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintf(out, "diagnostic: %s\n", diagnostic); err != nil {
			return err
		}
	}
	return nil
}

func retiredRemoteRefCount(outcome worktrees.BranchListOutcome) string {
	if outcome.RetiredRemoteUnavailable {
		return "unavailable"
	}
	if count, ok := outcome.RetiredRefs[worktrees.BranchScopeRemote]; ok {
		return fmt.Sprintf("%d", count)
	}
	return "0"
}

func retiredRemoteTagCount(outcome worktrees.BranchListOutcome) string {
	if outcome.RetiredRemoteUnavailable {
		return "unavailable"
	}
	if count, ok := outcome.RetiredTags[worktrees.BranchScopeRemote]; ok {
		return fmt.Sprintf("%d", count)
	}
	return "0"
}

// yamlCompatibleBranchList preserves the machine contract's JSON field names.
// yaml.v3 does not use json tags, so marshal through JSON rather than allowing
// generatedat-style YAML keys to drift from the JSON API.
func yamlCompatibleBranchList(outcome worktrees.BranchListOutcome) ([]byte, error) {
	return yamlCompatibleJSON(outcome)
}

type branchYAMLReport interface {
	worktrees.BranchListOutcome | worktrees.RetiredArchivePlan
}

func yamlCompatibleJSON[T branchYAMLReport](value T) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	// Only the two concrete reports reach here. Successful JSON marshaling
	// yields valid JSON, and its decoded strings/bools/finite numbers/slices/maps
	// have no custom YAML marshalers or unsupported values.
	_ = json.Unmarshal(data, &document)
	raw, _ := yaml.Marshal(document)
	return raw, nil
}

func printDispositionTotals(out io.Writer, totals map[string]int) error {
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(out, "%-11s %d\n", name, totals[name]); err != nil {
			return err
		}
	}
	return nil
}

func printBranchCleanup(out io.Writer, outcome worktrees.BranchCleanupOutcome) error {
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no branches matched")
		return err
	}
	currentRepository := ""
	deleted, eligible := 0, 0
	for _, result := range outcome.Results {
		if result.Repository != currentRepository {
			currentRepository = result.Repository
			if _, err := fmt.Fprintf(out, "\n%s\n", currentRepository); err != nil {
				return err
			}
		}
		switch {
		case result.Applied:
			deleted++
			if _, err := fmt.Fprintf(out, "  deleted      %s %s (%s)\n", result.Scope, result.Branch, result.ShortSHA); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(out, "  would delete %s %s (%s)\n", result.Scope, result.Branch, result.ShortSHA); err != nil {
				return err
			}
		case result.Error != "":
			if _, err := fmt.Fprintf(out, "  failed       %s %s: %s\n", result.Scope, result.Branch, result.Error); err != nil {
				return err
			}
		default:
			// A whole-repository disposition (for example unreadable, when the
			// exact origin target itself could not be fetched) carries no Scope
			// or Branch — it is the only row printed for that repository, so it
			// must name the repository inline rather than relying solely on the
			// group header above, which is easy to lose when rows are grepped or
			// scrolled past.
			if _, err := fmt.Fprintf(out, "  skip         %s %s %s (%s): %s\n", result.Repository, result.Scope, result.Branch, result.Disposition, result.SkipReason); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if outcome.Apply {
		_, err := fmt.Fprintf(out, "%d deleted\n", deleted)
		return err
	}
	_, err := fmt.Fprintf(out, "%d eligible; dry-run only, pass --apply to delete\n", eligible)
	return err
}
