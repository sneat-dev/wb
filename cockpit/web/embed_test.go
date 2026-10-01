package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sneat-dev/wb/internal/dashboard"
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
		"index.html":   {Data: []byte(`<app-root ngCspNonce="__CSP_NONCE__"></app-root>`)},
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

func TestEmbeddedDistThroughHandlerServesTheBuildOrTheOneLinePage(t *testing.T) {
	t.Parallel()
	response, body := get(t, Handler(), "/cockpit/")
	if builtIn(distFS) {
		// This branch runs only in a checkout holding a real build (after
		// `pnpm build` in cockpit/web): Handler serves it, with the
		// placeholder replaced by the nonce of this response. The other
		// branch below runs only in a clean clone, where dist is a
		// placeholder.
		nonce := nonceInPolicy.FindStringSubmatch(response.Header.Get("Content-Security-Policy"))
		if response.StatusCode != http.StatusOK || nonce == nil || strings.Contains(body, noncePlaceholder) || !strings.Contains(body, nonce[1]) {
			t.Fatalf("status = %d, policy = %q", response.StatusCode, response.Header.Get("Content-Security-Policy"))
		}
		return
	}
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
		response, body := get(t, HandlerFor(fstest.MapFS{".gitkeep": {}}), target)
		if response.StatusCode != http.StatusOK || body != notBuiltPage {
			t.Errorf("%s = %d %q", target, response.StatusCode, body)
		}
	}
}

func TestBuiltTreeServesFilesAndFallsBackToTheEntryDocument(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(builtTree())
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
	response, _ := get(t, HandlerFor(statOnlyFS{}), "/cockpit/")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestMissingAssetIs404ButClientRoutesFallBack(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(builtTree())
	for target, want := range map[string]int{
		"/cockpit/missing.js":    404,
		"/cockpit/chunk-ABC.css": 404,
		"/cockpit/sub/none.png":  404,
		"/cockpit/.gitkeep":      404,
		"/cockpit/sub/.gitkeep":  404,
		"/cockpit/fleet/alpha":   200,
		"/cockpit/repo/v1.2":     200,
		"/cockpit/a.b/c":         200,
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
	for _, handler := range []http.Handler{HandlerFor(builtTree()), HandlerFor(fstest.MapFS{})} {
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
	built := HandlerFor(builtTree())
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
	if response, _ := get(t, HandlerFor(fstest.MapFS{}), "/cockpit/"); response.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("not-built Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
}

var nonceInPolicy = regexp.MustCompile(`style-src 'self' 'nonce-([A-Za-z0-9+/=_-]{16,})'`)

func TestEveryResponseCarriesTheStrictPolicy(t *testing.T) {
	t.Parallel()
	for name, handler := range map[string]http.Handler{"built": HandlerFor(builtTree()), "not built": HandlerFor(fstest.MapFS{})} {
		for _, target := range []string{"/cockpit/", "/cockpit/fleet", "/cockpit/main.js", "/cockpit/missing.js"} {
			response, _ := get(t, handler, target)
			header := response.Header.Get("Content-Security-Policy")
			if !strings.Contains(header, "script-src 'self';") || !strings.Contains(header, "frame-ancestors 'self'") || !nonceInPolicy.MatchString(header) {
				t.Errorf("%s %s policy = %q", name, target, header)
			}
			if strings.Contains(header, "unsafe-") {
				t.Errorf("%s %s policy allows unsafe sources: %q", name, target, header)
			}
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/cockpit/", nil))
		if recorder.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: a refused method carries no policy", name)
		}
	}
}

func TestEntryDocumentNonceIsFreshPerResponseAndMatchesThePolicy(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(builtTree())
	seen := map[string]bool{}
	for range 5 {
		response, body := get(t, handler, "/cockpit/fleet/alpha")
		match := nonceInPolicy.FindStringSubmatch(response.Header.Get("Content-Security-Policy"))
		if match == nil {
			t.Fatalf("no nonce in policy %q", response.Header.Get("Content-Security-Policy"))
		}
		nonce := match[1]
		if want := `<app-root ngCspNonce="` + nonce + `"></app-root>`; body != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
		if strings.Contains(body, noncePlaceholder) || seen[nonce] {
			t.Fatalf("nonce %q is reused or the placeholder survived", nonce)
		}
		seen[nonce] = true
	}
}

func TestPolicyIsWhatTheBrowserReceivesThroughTheDashboardMount(t *testing.T) {
	t.Parallel()
	handler := dashboard.NewHandler(dashboard.Options{Mounts: map[string]http.Handler{MountPath: HandlerFor(builtTree())}})
	response, _ := get(t, handler, "/cockpit/")
	values := response.Header.Values("Content-Security-Policy")
	if len(values) != 1 || strings.Contains(values[0], "unsafe-") || !nonceInPolicy.MatchString(values[0]) {
		t.Fatalf("Content-Security-Policy = %q, want exactly the strict policy", values)
	}
}

// The end-to-end test serves the build with tools/lib/serve-dist.mjs, which must
// send the policy the daemon sends or the test proves nothing about it.
func TestEndToEndServerSendsTheDaemonsPolicy(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("tools/lib/serve-dist.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if want := PolicyFor("${nonce}"); !strings.Contains(string(source), want) {
		t.Fatalf("tools/lib/serve-dist.mjs does not contain the policy %q", want)
	}
}

func TestFreshPolicyIsThePageShapeWithANewNonceEachTime(t *testing.T) {
	t.Parallel()
	first, second := FreshPolicy(), FreshPolicy()
	if first == second || strings.Contains(first, "unsafe-") || !strings.Contains(first, "script-src 'self'; style-src 'self' 'nonce-") {
		t.Errorf("FreshPolicy = %q then %q", first, second)
	}
}
