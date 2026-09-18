package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLayoutAuditAcceptsALiteralHostLevelClone encodes
// projects-root-layout#ac:clone-path-inverts-to-url at the command level: a
// correctly placed <root>/<host>/<org>/<repo> canonical clone is reported ok
// and is never called misowned, so the host level is not "fixed" away.
func TestLayoutAuditAcceptsALiteralHostLevelClone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "github.com", "dal-go", "dalgo")
	initOriginRepository(t, canonical, "dal-go/dalgo")

	audit := runWB(t, "layout", "audit", "--projects-root", root, "--format", "json")
	if audit.exitCode != exitOK {
		t.Fatalf("audit exit = %d, want ok; stderr=%s stdout=%s", audit.exitCode, audit.stderr, audit.stdout)
	}
	var report struct {
		Summary struct {
			OK       int `json:"ok"`
			Misowned int `json:"misowned"`
			BadHost  int `json:"bad_host"`
		} `json:"summary"`
		Findings []struct {
			Path      string `json:"path"`
			Kind      string `json:"kind"`
			RemoteURL string `json:"remote_url"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(audit.stdout), &report); err != nil {
		t.Fatalf("decode audit: %v\n%s", err, audit.stdout)
	}
	if report.Summary.OK != 1 || report.Summary.Misowned != 0 || report.Summary.BadHost != 0 {
		t.Fatalf("summary = %+v, want exactly one ok finding", report.Summary)
	}
	if len(report.Findings) != 1 || report.Findings[0].Kind != "ok" || report.Findings[0].Path != canonical {
		t.Fatalf("findings = %+v", report.Findings)
	}
	// The AC's inversion, stated where an operator sees it. The clone's origin
	// is a local bare repository here, so this URL can only come from the path.
	if report.Findings[0].RemoteURL != "https://github.com/dal-go/dalgo" {
		t.Fatalf("remote_url = %q, want https://github.com/dal-go/dalgo", report.Findings[0].RemoteURL)
	}

	markdown := runWB(t, "layout", "audit", "--projects-root", root)
	if markdown.exitCode != exitOK {
		t.Fatalf("markdown audit exit = %d; stderr=%s", markdown.exitCode, markdown.stderr)
	}
	if !strings.Contains(markdown.stdout, "https://github.com/dal-go/dalgo") {
		t.Fatalf("markdown audit does not report the inverted remote URL:\n%s", markdown.stdout)
	}
}

// TestLayoutAuditReportsANonHostnameFirstLevelWithoutFixingTheFleet encodes the
// finding half of projects-root-layout#ac:clone-path-inverts-to-url for the
// legacy two-level placement this machine still uses: audit reports it,
// `clean --apply` leaves every clone exactly where it is.
func TestLayoutAuditReportsANonHostnameFirstLevelWithoutFixingTheFleet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := filepath.Join(root, "sneat-dev", "wb")
	initOriginRepository(t, legacy, "sneat-dev/wb")

	audit := runWB(t, "layout", "audit", "--projects-root", root, "--format", "json")
	if audit.exitCode != exitFindings {
		t.Fatalf("audit exit = %d, want findings; stderr=%s stdout=%s", audit.exitCode, audit.stderr, audit.stdout)
	}
	if !strings.Contains(audit.stdout, `"kind": "bad_host"`) {
		t.Fatalf("audit did not report the non-hostname first level:\n%s", audit.stdout)
	}

	clean := runWB(t, "layout", "clean", "--projects-root", root, "--apply", "--format", "json")
	if clean.exitCode != exitOK {
		t.Fatalf("clean exit = %d; stderr=%s stdout=%s", clean.exitCode, clean.stderr, clean.stdout)
	}
	var cleaned struct {
		Actions []struct {
			Status string `json:"status"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(clean.stdout), &cleaned); err != nil {
		t.Fatalf("decode clean: %v\n%s", err, clean.stdout)
	}
	if len(cleaned.Actions) != 0 {
		t.Fatalf("clean acted on the legacy first level: %+v", cleaned.Actions)
	}
	if _, err := os.Stat(filepath.Join(legacy, "README.md")); err != nil {
		t.Fatalf("clean must leave the legacy clone in place: %v", err)
	}
}

// TestProjectsRootHelpNamesTheHostLevel pins the documented shape of the flag
// this layout derives from.
func TestProjectsRootHelpNamesTheHostLevel(t *testing.T) {
	t.Parallel()
	help := runWB(t, "--help")
	if help.exitCode != exitOK {
		t.Fatalf("help exit = %d; stderr=%s", help.exitCode, help.stderr)
	}
	if !strings.Contains(help.stdout, "{host}/{org}/{repo}") {
		t.Fatalf("--projects-root help does not name the host level:\n%s", help.stdout)
	}

	layoutHelp := runWB(t, "layout", "--help")
	if layoutHelp.exitCode != exitOK {
		t.Fatalf("layout help exit = %d; stderr=%s", layoutHelp.exitCode, layoutHelp.stderr)
	}
	if !strings.Contains(layoutHelp.stdout, "{host}/{owner}/{repository}") {
		t.Fatalf("wb layout help does not name the host level:\n%s", layoutHelp.stdout)
	}
}
