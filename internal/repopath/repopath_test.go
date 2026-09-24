package repopath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestClonePathInvertsToRemoteURL encodes
// projects-root-layout#ac:clone-path-inverts-to-url at the unit level: a
// canonical clone at <root>/github.com/dal-go/dalgo inverts to
// https://github.com/dal-go/dalgo without reading any configuration or the
// repository's own remote.
func TestClonePathInvertsToRemoteURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	// This clone's origin is a local bare repository, so nothing about the
	// remote can produce "https://github.com/dal-go/dalgo". Inverting the path
	// must not consult it.
	initLocalCloneWithUnrelatedOrigin(t, root, canonical)

	got, err := RemoteURLForLocalPath(root, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/dal-go/dalgo" {
		t.Fatalf("RemoteURLForLocalPath(%q) = %q, want https://github.com/dal-go/dalgo", canonical, got)
	}
}

// TestClonePathInversionDoesNotReadAnyRemote proves the inversion is pure path
// arithmetic even when the clone has no origin remote at all.
func TestClonePathInversionDoesNotReadAnyRemote(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "github.com", "acme", "app")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, canonical, "init", "-b", "main")
	if got, err := FromLocalPath(root, canonical); err != nil || got.RemoteURL() != "https://github.com/acme/app" {
		t.Fatalf("inversion with no origin remote = %q, %v", got.RemoteURL(), err)
	}
}

// TestClonePathInversionCoversTheFleetSlugs proves the inverse holds for
// multi-segment hosted paths and for the owner/repository pairs actually
// present in this machine's fleet.
func TestClonePathInversionCoversTheFleetSlugs(t *testing.T) {
	t.Parallel()
	root := "/projects"
	for _, slug := range []string{
		"github.com/dal-go/dalgo",
		"github.com/sneat-dev/wb",
		"github.com/sneat-co/backstage",
		"gitlab.example.test/sneat-dev/wb",
		"github.com:8443/acme/app",
	} {
		address, err := ParseRelative(slug)
		if err != nil {
			t.Fatalf("ParseRelative(%q): %v", slug, err)
		}
		if got, want := address.RemoteURL(), "https://"+slug; got != want {
			t.Fatalf("RemoteURL(%q) = %q, want %q", slug, got, want)
		}
		if got, want := mustRemoteURL(t, root, address.Path(root)), "https://"+slug; got != want {
			t.Fatalf("inverse of %q = %q, want %q", address.Path(root), got, want)
		}
	}
}

// TestFirstLevelEntryThatIsNotAForgeHostIsRejected encodes the second half of
// projects-root-layout#ac:clone-path-inverts-to-url: a first-level entry that
// is not a valid hostname is never silently treated as a forge. The legacy
// two-level placement this machine still uses is the motivating case.
func TestFirstLevelEntryThatIsNotAForgeHostIsRejected(t *testing.T) {
	t.Parallel()
	root := "/projects"
	for _, legacy := range []string{
		"sneat-dev/wb",
		"dal-go/dalgo",
		"acme/app",
		"P/somewhere",
		"..",
		".github/workflows",
		"github.com/acme", // two levels, no repository
		"github.com/acme/app/extra",
		"github.com:notaport/acme/app",
	} {
		if _, err := ParseRelative(legacy); err == nil {
			t.Fatalf("ParseRelative(%q) accepted a path with no literal forge hostname", legacy)
		}
		if _, err := RemoteURLForLocalPath(root, filepath.Join(root, filepath.FromSlash(legacy))); err == nil {
			t.Fatalf("RemoteURLForLocalPath(%q) invented a remote for a non-forge path", legacy)
		}
	}
}

