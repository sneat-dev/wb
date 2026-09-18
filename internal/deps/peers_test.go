package deps

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// "Can I reuse this package here" was answered by running the install and
// interpreting whatever the package manager said about peer conflicts. That
// mutates the checkout to find out, and the warnings do not distinguish "two
// majors behind" from "the publisher marked this optional". These tests pin
// the five verdicts that replace it.

func newPeerTargetCheckout(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		writeTestFile(t, filepath.Join(root, path), body)
	}
	return root
}

const peerTargetPackageJSON = `{
  "name": "@acme/host",
  "dependencies": {
    "react": "^18.0.0",
    "@acme/old": "^1.0.0"
  },
  "devDependencies": {
    "typescript": "5.4.2"
  }
}
`

const peerTargetPnpmLock = `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      react:
        specifier: ^18.0.0
        version: 18.3.1
      '@acme/old':
        specifier: ^1.0.0
        version: 1.4.0
    devDependencies:
      typescript:
        specifier: 5.4.2
        version: 5.4.2
`

func peerOptions(t *testing.T, root string, set PublishedPeerSet) PeerOptions {
	t.Helper()
	return PeerOptions{
		Package: "@acme/widget", Against: root,
		Now: func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) },
		PublishedPeers: func(context.Context, string) (PublishedPeerSet, error) {
			return set, nil
		},
	}
}

func peerRowByName(t *testing.T, report PeerReport, name string) PeerRow {
	t.Helper()
	for _, row := range report.Peers {
		if row.Peer == name {
			return row
		}
	}
	t.Fatalf("no row for %q in %+v", name, report.Peers)
	return PeerRow{}
}

