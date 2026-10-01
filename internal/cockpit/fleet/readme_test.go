package fleet

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// readmeFixture is a Cockpit server over a snapshotter that has read one real
// clone, github.com/acme/widgets, through the production collectors. The
// clone's README is whatever setup committed.
type readmeFixture struct {
	t      *testing.T
	clone  string
	server *cockpitServer
	id     string
	cookie *http.Cookie
}

// newReadmeFixture makes the clone, runs setup in it (which writes and commits
// whatever it likes on main), and takes one snapshot.
func newReadmeFixture(t *testing.T, setup func(clone string)) *readmeFixture {
	t.Helper()
	root := realTempDir(t)
	clone := filepath.Join(root, "github.com", "acme", "widgets")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "init", "--initial-branch=main")
	setup(clone)
	home := t.TempDir()
	collectors := LocalCollectors{ProjectsRoot: root, Home: home}
	snapshotter, _ := newSnapshotter(collectors.Collectors(nil), nil)
	refreshAndSettle(t, snapshotter)
	f := &readmeFixture{t: t, clone: clone, server: newCockpitServer(t, snapshotter)}
	for _, repository := range snapshotter.Document().Repositories {
		f.id = repository.ID
	}
	f.cookie = f.server.login()
	return f
}

func commitFile(t *testing.T, clone, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(clone, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "--all")
	gitIn(t, clone, "commit", "-m", "add "+name)
}

func (f *readmeFixture) readme(cookie *http.Cookie, headers ...string) (int, string, http.Header) {
	f.t.Helper()
	recorder := f.server.get(ReadmePath+"?repository="+url.QueryEscape(f.id), cookie, headers...)
	return recorder.Code, recorder.Body.String(), recorder.Header()
}

// TestOwnerReadsTheCommittedReadmeAsInertMarkdown proves the owner route serves
// the committed bytes with a markdown type, nosniff and a locked-down policy
// (cockpit#req:repository-readme), and that the working tree is not consulted:
// an uncommitted edit does not change what is served.
func TestOwnerReadsTheCommittedReadmeAsInertMarkdown(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(clone string) {
		commitFile(t, clone, "README.md", "# Widgets\n\n<script>alert(1)</script>\n", 0o644)
	})
	if err := os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("an uncommitted edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, body, header := f.readme(f.cookie)
	if status != http.StatusOK || body != "# Widgets\n\n<script>alert(1)</script>\n" {
		t.Fatalf("README = %d %q", status, body)
	}
	if header.Get("Content-Type") != "text/markdown; charset=utf-8" || header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") || header.Get("Cache-Control") != "no-store" {
		t.Errorf("README headers = %v", header)
	}
}

// TestReadmeNeedsAnOwnerSessionFromTheLoopbackOrigin requires 401 without a
// session and 403 for the hosted origin even holding the owner cookie.
func TestReadmeNeedsAnOwnerSessionFromTheLoopbackOrigin(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "README.md", "secret readme", 0o644) })
	if status, body, _ := f.readme(nil); status != http.StatusUnauthorized || strings.Contains(body, "secret") {
		t.Errorf("README without a session = %d %q", status, body)
	}
	if status, body, _ := f.readme(f.cookie, "Origin", hostedOrigin); status != http.StatusForbidden || strings.Contains(body, "secret") {
		t.Errorf("README from the hosted origin with a cookie = %d %q", status, body)
	}
}

