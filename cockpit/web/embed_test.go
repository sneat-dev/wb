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

func get(t *testing.T, handler http.Handler, target string) (*http.Response, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	response := recorder.Result()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	return response, string(body)
}

func builtTree() fs.FS {
	return fstest.MapFS{
		"index.html":   {Data: []byte("<app-root></app-root>")},
		"main.js":      {Data: []byte("export {}")},
		"styles.css":   {Data: []byte(":root{}")},
		"data.json":    {Data: []byte("{}")},
		"logo.svg":     {Data: []byte("<svg/>")},
		"a.woff2":      {Data: []byte("f")},
		"a.woff":       {Data: []byte("f")},
		"a.png":        {Data: []byte("p")},
		"favicon.ico":  {Data: []byte("i")},
		"blob.bin":     {Data: []byte("b")},
		"sub/x.txt":    {Data: []byte("x")},
		".gitkeep":     {},
		"sub/.gitkeep": {},
	}
}

func TestEmbeddedPlaceholderIsNotBuiltAndServesTheOneLinePage(t *testing.T) {
	t.Parallel()
	if Built() {
		t.Skip("a real Cockpit build is embedded in this checkout")
	}
	response, body := get(t, Handler(), "/cockpit/")
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status = %d, type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "pnpm install && pnpm build") || !strings.Contains(body, "cockpit/web") || strings.Count(body, "\n") != 1 {
		t.Fatalf("body = %q, want a one-line page naming the build command", body)
	}
}

func TestUnbuiltTreeServesTheNotBuiltPageOnEveryPath(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/cockpit/", "/cockpit/fleet", "/cockpit"} {
		response, body := get(t, handlerFor(fstest.MapFS{".gitkeep": {}}), target)
		if response.StatusCode != http.StatusOK || body != notBuiltPage {
			t.Errorf("%s = %d %q", target, response.StatusCode, body)
		}
	}
}

func TestBuiltTreeServesFilesAndFallsBackToTheEntryDocument(t *testing.T) {
	t.Parallel()
	handler := handlerFor(builtTree())
	for target, want := range map[string]string{
		"/cockpit/":            "text/html; charset=utf-8",
		"/cockpit/fleet/alpha": "text/html; charset=utf-8",
		"/cockpit/sub":         "text/html; charset=utf-8",
		"/cockpit/main.js":     "text/javascript; charset=utf-8",
		"/cockpit/styles.css":  "text/css; charset=utf-8",
		"/cockpit/data.json":   "application/json",
		"/cockpit/logo.svg":    "image/svg+xml",
		"/cockpit/a.woff2":     "font/woff2",
		"/cockpit/a.woff":      "font/woff",
		"/cockpit/a.png":       "image/png",
		"/cockpit/favicon.ico": "image/vnd.microsoft.icon",
		"/cockpit/blob.bin":    "application/octet-stream",
	} {
		response, _ := get(t, handler, target)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != want {
			t.Errorf("%s = %d %q, want 200 %q", target, response.StatusCode, response.Header.Get("Content-Type"), want)
		}
	}
	if _, body := get(t, handler, "/cockpit/main.js"); body != "export {}" {
		t.Errorf("main.js body = %q", body)
	}
}

// statOnlyFS reports every entry as present but cannot open any, the one way
// a tree that looked built can fail when its entry document is read.
type statOnlyFS struct{}

func (statOnlyFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func (statOnlyFS) Stat(name string) (fs.FileInfo, error) {
	return fstest.MapFS{name: {Data: []byte("x")}}.Stat(name)
}

func TestBuiltTreeWhoseEntryDocumentCannotBeReadAnswersNotFound(t *testing.T) {
	t.Parallel()
	response, _ := get(t, handlerFor(statOnlyFS{}), "/cockpit/")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestMissingAssetIs404ButClientRoutesFallBack(t *testing.T) {
	t.Parallel()
	handler := handlerFor(builtTree())
	for target, want := range map[string]int{
		"/cockpit/missing.js":    404,
		"/cockpit/chunk-ABC.css": 404,
		"/cockpit/sub/none.png":  404,
		"/cockpit/.gitkeep":      404,
		"/cockpit/sub/.gitkeep":  404,
		"/cockpit/fleet/alpha":   200,
		"/cockpit/index.html":    200,
		"/cockpit/main.js":       200,
	} {
		if response, _ := get(t, handler, target); response.StatusCode != want {
			t.Errorf("%s = %d, want %d", target, response.StatusCode, want)
		}
	}
	if response, body := get(t, handler, "/cockpit/missing.js"); strings.Contains(body, "app-root") || strings.Contains(response.Header.Get("Content-Type"), "html") {
		t.Errorf("a missing asset answered HTML: %q", body)
	}
}

func TestOnlyGetAndHeadAreAnswered(t *testing.T) {
	t.Parallel()
	for _, handler := range []http.Handler{handlerFor(builtTree()), UnbuiltHandler()} {
		for method, want := range map[string]int{"GET": 200, "HEAD": 200, "POST": 405, "PUT": 405, "DELETE": 405} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(method, "/cockpit/", nil))
			if recorder.Code != want {
				t.Errorf("%s = %d, want %d", method, recorder.Code, want)
			}
			if want == 405 && recorder.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s Allow = %q", method, recorder.Header().Get("Allow"))
			}
		}
	}
}

func TestEntryFallbackAndNotBuiltPageAreNoCacheButAssetsAreNot(t *testing.T) {
	t.Parallel()
	built := handlerFor(builtTree())
	for target, want := range map[string]string{
		"/cockpit/":            "no-cache",
		"/cockpit/index.html":  "no-cache",
		"/cockpit/fleet/alpha": "no-cache",
		"/cockpit/main.js":     "",
	} {
		if response, _ := get(t, built, target); response.Header.Get("Cache-Control") != want {
			t.Errorf("%s Cache-Control = %q, want %q", target, response.Header.Get("Cache-Control"), want)
		}
	}
	if response, _ := get(t, UnbuiltHandler(), "/cockpit/"); response.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("not-built Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
}
