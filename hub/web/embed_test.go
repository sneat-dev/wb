package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, handler http.Handler, target string) *http.Response {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder.Result()
}

// bodyOf is get plus the two assertions every served-page test makes: the
// request succeeded and its body is readable.
func bodyOf(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	response := get(t, handler, target)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s = %s", target, response.Status)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	return string(body)
}

func builtTree() fs.FS {
	return fstest.MapFS{
		"dashboard/index.html":        {Data: []byte(`<main data-dashboard-scope="user"></main>`)},
		"dashboard/github/index.html": {Data: []byte("<main>github</main>")},
		"_astro/dashboard.js":         {Data: []byte("export {}")},
		"_astro/BaseLayout.css":       {Data: []byte(":root{}")},
		"_astro/inter.woff2":          {Data: []byte("font")},
	}
}

// TestHandlerServesTheDirectoryIndexUnderTheMountPath is the Astro contract:
// base /workbench, directory output, trailing slash always.
func TestHandlerServesTheDirectoryIndexUnderTheMountPath(t *testing.T) {
	handler := handlerFor(builtTree(), true)

	response := get(t, handler, MountPath+"dashboard/")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", response.Status)
	}
	if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	body := make([]byte, 1024)
	n, _ := response.Body.Read(body)
	if !strings.Contains(string(body[:n]), `data-dashboard-scope="user"`) {
		t.Fatalf("body = %q", body[:n])
	}

	// An asset the emitted HTML references by its absolute /workbench/ URL.
	if response := get(t, handler, MountPath+"_astro/dashboard.js"); response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset = %s %q", response.Status, response.Header.Get("Content-Type"))
	}
}

// TestHandlerServesTheHostedOriginAsSameOrigin is the self-hosting promise in
// spec/features/self-hosted-bench: the Astro build bakes the hosted origin
// into its HTML and its JS chunks, and the embedded dashboard must call its
// own hub at /v0/workbench/ on the listener that serves the page, with no
// configuration. The handler strips the origin literal, leaving root-absolute
// paths, and must leave every other host alone.
func TestHandlerServesTheHostedOriginAsSameOrigin(t *testing.T) {
	const endpoint = hostedDashboardOrigin + "/v0/workbench/dashboard"
	files := fstest.MapFS{
		"dashboard/index.html": {Data: []byte(`<section data-endpoint="` + endpoint + `"></section>`)},
		"_astro/dashboard.js": {Data: []byte(
			`const stats="` + hostedDashboardOrigin + `/v0/workbench/stats/repository/acme%2Fwidgets";` +
				`const marketing="https://sneat.work/bench/dashboard/";`)},
		// The rewrite is scoped to HTML and JavaScript: an asset of any other
		// type is served byte-for-byte, origin and all.
		"_astro/manifest.json": {Data: []byte(`{"endpoint":"` + endpoint + `"}`)},
	}
	handler := handlerFor(files, true)

	for target, want := range map[string]string{
		MountPath + "dashboard/":          `data-endpoint="/v0/workbench/dashboard"`,
		MountPath + "_astro/dashboard.js": `"/v0/workbench/stats/repository/acme%2Fwidgets"`,
	} {
		got := bodyOf(t, handler, target)
		for _, unwanted := range []string{"wb-github-app.sneat.dev", "https:///"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("%s body still contains the hosted origin artifact %q: %q", target, unwanted, got)
			}
		}
		if !strings.Contains(got, "/v0/workbench/") || !strings.Contains(got, want) {
			t.Fatalf("%s body = %q; want the root-absolute %q", target, got, want)
		}
	}

	// The canonical site URL names a different host and is not this hub's API.
	if got := bodyOf(t, handler, MountPath+"_astro/dashboard.js"); !strings.Contains(got, "https://sneat.work/bench/dashboard/") {
		t.Fatalf("the canonical site URL was rewritten: %q", got)
	}
	if got := bodyOf(t, handler, MountPath+"_astro/manifest.json"); !strings.Contains(got, endpoint) {
		t.Fatalf("a non-HTML/JS asset was rewritten: %q", got)
	}
}

// TestHandlerRedirectsToTheTrailingSlashForm covers the URL an operator
// actually types.
func TestHandlerRedirectsToTheTrailingSlashForm(t *testing.T) {
	handler := handlerFor(builtTree(), true)
	for target, want := range map[string]string{
		MountPath + "dashboard": MountPath + "dashboard/",
		"/workbench":            MountPath + "dashboard/",
		MountPath:               MountPath + "dashboard/",
	} {
		response := get(t, handler, target)
		if response.StatusCode != http.StatusFound || response.Header.Get("Location") != want {
			t.Fatalf("%s = %s %q, want a redirect to %q", target, response.Status, response.Header.Get("Location"), want)
		}
	}
}

func TestHandlerReportsAnUnknownPathAsNotFound(t *testing.T) {
	if response := get(t, handlerFor(builtTree(), true), MountPath+"nothing/here.html"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %s", response.Status)
	}
}

// TestHandlerExplainsAnUnbuiltDashboard is what a wb built from a clean clone
// serves; it must say how to fix it rather than 404.
func TestHandlerExplainsAnUnbuiltDashboard(t *testing.T) {
	handler := handlerFor(fstest.MapFS{".gitkeep": {}}, false)
	response := get(t, handler, MountPath+"dashboard/")
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status = %s, content type = %q", response.Status, response.Header.Get("Content-Type"))
	}
	body := make([]byte, 512)
	n, _ := response.Body.Read(body)
	for _, want := range []string{"not built", "pnpm build", "hub/web"} {
		if !strings.Contains(string(body[:n]), want) {
			t.Fatalf("body %q does not mention %q", body[:n], want)
		}
	}
}

// TestEmbeddedDistIsAlwaysPresent is the guard on the go:embed placeholder: a
// clone that lost dist/.gitkeep would not compile, and one that kept it must
// still expose a usable FS.
func TestEmbeddedDistIsAlwaysPresent(t *testing.T) {
	entries, err := fs.ReadDir(distFS, ".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("embedded dist = %v, %v", entries, err)
	}
	// Built() and the served tree must agree, whichever state this checkout
	// is in.
	response := get(t, Handler(), MountPath+"dashboard/")
	if Built() != (response.Header.Get("Content-Type") == "text/html; charset=utf-8") {
		t.Fatalf("Built() = %t but /workbench/dashboard/ returned %q", Built(), response.Header.Get("Content-Type"))
	}
}

func TestContentTypeCoversEveryExtensionTheBuildEmits(t *testing.T) {
	for name, want := range map[string]string{
		"a.html":  "text/html; charset=utf-8",
		"a.css":   "text/css; charset=utf-8",
		"a.js":    "text/javascript; charset=utf-8",
		"a.mjs":   "text/javascript; charset=utf-8",
		"a.json":  "application/json",
		"a.svg":   "image/svg+xml",
		"a.woff":  "font/woff",
		"a.woff2": "font/woff2",
		"a.png":   "image/png",
		"a.ico":   "image/vnd.microsoft.icon",
		"a.bin":   "application/octet-stream",
	} {
		if got := contentType(name); got != want {
			t.Fatalf("contentType(%q) = %q, want %q", name, got, want)
		}
	}
}