func TestIsForgeHostAcceptsLiteralHostnamesOnly(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"github.com":             true,
		"GitHub.com":             true,
		"git.example.test":       true,
		"gitlab.example.test":    true,
		"github.com:8443":        true,
		"enterprise.example":     true,
		"dal-go":                 false,
		"sneat-dev":              false,
		"acme":                   false,
		"localhost":              false,
		"":                       false,
		"..":                     false,
		".com":                   false,
		"com.":                   false,
		"github.com.":            false,
		"-github.com":            false,
		"github-.com":            false,
		"github.c0m":             false,
		"github.com:":            false,
		"github.com:0x1":         false,
		"github.com:123456":      false,
		"github.com:8443:8443":   false,
		"a..b":                   false,
		"gіthub.com":             false, // Cyrillic "і" lookalike, not ASCII.
		"git_hub.com":            false,
		"github.com/app":         false,
		" github.com":            false,
		"github.com ":            false,
		"sub.github.com":         true,
		"a.b.c.example.test":     true,
		"really-long-label-exam": false,
	} {
		if got := IsForgeHost(host); got != want {
			t.Errorf("IsForgeHost(%q) = %t, want %t", host, got, want)
		}
	}
}

// TestIsForgeHostRejectsNameOverTwoHundredFiftyThreeCharacters covers the
// length guard on its own: a hostname built from otherwise-valid labels but
// past the 253-character DNS limit must still be refused.
func TestIsForgeHostRejectsNameOverTwoHundredFiftyThreeCharacters(t *testing.T) {
	label := strings.Repeat("a", 50)
	overlong := strings.Join([]string{label, label, label, label, label, "com"}, ".")
	if len(overlong) <= 253 {
		t.Fatalf("fixture host is %d characters, want > 253", len(overlong))
	}
	if IsForgeHost(overlong) {
		t.Fatalf("IsForgeHost(%d-character host) = true, want false", len(overlong))
	}
}

func TestAddressPathAndSlug(t *testing.T) {
	t.Parallel()
	address := Address{Host: "github.com", Org: "dal-go", Repo: "dalgo"}
	if got := address.Relative(); got != "github.com/dal-go/dalgo" {
		t.Fatalf("Relative() = %q", got)
	}
	if got := address.Slug(); got != "dal-go/dalgo" {
		t.Fatalf("Slug() = %q", got)
	}
	if got := address.Path("/projects"); got != filepath.Join("/projects", "github.com", "dal-go", "dalgo") {
		t.Fatalf("Path() = %q", got)
	}
	if !address.EqualFold(Address{Host: "GitHub.com", Org: "DAL-GO", Repo: "Dalgo"}) {
		t.Fatal("EqualFold must ignore case across host, org and repository")
	}
	if address.String() != address.Relative() {
		t.Fatal("String must render the on-disk spelling")
	}
}

func TestFromLocalPathRejectsPathsOutsideTheRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := FromLocalPath(root, filepath.Join(filepath.Dir(root), "github.com", "acme", "app")); err == nil {
		t.Fatal("a path outside the projects root must be rejected")
	}
}

func TestSafeSegmentMatchesRepositoryRules(t *testing.T) {
	t.Parallel()
	if SafeSegment(".github", true) != true || SafeSegment(".github", false) != false {
		t.Fatal("a dot-leading segment is valid only for a repository")
	}
	for _, segment := range []string{"", ".", "..", "-lead", "has space", "sl/ash"} {
		if SafeSegment(segment, true) {
			t.Fatalf("SafeSegment(%q, true) accepted an unsafe segment", segment)
		}
	}
}

