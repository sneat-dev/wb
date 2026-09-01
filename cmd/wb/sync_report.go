package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/wbhome"
)

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
	if err := writeSyncIssuesFile(path, fleetsync.IssuesMarkdown(meta, results)); err != nil {
		_, _ = fmt.Fprintln(errOut, "sync issues report not written:", err)
		return
	}
	_, _ = fmt.Fprintf(out, "Issues report: %s\n", path)
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
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