func TestInspectPeersJudgesEveryPublishedRequirementAgainstTheLockedVersion(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{
		"package.json":   peerTargetPackageJSON,
		"pnpm-lock.yaml": peerTargetPnpmLock,
	})

	report, err := InspectPeers(context.Background(), peerOptions(t, root, PublishedPeerSet{
		Version: "2.1.0",
		Peers: map[string]string{
			"react":      "^18.0.0",
			"@acme/old":  "^2.0.0",
			"vue":        "^3.0.0",
			"@acme/opt":  "^1.0.0",
			"typescript": "5.0.0 - 6.0.0",
		},
		Optional: map[string]bool{"@acme/opt": true},
		Source:   "test registry",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if report.Package != "@acme/widget" || report.Version != "2.1.0" || report.AgainstName != "@acme/host" {
		t.Fatalf("report header = %+v", report)
	}

	// The satisfied row is judged against 18.3.1 — what the lockfile actually
	// installs — not against the caret range the manifest declares. A range
	// cannot be compared to a range; only an installed version answers this.
	react := peerRowByName(t, report, "react")
	if react.Verdict != PeerSatisfied || react.Installed != "18.3.1" || !strings.Contains(react.InstalledSource, "pnpm-lock.yaml") {
		t.Fatalf("react row = %+v, want satisfied against the locked version", react)
	}
	old := peerRowByName(t, report, "@acme/old")
	if old.Verdict != PeerUnsatisfied || old.Installed != "1.4.0" || !strings.Contains(old.Reason, "^2.0.0") {
		t.Fatalf("@acme/old row = %+v, want an unsatisfied verdict naming the range", old)
	}
	if missing := peerRowByName(t, report, "vue"); missing.Verdict != PeerMissing || missing.Installed != "" {
		t.Fatalf("vue row = %+v, want missing", missing)
	}
	// Optional peers are the whole reason a naive install warning is
	// unreadable: absent-and-optional is not a problem.
	if optional := peerRowByName(t, report, "@acme/opt"); optional.Verdict != PeerOptionalMissing || !optional.Optional {
		t.Fatalf("@acme/opt row = %+v, want optional_missing", optional)
	}
	// A hyphen range is a distinct grammar outside the evaluated subset; WB
	// says so instead of guessing in either direction.
	unevaluated := peerRowByName(t, report, "typescript")
	if unevaluated.Verdict != PeerUnevaluated || unevaluated.Reason == "" {
		t.Fatalf("typescript row = %+v, want unevaluated with a reason", unevaluated)
	}

	want := PeerSummary{Total: 5, Satisfied: 1, Unsatisfied: 1, Missing: 1, OptionalMissing: 1, Unevaluated: 1}
	if report.Summary != want {
		t.Fatalf("summary = %+v, want %+v", report.Summary, want)
	}
	if !PeersFailed(report) {
		t.Fatal("a report with an unsatisfied and a missing peer must fail")
	}
}

// An unevaluated row must never be reported as a pass. This is the failure
// mode that makes a guessing tool worse than no tool.
func TestPeersFailedIgnoresUnevaluatedButTheReportRefusesToCallItAPass(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{
		"package.json":   peerTargetPackageJSON,
		"pnpm-lock.yaml": peerTargetPnpmLock,
	})

	report, err := InspectPeers(context.Background(), peerOptions(t, root, PublishedPeerSet{
		Version: "2.1.0",
		Peers:   map[string]string{"react": "^18.0.0", "typescript": "5.0.0 - 6.0.0"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if PeersFailed(report) {
		t.Fatalf("an unevaluated row is not a finding: %+v", report.Summary)
	}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "not the same as judging them compatible") {
		t.Fatalf("markdown must refuse to call an unevaluated row a pass:\n%s", markdown)
	}
	if strings.Contains(markdown, "Every peer requirement is met") {
		t.Fatalf("markdown claims a clean pass while a row was unevaluated:\n%s", markdown)
	}
}

func TestInspectPeersReportsACleanTargetAsReusable(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{
		"package.json":   peerTargetPackageJSON,
		"pnpm-lock.yaml": peerTargetPnpmLock,
	})

	report, err := InspectPeers(context.Background(), peerOptions(t, root, PublishedPeerSet{
		Version: "2.1.0", Peers: map[string]string{"react": "^18.0.0"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if PeersFailed(report) {
		t.Fatalf("summary = %+v, want no findings", report.Summary)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "Every peer requirement is met") {
		t.Fatalf("markdown = %s", markdown)
	}
}

// "Requires nothing of its host" is a legitimate — and maximally reusable —
// answer, not an error or an empty screen.
func TestInspectPeersSaysSoWhenAPackageDeclaresNoPeers(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{"package.json": peerTargetPackageJSON})

	report, err := InspectPeers(context.Background(), peerOptions(t, root, PublishedPeerSet{Version: "2.1.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Peers) != 0 || PeersFailed(report) {
		t.Fatalf("report = %+v", report)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "requires nothing of its host") {
		t.Fatalf("markdown = %s", markdown)
	}
}

// Without a lockfile there is no installed version, only a declared range. WB
// says which it used rather than presenting a range as an installed version.
func TestInspectPeersLabelsADeclaredSpecifierWithNoLockfileEvidence(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{"package.json": peerTargetPackageJSON})

	report, err := InspectPeers(context.Background(), peerOptions(t, root, PublishedPeerSet{
		Version: "2.1.0", Peers: map[string]string{"react": "^18.0.0"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	row := peerRowByName(t, report, "react")
	if !strings.Contains(row.InstalledSource, "no lockfile evidence") {
		t.Fatalf("row = %+v, want the source to admit there was no lockfile", row)
	}
	// "^18.0.0" is not an exact version, so the comparison is unevaluated
	// rather than silently treated as a match.
	if row.Verdict != PeerUnevaluated {
		t.Fatalf("row = %+v, want a declared range to be unevaluated, not assumed satisfied", row)
	}
}

func TestInspectPeersRefusesIncompleteInput(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{"package.json": peerTargetPackageJSON})

	if _, err := InspectPeers(context.Background(), PeerOptions{Against: root}); err == nil || !strings.Contains(err.Error(), "package name") {
		t.Fatalf("error = %v, want a refusal naming the missing package", err)
	}
	if _, err := InspectPeers(context.Background(), PeerOptions{Package: "@acme/widget"}); err == nil || !strings.Contains(err.Error(), "--against") {
		t.Fatalf("error = %v, want a refusal naming --against", err)
	}
	missing := filepath.Join(root, "nope")
	if _, err := InspectPeers(context.Background(), PeerOptions{Package: "@acme/widget", Against: missing}); err == nil {
		t.Fatal("a nonexistent --against path must be an error")
	}
	file := filepath.Join(root, "package.json")
	if _, err := InspectPeers(context.Background(), PeerOptions{Package: "@acme/widget", Against: file}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want a refusal that --against is not a directory", err)
	}
}

func TestInspectPeersSurfacesARegistryFailure(t *testing.T) {
	root := newPeerTargetCheckout(t, map[string]string{"package.json": peerTargetPackageJSON})
	want := errors.New("404 Not Found")

	options := peerOptions(t, root, PublishedPeerSet{})
	options.PublishedPeers = func(context.Context, string) (PublishedPeerSet, error) { return PublishedPeerSet{}, want }
	if _, err := InspectPeers(context.Background(), options); !errors.Is(err, want) {
		t.Fatalf("error = %v, want the registry's own failure", err)
	}
}

func TestPeerPackageNameStripsAPinnedVersionButKeepsTheScope(t *testing.T) {
	t.Parallel()
	for reference, want := range map[string]string{
		"@sneat/core":        "@sneat/core",
		"@sneat/core@0.31.0": "@sneat/core",
		"react":              "react",
		"react@18.3.1":       "react",
	} {
		if got := peerPackageName(reference); got != want {
			t.Fatalf("peerPackageName(%q) = %q, want %q", reference, got, want)
		}
	}
}

func TestParsePublishedPeersReadsTheMultiFieldRegistryObject(t *testing.T) {
	t.Parallel()
	set, err := parsePublishedPeers(`{
  "version": "2.1.0",
  "peerDependencies": {"react": "^18.0.0", "vue": "^3.0.0"},
  "peerDependenciesMeta": {"vue": {"optional": true}}
}`)
	if err != nil {
		t.Fatal(err)
	}
	if set.Version != "2.1.0" || set.Peers["react"] != "^18.0.0" || !set.Optional["vue"] || set.Optional["react"] {
		t.Fatalf("set = %+v", set)
	}
	empty, err := parsePublishedPeers("undefined")
	if err != nil || len(empty.Peers) != 0 {
		t.Fatalf("empty = %+v, err = %v", empty, err)
	}
}
