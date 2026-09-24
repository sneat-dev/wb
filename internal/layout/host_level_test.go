package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestAuditDoesNotReportAHostLevelCloneAsMisowned encodes the layout half of
// projects-root-layout#ac:clone-path-inverts-to-url: a correctly placed
// <root>/<host>/<org>/<repo> canonical clone is reported ok, not misowned.
func TestAuditDoesNotReportAHostLevelCloneAsMisowned(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := hostedClone(t, root, "github.com", "dal-go", "dalgo", "git@github.com:dal-go/dalgo.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly the canonical clone", report.Findings)
	}
	finding := report.Findings[0]
	if finding.Kind != KindOK {
		t.Fatalf("kind = %s (reason %q), want ok for %s", finding.Kind, finding.Reason, canonical)
	}
	if finding.Path != canonical || finding.PathSlug != "dal-go/dalgo" {
		t.Fatalf("finding = %+v", finding)
	}
	if Failed(report) {
		t.Fatalf("a correctly placed host-level clone must not fail the audit: %+v", report.Summary)
	}
}

// TestAuditReportsANonHostnameFirstLevelAsALayoutFinding encodes the second
// half of projects-root-layout#ac:clone-path-inverts-to-url: this machine's
// legacy {owner}/{repository} first level is not a literal forge hostname, so
// it is reported as a layout finding — and its clones are still read in place
// rather than being moved or "fixed".
func TestAuditReportsANonHostnameFirstLevelAsALayoutFinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := legacyClone(t, root, "dal-go", "dalgo", "git@github.com:dal-go/dalgo.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	badHost := findingFor(t, report, KindBadHost)
	if badHost.Path != filepath.Join(root, "dal-go") || badHost.PathSlug != "dal-go" {
		t.Fatalf("bad-host finding = %+v", badHost)
	}
	if !strings.Contains(badHost.Reason, "not a literal forge hostname") {
		t.Fatalf("bad-host reason = %q", badHost.Reason)
	}
	// The legacy clone itself stays visible and operable.
	ok := findingFor(t, report, KindOK)
	if ok.Path != legacy {
		t.Fatalf("legacy clone finding = %+v, want %s", ok, legacy)
	}
	if report.Summary.BadHost != 1 || !Failed(report) {
		t.Fatalf("summary = %+v, want one bad-host finding", report.Summary)
	}
	// Clean must never treat the legacy first level as removable.
	for _, apply := range []bool{false, true} {
		clean, cleanErr := Clean(context.Background(), root, CleanOptions{Apply: apply})
		if cleanErr != nil {
			t.Fatal(cleanErr)
		}
		if len(clean.Actions) != 0 {
			t.Fatalf("clean(apply=%t) planned %+v, want no action on a legacy owner directory", apply, clean.Actions)
		}
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("the legacy clone must survive clean: %v", err)
	}
}

