package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// credentialedURL matches the userinfo component of a URL — the
// "user:secret@" that a clone created outside WB can carry in its remote, and
// that git reproduces verbatim in its failure output.
var credentialedURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s@]+@`)

// redactCredentials removes userinfo from URLs in content. The report is a
// file on disk that gets pasted into agent contexts and issue trackers, and a
// token that reaches it is a token that has leaked.
func redactCredentials(content string) string {
	return credentialedURL.ReplaceAllString(content, "${1}REDACTED@")
}

// syncIssuesReportName is the stable filename under WB home. It is stable on
// purpose: the path is the instruction handed to an agent ("read
// ~/.wb/last-sync-issues.md and fix what it lists"), which a timestamped
// directory could not be.
const syncIssuesReportName = "last-sync-issues.md"

// writeSyncIssuesReport renders and writes the issues report, printing the
// path it wrote to out and any failure to errOut. It never fails a sync, so it
// returns nothing for a caller to check: sync's exit code reflects sync, never
// its reporting — the same policy finishSync already applies to a failed
// --publish and to refreshSyncedCheckoutMarkers.
func writeSyncIssuesReport(
	meta fleetsync.RunMeta,
	results []fleetsync.Result,
	projectsRoot string,
	out, errOut io.Writer,
) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "sync issues report not written:", err)
		return
	}
	path := filepath.Join(home, syncIssuesReportName)
	content := redactCredentials(fleetsync.IssuesMarkdown(meta, results))
	if err := writeSyncIssuesFile(path, content); err != nil {
		_, _ = fmt.Fprintln(errOut, "sync issues report not written:", err)
		return
	}
	groups := fleetsync.Summary(results)
	attention, _ := fleetsync.SummaryGroupByLabel(groups, "Needs attention")
	errors, _ := fleetsync.SummaryGroupByLabel(groups, "Errors")
	_, _ = fmt.Fprintf(out, "Sync issues: %d records — errors on %d repos and %d repos require attention; details in %s\n",
		len(errors.Results)+len(attention.Results), len(errors.Results), len(attention.Results), path)
}

// writeSyncIssuesFile replaces the report through a temporary file in the same
// directory. An agent reads this path unprompted, so it must never observe a
// half-written report: rename is atomic, a partial write is not.
func writeSyncIssuesFile(path, contents string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wb-sync-issues-*")
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	// 0o600, not something wider: the report carries verbatim git output,
	// which can include a credentialed remote URL (a clone made outside WB,
	// e.g. https://x-access-token:TOKEN@github.com/o/r.git, reproduces it
	// verbatim in failure text). os.CreateTemp already yields 0600; this
	// makes that guarantee explicit rather than implicit, matching
	// archiveprune's receipt files.
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
