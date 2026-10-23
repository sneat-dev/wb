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

// readmeFixture is a Cockpit server over a snapshotter that has read one
// repository, github.com/acme/widgets, through the production collectors over a
// scripted Git. The repository's README is whatever setup put in the model.
type readmeFixture struct {
	t      *testing.T
	server *cockpitServer
	id     string
	cookie *http.Cookie
}

// newReadmeFixture makes the repository, runs setup on its model (the branch is
// main and its README is absent unless setup says otherwise), and takes one
// snapshot.
func newReadmeFixture(t *testing.T, setup func(repo *fakeRepo)) *readmeFixture {
	t.Helper()
	root := realTempDir(t)
	clone := filepath.Join(root, "github.com", "acme", "widgets")
	if err := os.MkdirAll(filepath.Join(clone, ".git", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := newFakeGit(t)
	git.add(".", newFakeRepo(0, ""))
	repo := git.add(clone, newFakeRepo(1, ""))
	repo.readme["main"] = fakeReadme{Absent: true}
	setup(repo)
	collectors := LocalCollectors{ProjectsRoot: root, Home: t.TempDir(), Runner: git}
	snapshotter, _ := newSnapshotter(collectors.Collectors(nil), nil)
	refreshAndSettle(t, snapshotter)
	f := &readmeFixture{t: t, server: newCockpitServer(t, snapshotter)}
	for _, repository := range snapshotter.Document().Repositories {
		f.id = repository.ID
	}
	f.cookie = f.server.login()
	return f
}

func (f *readmeFixture) readme(cookie *http.Cookie, headers ...string) (int, string, http.Header) {
	f.t.Helper()
	recorder := f.server.get(ReadmePath+"?repository="+url.QueryEscape(f.id), cookie, headers...)
	return recorder.Code, recorder.Body.String(), recorder.Header()
}

// TestOwnerReadsTheCommittedReadmeAsInertMarkdown proves the owner route serves
// the committed bytes with a markdown type, nosniff and a locked-down policy
// (cockpit#req:repository-readme).
func TestOwnerReadsTheCommittedReadmeAsInertMarkdown(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(repo *fakeRepo) {
		repo.readme["main"] = fakeReadme{Content: "# Widgets\n\n<script>alert(1)</script>\n"}
	})
	status, body, header := f.readme(f.cookie)
	if status != http.StatusOK || body != "# Widgets\n\n<script>alert(1)</script>\n" {
		t.Fatalf("README = %d %q", status, body)
	}
	if header.Get("Content-Type") != "text/markdown; charset=utf-8" || header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") || header.Get("Cache-Control") != "no-store" {
		t.Errorf("README headers = %v", header)
	}
}

// TestReadmeIsRefusedToTheSessionCookieWithoutItsKey: the README is content,
// and the cookie another server on the loopback host could replay does not read
// it (cockpit#ac:replayed-cookie-is-not-the-owner). The fixture forgets the
// session's key, so its requests carry the cookie alone, as that server's would.
func TestReadmeIsRefusedToTheSessionCookieWithoutItsKey(t *testing.T) {
	t.Parallel()
	const content = "# SENTINEL-README-CONTENT\n"
	f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: content} })
	key, _ := f.server.keys.LoadAndDelete(f.cookie.Value)
	for name, headers := range map[string][]string{
		"the cookie alone":             nil,
		"the cookie and a wrong key":   {cockpit.SessionKeyHeader, "not-the-key"},
		"the cookie, claiming a fetch": {"Origin", "http://" + testHost, "Sec-Fetch-Site", "same-origin"},
	} {
		if status, body, _ := f.readme(f.cookie, headers...); status != http.StatusUnauthorized || strings.Contains(body, "SENTINEL") {
			t.Errorf("%s: README = %d %q, want 401 and no content", name, status, body)
		}
	}
	// The same cookie with its key reads it.
	if status, body, _ := f.readme(f.cookie, cockpit.SessionKeyHeader, key.(string)); status != http.StatusOK || body != content {
		t.Errorf("the cookie and its key: README = %d %q, want the content", status, body)
	}
}

