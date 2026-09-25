package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sneat-dev/wb/internal/filewrite"
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
	return writeSyncIssuesFileInjected(path, contents, nil)
}

// writeSyncIssuesFileInjected is writeSyncIssuesFile's test seam (task-9
// PR-2): every production call site reaches it only through
// writeSyncIssuesFile, which always passes a nil *filewrite.Injector, so
// production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/write/close/chmod/rename failure branch
// deterministically. The chmod runs path-based, after close, exactly as
// before, so it uses filewrite.ChmodPath rather than filewrite.Chmod.
func writeSyncIssuesFileInjected(path, contents string, inj *filewrite.Injector) error {
	directory := filepath.Dir(path)
	temporary, err := filewrite.CreateTemp(directory, ".wb-sync-issues-*", inj)
	if err != nil {
		return fmt.Errorf("stage a replacement for %s: %w", path, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := filewrite.Write(temporary, []byte(contents), name, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := filewrite.Close(temporary, name, inj); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	// 0o600, not something wider: the report carries verbatim git output,
	// which can include a credentialed remote URL (a clone made outside WB,
	// e.g. https://x-access-token:TOKEN@github.com/o/r.git, reproduces it
	// verbatim in failure text). os.CreateTemp already yields 0600; this
	// makes that guarantee explicit rather than implicit, matching
	// archiveprune's receipt files.
	if err := filewrite.ChmodPath(name, 0o600, inj); err != nil {
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := filewrite.Rename(name, path, inj); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
