package cmdrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/runexec"
	"io"
	"sort"
)

func printRecipeEvent(out, errOut io.Writer, event runexec.RecipeEvent) error {
	if event.DryRun {
		_, err := fmt.Fprintln(errOut, "dry-run: reporting only; pass --apply to commit & push")
		return err
	}
	symbols := map[runexec.RecipeBucket]string{runexec.Updated: "✓", runexec.Skipped: "–", runexec.Archived: "▪", runexec.Forked: "⑂", runexec.Failed: "✗"}
	_, err := fmt.Fprintf(out, "%s %s\n", symbols[event.Row.Bucket], event.Row.Text)
	return err
}
func printRecipeResult(out io.Writer, result runexec.RecipeResult, list bool) error {
	if list {
		for _, name := range result.Names {
			if _, err := fmt.Fprintln(out, name); err != nil {
				return err
			}
		}
		return nil
	}
	counts := map[runexec.RecipeBucket]int{}
	var failures []string
	for _, row := range result.Rows {
		counts[row.Bucket]++
		if row.Bucket == runexec.Failed {
			failures = append(failures, row.Text)
		}
	}
	if _, err := fmt.Fprintf(out, "\n━━━ Summary ━━━\nUpdated  %d\nSkipped  %d\nForks    %d\nArchived %d\nErrors   %d\n", counts[runexec.Updated], counts[runexec.Skipped], counts[runexec.Forked], counts[runexec.Archived], counts[runexec.Failed]); err != nil {
		return err
	}
	sort.Strings(failures)
	for _, failure := range failures {
		if _, err := fmt.Fprintf(out, "  ✗ %s\n", failure); err != nil {
			return err
		}
	}
	return nil
}
