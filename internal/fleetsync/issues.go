package fleetsync

import (
	"fmt"
	"strings"
	"time"
)

// RunMeta describes the sync run that produced an issues report. It carries
// what the results themselves cannot say: when the run happened, whether it
// was a dry run, whether archived pruning was requested, and whether the run
// failed before it scanned anything at all.
type RunMeta struct {
	StartedAt    time.Time
	ProjectsRoot string
	Scanned      int
	DryRun       bool
	// PruneArchived records whether --prune-archived was passed. Without it
	// the ArchivedNotPruned entries cannot be explained.
	PruneArchived bool
	// RunErr is set when the run failed before scanning anything — a GitHub
	// authentication or discovery failure. Results is empty in that case, and
	// the failure is itself the issue worth reporting.
	RunErr error
}

// IssuesMarkdown renders the attention and error groups as Markdown for a
// human or an AI agent to act on. It performs no IO and is deterministic:
// identical input always renders identical bytes.
func IssuesMarkdown(meta RunMeta, results []Result) string {
	var out strings.Builder
	out.WriteString("# WB sync issues\n\n")
	writeIssuesHeader(&out, meta)
	out.WriteString("All repositories are in sync. Nothing requires attention.\n")
	return out.String()
}

// writeIssuesHeader renders the run's provenance and counts. Later tasks widen
// it with the counts; this first version reports the clean run only.
func writeIssuesHeader(out *strings.Builder, meta RunMeta) {
	fmt.Fprintf(out, "**Run:** %s · **Projects root:** %s\n",
		meta.StartedAt.UTC().Format(time.RFC3339), meta.ProjectsRoot)
	fmt.Fprintf(out, "**Scanned:** %d repositories · **Issues:** none\n\n", meta.Scanned)
	if meta.DryRun {
		out.WriteString("**Dry run:** the fleet was not modified. Findings are real, but " +
			"unpushed-commit detection normally runs after a pull and may be incomplete.\n\n")
	}
}