// TestReadmeCommittedAsASymbolicLinkOrNotAFileIsNotServed covers the entries
// that are not a regular file: a symbolic link (to a file outside the
// repository), a directory and a submodule; the executable bit is fine.
func TestReadmeCommittedAsASymbolicLinkOrNotAFileIsNotServed(t *testing.T) {
	t.Parallel()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(clone string){
		"a symbolic link": func(clone string) {
			if err := os.Symlink(outside, filepath.Join(clone, "README.md")); err != nil {
				t.Fatal(err)
			}
			gitIn(t, clone, "add", "README.md")
			gitIn(t, clone, "commit", "-m", "link")
		},
		"a directory": func(clone string) { commitFile(t, clone, "README.md/inner", "x", 0o644) },
		"a submodule": func(clone string) {
			commitFile(t, clone, "keep", "x", 0o644)
			gitIn(t, clone, "update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(gitIn(t, clone, "rev-parse", "HEAD"))+",README.md")
			gitIn(t, clone, "commit", "-m", "submodule")
		},
	} {
		f := newReadmeFixture(t, setup)
		status, body, _ := f.readme(f.cookie)
		if status != http.StatusForbidden || strings.Contains(body, "secret") || !strings.Contains(body, `"readme_not_a_regular_file"`) {
			t.Errorf("%s: README = %d %q, want 403 and no content", name, status, body)
		}
	}
	f := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "README.md", "# run me\n", 0o755) })
	if status, body, _ := f.readme(f.cookie); status != http.StatusOK || body != "# run me\n" {
		t.Errorf("an executable README = %d %q, want it served", status, body)
	}
}