// TestReadmeCommittedAsASymbolicLinkOrNotAFileIsNotServed covers the entries
// that are not a regular file (a symbolic link, a directory and a submodule);
// the executable bit is fine.
func TestReadmeCommittedAsASymbolicLinkOrNotAFileIsNotServed(t *testing.T) {
	t.Parallel()
	for name, entry := range map[string]fakeReadme{
		"a symbolic link": {Mode: "120000", Kind: "blob", Content: "outside secret"},
		"a directory":     {Mode: "040000", Kind: "tree"},
		"a submodule":     {Mode: "160000", Kind: "commit"},
	} {
		f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = entry })
		status, body, _ := f.readme(f.cookie)
		if status != http.StatusForbidden || strings.Contains(body, "secret") || !strings.Contains(body, `"readme_not_a_regular_file"`) {
			t.Errorf("%s: README = %d %q, want 403 and no content", name, status, body)
		}
	}
	f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: "# run me\n", Mode: "100755"} })
	if status, body, _ := f.readme(f.cookie); status != http.StatusOK || body != "# run me\n" {
		t.Errorf("an executable README = %d %q, want it served", status, body)
	}
}

// TestReadmeAbsentOrUnknownIs404 covers a repository with no README, a default
// branch that cannot be told (a detached HEAD and no origin), an unknown id and
// no id; the bodies are short codes.
func TestReadmeAbsentOrUnknownIs404(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(*fakeRepo) {})
	if status, body, _ := f.readme(f.cookie); status != http.StatusNotFound || body != `{"error":"readme_not_found"}`+"\n" {
		t.Errorf("absent README = %d %q", status, body)
	}
	for _, target := range []string{ReadmePath + "?repository=repo-unknown", ReadmePath, ReadmePath + "?repository=" + url.QueryEscape("/a/path")} {
		if recorder := f.server.get(target, f.cookie); recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"unknown_repository"`) {
			t.Errorf("%s = %d %s, want 404 unknown_repository", target, recorder.Code, recorder.Body.String())
		}
	}
	detached := newReadmeFixture(t, func(repo *fakeRepo) { repo.branch = "" })
	if status, body, _ := detached.readme(detached.cookie); status != http.StatusNotFound || !strings.Contains(body, `"default_branch_unknown"`) {
		t.Errorf("README with no default branch = %d %q", status, body)
	}
}

// TestReadmeAtTheCapIsServedAndOverItIs413 states the cap: MaxReadmeBytes is
// one mebibyte; it is measured before it is read.
func TestReadmeAtTheCapIsServedAndOverItIs413(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: strings.Repeat("a", MaxReadmeBytes)} })
	if status, body, _ := f.readme(f.cookie); status != http.StatusOK || len(body) != MaxReadmeBytes {
		t.Fatalf("README at the cap = %d with %d bytes", status, len(body))
	}
	big := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: strings.Repeat("a", MaxReadmeBytes+1)} })
	if status, body, _ := big.readme(big.cookie); status != http.StatusRequestEntityTooLarge || !strings.Contains(body, `"readme_too_large"`) {
		t.Errorf("README past the cap = %d %q, want 413", status, body)
	}
}

// TestReadmeNeedsAnOwnerSessionFromTheLoopbackOrigin requires 401 without a
// session and 403 for the hosted origin even holding the owner cookie.
func TestReadmeNeedsAnOwnerSessionFromTheLoopbackOrigin(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: "secret readme"} })
	if status, body, _ := f.readme(nil); status != http.StatusUnauthorized || strings.Contains(body, "secret") {
		t.Errorf("README without a session = %d %q", status, body)
	}
	if status, body, _ := f.readme(f.cookie, "Origin", hostedOrigin); status != http.StatusForbidden || strings.Contains(body, "secret") {
		t.Errorf("README from the hosted origin with a cookie = %d %q", status, body)
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

// TestFleetRouteIsMetadataAndReadmeIsOwnerOnly pins the registrations: the
// hosted origin may read the fleet document but not the README.
func TestFleetRouteIsMetadataAndReadmeIsOwnerOnly(t *testing.T) {
	t.Parallel()
	f := newReadmeFixture(t, func(repo *fakeRepo) { repo.readme["main"] = fakeReadme{Content: "x"} })
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
	sources := oneRepoSources(t.TempDir())
	snapshotter, clock := newSnapshotter(sources.collectors(), nil)
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
	// A later look that finds the fleet as it was keeps the document and its
	// ETag; one that finds a change publishes a new document with a new one.
	clock.advance(time.Second)
	if err := snapshotter.RefreshRepository(t.Context(), localRepositoryID(testMachine, oneRepoSources("").repos[0])); err != nil {
		t.Fatal(err)
	}
	if _, same := snapshotter.Body(); same != etag {
		t.Error("a look that found nothing changed gave the document a new ETag")
	}
	sources.change(func(f *fakeSources) { f.branch = "trunk" })
	if err := snapshotter.RefreshRepository(t.Context(), localRepositoryID(testMachine, oneRepoSources("").repos[0])); err != nil {
		t.Fatal(err)
	}
	if _, changed := snapshotter.Body(); changed == etag {
		t.Error("a republished document kept the old ETag")
	}
}
