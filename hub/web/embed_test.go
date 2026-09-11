package web

import (
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
// base /bench, directory output, trailing slash always.
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

	// An asset the emitted HTML references by its absolute /bench/ URL.
	if response := get(t, handler, MountPath+"_astro/dashboard.js"); response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset = %s %q", response.Status, response.Header.Get("Content-Type"))
	}
}

// TestHandlerRedirectsToTheTrailingSlashForm covers the URL an operator
// actually types.
func TestHandlerRedirectsToTheTrailingSlashForm(t *testing.T) {
	handler := handlerFor(builtTree(), true)
	for target, want := range map[string]string{
		MountPath + "dashboard": MountPath + "dashboard/",
		"/bench":                MountPath + "dashboard/",
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
		t.Fatalf("Built() = %t but /bench/dashboard/ returned %q", Built(), response.Header.Get("Content-Type"))
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