// TestReadmeAbsentOrUnknownIs404 covers a repository with no README, a default
// branch that cannot be told (a detached HEAD and no origin), an unknown id and
// no id; the bodies are short codes.
func TestReadmeAbsentOrUnknownIs404(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "other.md", "x", 0o644) })
	if status, body, _ := f.readme(f.cookie); status != http.StatusNotFound || body != `{"error":"readme_not_found"}`+"\n" {
		t.Errorf("absent README = %d %q", status, body)
	}
	for _, target := range []string{ReadmePath + "?repository=repo-unknown", ReadmePath, ReadmePath + "?repository=" + url.QueryEscape(f.clone)} {
		if recorder := f.server.get(target, f.cookie); recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"unknown_repository"`) {
			t.Errorf("%s = %d %s, want 404 unknown_repository", target, recorder.Code, recorder.Body.String())
		}
	}
	detached := newReadmeFixture(t, func(clone string) {
		commitFile(t, clone, "README.md", "# x", 0o644)
		gitIn(t, clone, "checkout", "--detach")
	})
	if status, body, _ := detached.readme(detached.cookie); status != http.StatusNotFound || !strings.Contains(body, `"default_branch_unknown"`) {
		t.Errorf("README with no default branch = %d %q", status, body)
	}
}

// TestReadmeAtTheCapIsServedAndOverItIs413 states the cap: MaxReadmeBytes is
// one mebibyte; it is measured before it is read.
func TestReadmeAtTheCapIsServedAndOverItIs413(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "README.md", strings.Repeat("a", MaxReadmeBytes), 0o644) })
	if status, body, _ := f.readme(f.cookie); status != http.StatusOK || len(body) != MaxReadmeBytes {
		t.Fatalf("README at the cap = %d with %d bytes", status, len(body))
	}
	big := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "README.md", strings.Repeat("a", MaxReadmeBytes+1), 0o644) })
	if status, body, _ := big.readme(big.cookie); status != http.StatusRequestEntityTooLarge || !strings.Contains(body, `"readme_too_large"`) {
		t.Errorf("README past the cap = %d %q, want 413", status, body)
	}
}

// TestReadmeFailuresAnswerShortCodes maps every README error to its status and
// code without the error's own text.
func TestReadmeFailuresAnswerShortCodes(t *testing.T) {
	t.Parallel()
	for err, want := range map[error][2]any{
		errReadmeAbsent:     {http.StatusNotFound, "readme_not_found"},
		errReadmeNotRegular: {http.StatusForbidden, "readme_not_a_regular_file"},
		errReadmeTooLarge:   {http.StatusRequestEntityTooLarge, "readme_too_large"},
		errBoom:             {http.StatusInternalServerError, "read_failed"},
	} {
		sources := oneRepoSources(t.TempDir())
		sources.readmeErr = err
		snapshotter, _ := newSnapshotter(sources.collectors(), nil)
		refreshAndSettle(t, snapshotter)
		server := newCockpitServer(t, snapshotter)
		var id string
		for _, repository := range snapshotter.Document().Repositories {
			if repository.Route == RouteLocal {
				id = repository.ID
			}
		}
		recorder := server.get(ReadmePath+"?repository="+id, server.login())
		if recorder.Code != want[0] || strings.TrimSpace(recorder.Body.String()) != `{"error":"`+want[1].(string)+`"}` {
			t.Errorf("%v: %d %q, want %v", err, recorder.Code, recorder.Body.String(), want)
		}
	}
}

// TestHostileRepositoryConfigRunsNothingWhenItsReadmeIsServed commits a
// repository whose own config names a filesystem monitor, a hooks path, an ssh
// command and a promisor remote, and serves its README: none of them runs.
func TestHostileRepositoryConfigRunsNothingWhenItsReadmeIsServed(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "hostile.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newReadmeFixture(t, func(clone string) {
		commitFile(t, clone, "README.md", "# fine\n", 0o644)
		hostileConfig(t, clone, script)
	})
	if status, body, _ := f.readme(f.cookie); status != http.StatusOK || body != "# fine\n" {
		t.Fatalf("README = %d %q", status, body)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the repository's own configuration ran a program")
	}
}

// hostileConfig makes clone's .git/config name script wherever Git would run a
// program for it.
func hostileConfig(t *testing.T, clone, script string) {
	t.Helper()
	config := filepath.Join(clone, ".git", "config")
	existing, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	hostile := "\n[core]\n\tfsmonitor = " + script + "\n\thooksPath = " + filepath.Dir(script) + "\n\tsshCommand = " + script + "\n" +
		"[remote \"origin\"]\n\turl = ssh://127.0.0.1:1/never.git\n\tpromisor = true\n\tpartialclonefilter = blob:none\n" +
		"[remote \"evil\"]\n\turl = ext::" + script + "\n\tpromisor = true\n\tpartialclonefilter = blob:none\n" +
		"[protocol]\n\tallow = always\n[protocol \"ext\"]\n\tallow = always\n" +
		"[extensions]\n\tpartialClone = origin\n"
	if err := os.WriteFile(config, append(existing, hostile...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFleetRouteIsMetadataAndReadmeIsOwnerOnly pins the registrations: the
// hosted origin may read the fleet document but not the README.
func TestFleetRouteIsMetadataAndReadmeIsOwnerOnly(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(clone string) { commitFile(t, clone, "README.md", "x", 0o644) })
	hosted := f.server.get(cockpit.APIPrefix+FleetRoute, nil, "Origin", hostedOrigin)
	if hosted.Code != http.StatusOK || hosted.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Errorf("hosted fleet read = %d %v", hosted.Code, hosted.Header())
	}
	post := f.server.get(ReadmePath, nil, "Origin", hostedOrigin)
	if post.Code != http.StatusForbidden || post.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("hosted README read = %d %v", post.Code, post.Header())
	}
}

// TestFleetResponseCarriesAStrongETagAndAnswersIfNoneMatch proves the document
// is served from bytes marshalled once, with conditional requests.
func TestFleetResponseCarriesAStrongETagAndAnswersIfNoneMatch(t *testing.T) {
	t.Parallel()
	snapshotter, clock := newSnapshotter(oneRepoSources(t.TempDir()).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	first := server.get(cockpit.APIPrefix+FleetRoute, nil)
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("first read = %d, ETag %q, Cache-Control %q", first.Code, etag, first.Header().Get("Cache-Control"))
	}
	for _, candidate := range []string{etag, `"other", ` + etag, "*"} {
		if again := server.get(cockpit.APIPrefix+FleetRoute, nil, "If-None-Match", candidate); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Errorf("If-None-Match %q = %d with %d bytes, want 304", candidate, again.Code, again.Body.Len())
		}
	}
	if stale := server.get(cockpit.APIPrefix+FleetRoute, nil, "If-None-Match", `"stale"`); stale.Code != http.StatusOK || stale.Body.String() != first.Body.String() {
		t.Errorf("a stale validator = %d, want the document", stale.Code)
	}
	clock.advance(time.Second)
	if err := snapshotter.RefreshRepository(t.Context(), localRepositoryID(testMachine, oneRepoSources("").repos[0])); err != nil {
		t.Fatal(err)
	}
	if _, changed := snapshotter.Body(); changed == etag {
		t.Error("a republished document kept the old ETag")
	}
}