// TestAuditReportsAHostLevelCloneFromAnotherForgeOrOwnerAsMisowned proves the
// host level is the literal forge: a path host that the origin disagrees with
// is misowned, and the expected path keeps the whole host/owner/repository
// address.
func TestAuditReportsAHostLevelCloneFromAnotherForgeOrOwnerAsMisowned(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wrongForge := hostedClone(t, root, "github.com", "dal-go", "dalgo", "git@gitlab.example.test:dal-go/dalgo.git")
	wrongOwner := hostedClone(t, root, "github.com", "acme", "app", "git@github.com:acme/other.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	findings := findingsFor(report, KindMisowned)
	if len(findings) != 2 {
		t.Fatalf("misowned findings = %+v, want two", findings)
	}
	for _, finding := range findings {
		switch finding.Path {
		case wrongForge:
			if finding.OriginSlug != "dal-go/dalgo" {
				t.Fatalf("wrong-forge finding = %+v", finding)
			}
		case wrongOwner:
			if finding.ExpectedPath != filepath.Join(root, "github.com", "acme", "other") {
				t.Fatalf("wrong-owner expected path = %q", finding.ExpectedPath)
			}
		default:
			t.Fatalf("unexpected misowned path %s", finding.Path)
		}
	}
}

// TestAuditReportsAHostLevelMissingItsOrganizationLevel covers a checkout
// placed directly under a host directory: {host}/{repository} is not a
// canonical clone address.
func TestAuditReportsAHostLevelMissingItsOrganizationLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	address := filepath.Join(root, "github.com", "dalgo")
	if err := os.MkdirAll(address, 0o755); err != nil {
		t.Fatal(err)
	}
	checkoutWithOrigin(t, address, "git@github.com:dal-go/dalgo.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	finding := findingFor(t, report, KindMisowned)
	if finding.Path != address || finding.PathSlug != "github.com/dalgo" {
		t.Fatalf("finding = %+v", finding)
	}
	if !strings.Contains(finding.Reason, "must be followed by {org}/{repository}") {
		t.Fatalf("reason = %q", finding.Reason)
	}
}

// TestAuditReportsAHostLevelTopLevelCloneAgainstItsHostPath checks a checkout
// sitting directly under the projects root: the canonical path it is compared
// against now carries the literal host level.
func TestAuditReportsAHostLevelTopLevelCloneAgainstItsHostPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	top := filepath.Join(root, "dalgo")
	checkoutWithOrigin(t, top, "git@github.com:dal-go/dalgo.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	finding := findingFor(t, report, KindTopLevel)
	want := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if finding.ExpectedPath != want {
		t.Fatalf("expected path = %q, want %q", finding.ExpectedPath, want)
	}
	if finding.CanonicalExists {
		t.Fatal("no canonical clone exists yet")
	}
	if !strings.Contains(finding.Reason, "github.com/dal-go/dalgo") {
		t.Fatalf("reason = %q", finding.Reason)
	}
}

func TestOriginAddressReportsTheLiteralHostAndSlug(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()

	for _, test := range []struct {
		name string
		url  string
		host string
		slug string
	}{
		{name: "scp style", url: "git@github.com:dal-go/dalgo.git", host: "github.com", slug: "dal-go/dalgo"},
		{name: "https style", url: "https://github.com/acme/app.git", host: "github.com", slug: "acme/app"},
		{name: "self hosted", url: "https://git.example.test/team/app.git", host: "git.example.test", slug: "team/app"},
		{name: "explicit port", url: "https://github.com:8443/team/app.git", host: "github.com:8443", slug: "team/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, dir, "git", "init", "-b", "main")
			run(t, dir, "git", "remote", "add", "origin", test.url)

			address, err := OriginAddress(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if address.Host != test.host || address.Slug() != test.slug {
				t.Fatalf("OriginAddress(%q) = %+v, want %s/%s", test.url, address, test.host, test.slug)
			}
			if got, want := address.RemoteURL(), "https://"+test.host+"/"+test.slug; got != want {
				t.Fatalf("RemoteURL() = %q, want %q", got, want)
			}
		})
	}

	// A local-only remote names no forge at all.
	local := filepath.Join(root, "local")
	seed := filepath.Join(root, "local-seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "init", "-b", "main")
	run(t, local, "git", "init", "-b", "main")
	run(t, local, "git", "remote", "add", "origin", filepath.Join(seed, "acme", "app.git"))
	address, err := OriginAddress(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if address.Host != "" || address.Slug() != "acme/app" {
		t.Fatalf("local-only origin address = %+v", address)
	}

	if _, err := OriginAddress(context.Background(), filepath.Join(root, "absent")); err == nil {
		t.Fatal("OriginAddress on a non-repository must fail")
	}
}

func TestAuditReportsUnreadableHostAndOrganizationDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny reads")
	}
	t.Parallel()
	root := t.TempDir()
	lockedHost := filepath.Join(root, "github.com", "locked-org")
	if err := os.MkdirAll(lockedHost, 0o755); err != nil {
		t.Fatal(err)
	}
	lockedOrg := filepath.Join(root, "gitlab.example.test", "locked-org")
	if err := os.MkdirAll(lockedOrg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedHost, 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedOrg, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(lockedHost, 0o755)
		_ = os.Chmod(lockedOrg, 0o755)
	})

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Unreadable != 2 {
		t.Fatalf("summary = %+v, want two unreadable findings", report.Summary)
	}
	if !Failed(report) {
		t.Fatal("unreadable findings must fail the audit")
	}
}

func findingFor(t *testing.T, report Report, kind Kind) Finding {
	t.Helper()
	findings := findingsFor(report, kind)
	if len(findings) != 1 {
		t.Fatalf("findings of kind %s = %+v, want exactly one (all: %+v)", kind, findings, report.Findings)
	}
	return findings[0]
}

func findingsFor(report Report, kind Kind) []Finding {
	var findings []Finding
	for _, finding := range report.Findings {
		if finding.Kind == kind {
			findings = append(findings, finding)
		}
	}
	return findings
}

