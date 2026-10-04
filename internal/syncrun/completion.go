package syncrun

import (
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/fleetsync"
)

type Effects struct {
	Markers func([]fleetsync.Result, string, io.Writer)
	Publish func(string, string, int, io.Writer, io.Writer) error
}

func Finalize(meta fleetsync.RunMeta, results []fleetsync.Result, options Options, effects Effects, out, errOut io.Writer) int {
	projectsRoot, publish, dryRun := options.ProjectsRoot, options.Publish, options.DryRun
	// Written before the error short-circuit below, because a run WITH errors
	// is exactly the run whose report matters most. Unlike the checkout
	// markers, this also runs for a dry run: dry-run detection is read-only
	// and identical, so its findings are real — IssuesMarkdown stamps the
	// report so the reader knows the fleet was not actually pulled.
	writeSyncIssuesReport(meta, results, projectsRoot, out, errOut)

	hasErrors := false
	for _, res := range results {
		if res.Status == fleetsync.Failed {
			hasErrors = true
		}
	}

	if hasErrors {
		return 1
	}

	// A clone sync just created, refreshed, or moved is a checkout an agent may
	// arrive at next, so it gets its .worktree.md here. A dry run writes
	// nothing, and a marker WB could not write never fails a sync that
	// otherwise succeeded — the whole file is an orientation aid.
	if !dryRun {
		effects.Markers(results, projectsRoot, errOut)
	}

	if publish {
		if dryRun {
			_, _ = fmt.Fprintln(out, "dry-run: skipping remote publish")
		} else if err := effects.Publish(projectsRoot, options.Filter, options.Workers, out, errOut); err != nil {
			_, _ = fmt.Fprintln(errOut, "remote publish failed (sync itself succeeded):", err)
		}
	}

	return 0
}