func mustRemoteURL(t *testing.T, root, path string) string {
	t.Helper()
	url, err := RemoteURLForLocalPath(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func initLocalCloneWithUnrelatedOrigin(t *testing.T, root, canonical string) {
	t.Helper()
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	runGit(t, seed, "config", "user.email", "wb@example.test")
	runGit(t, seed, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "init")
	remote := filepath.Join(root, "unrelated-remote.git")
	runGit(t, root, "clone", "--bare", seed, remote)
	if err := os.RemoveAll(canonical); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "clone", remote, canonical)
	if out, err := exec.Command("git", "-C", canonical, "remote", "get-url", "origin").CombinedOutput(); err != nil {
		t.Fatalf("clone has no origin: %v: %s", err, out)
	} else if strings.Contains(string(out), "github.com") {
		t.Fatalf("fixture origin unexpectedly mentions github.com: %s", out)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

// TestFromCloneURLPlacesACloneOnItsOwnForge proves the clone destination comes
// from the URL the repository is cloned from, and that a URL naming no literal
// forge keeps the legacy two-level placement rather than inventing a host.
func TestFromCloneURLPlacesACloneOnItsOwnForge(t *testing.T) {
	t.Parallel()
	root := "/projects"
	for _, test := range []struct {
		cloneURL string
		want     string
	}{
		{cloneURL: "git@github.com:acme/app.git", want: "github.com/acme/app"},
		{cloneURL: "https://github.com/acme/app.git", want: "github.com/acme/app"},
		{cloneURL: "ssh://git@git.example.test/acme/app.git", want: "git.example.test/acme/app"},
		{cloneURL: "https://github.com:8443/acme/app.git", want: "github.com:8443/acme/app"},
		{cloneURL: "/srv/remotes/acme/app.git", want: "acme/app"},
		{cloneURL: "file:///srv/remotes/acme/app.git", want: "acme/app"},
		{cloneURL: "https://sneat-dev:secret@github.com/acme/app.git", want: "acme/app"},
		{cloneURL: "", want: "acme/app"},
		{cloneURL: "ssh://git@localhost:2222/acme/app.git", want: "acme/app"},
	} {
		address := FromCloneURL(test.cloneURL, "acme", "app")
		if got := address.Relative(); got != test.want {
			t.Fatalf("FromCloneURL(%q).Relative() = %q, want %q", test.cloneURL, got, test.want)
		}
		if got, want := address.Path(root), filepath.Join(root, filepath.FromSlash(test.want)); got != want {
			t.Fatalf("FromCloneURL(%q).Path() = %q, want %q", test.cloneURL, got, want)
		}
	}
}

// TestOwnersReadsThroughTheLiteralHostLevel is the discovery primitive every
// inventory walk shares: both the host-level and the legacy two-level shape
// must be listed together, and only a literal hostname may be read through.
func TestOwnersReadsThroughTheLiteralHostLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, directory := range []string{
		"github.com/acme/",
		"github.com/dal-go/",
		"git.example.test/acme/",
		"acme/",
		"dal-go/",
		".wb/",
		".worktrees/",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A file at the first level is not an owner directory.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	owners, unreadable := Owners(root)
	if len(unreadable) != 0 {
		t.Fatalf("unreadable = %v", unreadable)
	}
	var got []string
	for _, owner := range owners {
		got = append(got, filepath.ToSlash(filepath.Join(owner.Host, owner.Name)))
	}
	want := []string{"acme", "dal-go", "git.example.test/acme", "github.com/acme", "github.com/dal-go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Owners() = %v, want %v", got, want)
	}
	for _, owner := range owners {
		if owner.Path != filepath.Join(root, owner.Host, owner.Name) && owner.Host != "" {
			t.Fatalf("owner path = %q for host %q name %q", owner.Path, owner.Host, owner.Name)
		}
	}
}

// TestOwnersAcceptsAPortHostedForgeLevel pins the predicate consistency the
// placement rules already allow: a forge with an explicit port is a valid
// first level, so discovery must not filter it out.
func TestOwnersAcceptsAPortHostedForgeLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "github.com:8443", "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	owners, unreadable := Owners(root)
	if len(unreadable) != 0 || len(owners) != 1 {
		t.Fatalf("Owners() = %v, unreadable %v; want one owner for the port-hosted forge", owners, unreadable)
	}
	if owners[0].Host != "github.com:8443" || owners[0].Name != "acme" {
		t.Fatalf("owner = %+v, want host github.com:8443 name acme", owners[0])
	}
	if !SafeOwnerSegment("github.com:8443") || SafeOwnerSegment(".wb") || SafeOwnerSegment("dal-go") != true {
		t.Fatal("SafeOwnerSegment must accept a port-hosted forge and a plain owner, and reject a dot-name")
	}
}

func TestOwnersReportsUnreadableHostDirectoriesInsteadOfFailing(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny reads")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "github.com")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	owners, unreadable := Owners(root)
	if len(owners) != 0 || len(unreadable) != 1 {
		t.Fatalf("Owners() = %v, unreadable %v; want one diagnostic and no owners", owners, unreadable)
	}
	if !strings.Contains(unreadable[0], "github.com") {
		t.Fatalf("diagnostic = %q", unreadable[0])
	}
}