// checkoutWithOrigin initializes a repository at path with one origin remote.
func checkoutWithOrigin(t *testing.T, path, originURL string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, path, "git", "init", "-b", "main")
	run(t, path, "git", "config", "user.email", "wb@example.test")
	run(t, path, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, path, "git", "add", ".")
	run(t, path, "git", "commit", "-m", "init")
	run(t, path, "git", "remote", "add", "origin", originURL)
}

// hostedClone builds a real clone at <root>/<host>/<org>/<repo> whose origin
// is originURL. The seed and bare remote live outside root so the audit sees
// only the clone under test.
func hostedClone(t *testing.T, root, host, org, name, originURL string) string {
	t.Helper()
	seedRoot := t.TempDir()
	seed := filepath.Join(seedRoot, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "init", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(org+"/"+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "init")
	remote := filepath.Join(seedRoot, "remote.git")
	run(t, seedRoot, "git", "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	canonical := filepath.Join(root, host, org, name)
	cloneFrom(t, remote, canonical)
	run(t, canonical, "git", "remote", "set-url", "origin", originURL)
	return canonical
}

// legacyClone builds a real clone at the legacy <root>/<org>/<repo>
// placement, which is where this machine's fleet still sits.
func legacyClone(t *testing.T, root, org, name, originURL string) string {
	t.Helper()
	seedRoot := t.TempDir()
	seed := filepath.Join(seedRoot, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "init", "-b", "main")
	run(t, seed, "git", "config", "user.email", "wb@example.test")
	run(t, seed, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(org+"/"+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "init")
	remote := filepath.Join(seedRoot, "remote.git")
	run(t, seedRoot, "git", "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	canonical := filepath.Join(root, org, name)
	cloneFrom(t, remote, canonical)
	run(t, canonical, "git", "remote", "set-url", "origin", originURL)
	return canonical
}

// TestAuditReportsTheRemoteURLEachClonePathCorrespondsTo is the AC's inversion
// surfaced where an operator sees it: a clone at <root>/{host}/{org}/{repo}
// reports https://{host}/{org}/{repo}, and the fixture's origin is a local bare
// repository, so the answer cannot have come from reading the remote. A legacy
// clone reports the host its origin names — the host level it must move under —
// and a local-only origin reports nothing.
func TestAuditReportsTheRemoteURLEachClonePathCorrespondsTo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hosted := hostedClone(t, root, "github.com", "dal-go", "dalgo", "git@github.com:dal-go/dalgo.git")
	legacy := legacyClone(t, root, "sneat-dev", "wb", "git@github.com:sneat-dev/wb.git")
	local := legacyClone(t, root, "localco", "app", filepath.Join(t.TempDir(), "remotes", "localco", "app.git"))

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		hosted: "https://github.com/dal-go/dalgo",
		legacy: "https://github.com/sneat-dev/wb",
		local:  "",
	}
	seen := 0
	for _, finding := range report.Findings {
		expected, known := want[finding.Path]
		if !known {
			continue
		}
		seen++
		if finding.RemoteURL != expected {
			t.Fatalf("finding for %s reported remote_url %q, want %q", finding.Path, finding.RemoteURL, expected)
		}
	}
	if seen != len(want) {
		t.Fatalf("audit examined %d of %d clones: %+v", seen, len(want), report.Findings)
	}

	// The operator-visible markdown states it too.
	markdown := report.Markdown()
	if !strings.Contains(markdown, "https://github.com/dal-go/dalgo") {
		t.Fatalf("markdown does not report the host-level clone's remote URL:\n%s", markdown)
	}
	if !strings.Contains(markdown, "Remote URL") {
		t.Fatalf("markdown has no remote URL column:\n%s", markdown)
	}
}

// TestAuditInvertsWithoutReadingTheRepositoryOrigin proves the host-level
// answer is pure path arithmetic: the clone's origin is unreachable and names a
// different host, yet the reported URL still follows the path.
func TestAuditInvertsWithoutReadingTheRepositoryOrigin(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hosted := hostedClone(t, root, "github.com", "acme", "app", "git@gitlab.example.test:acme/renamed.git")

	report, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	finding := findingFor(t, report, KindMisowned)
	if finding.Path != hosted {
		t.Fatalf("finding = %+v", finding)
	}
	if finding.RemoteURL != "https://github.com/acme/app" {
		t.Fatalf("remote_url = %q, want the path inversion https://github.com/acme/app", finding.RemoteURL)
	}
}
