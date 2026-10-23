package web

import (
	"bytes"
	"compress/gzip"
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

// Untrusted README content is rendered in this origin: no image may load from
// another origin or from a data: URL (cockpit#ac:hostile-readme-is-inert).
func TestPolicyAllowsImagesFromTheOwnOriginOnly(t *testing.T) {
	t.Parallel()
	policy := PolicyFor("n")
	if !strings.Contains(policy, "img-src 'self';") || strings.Contains(policy, "data:") || strings.Contains(policy, "https:") {
		t.Fatalf("policy = %q, want img-src 'self' alone", policy)
	}
}

func TestFreshPolicyIsThePageShapeWithANewNonceEachTime(t *testing.T) {
	t.Parallel()
	first, second := FreshPolicy(), FreshPolicy()
	if first == second || strings.Contains(first, "unsafe-") || !strings.Contains(first, "script-src 'self'; style-src 'self' 'nonce-") {
		t.Errorf("FreshPolicy = %q then %q", first, second)
	}
}

func gzipped(t *testing.T, text string) []byte {
	t.Helper()
	return gzipBytes([]byte(text))
}

func gunzipped(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

// compressedTree is a build whose assets were compressed at build time.
func compressedTree(t *testing.T) fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(`<app-root ngCspNonce="__CSP_NONCE__"></app-root>`)},
		"index.html.gz":           {Data: gzipped(t, "STALE BUILD-TIME INDEX")},
		"main-ABCDEF12.js":        {Data: []byte("export const main = 1")},
		"main-ABCDEF12.js.gz":     {Data: gzipped(t, "export const main = 1")},
		"styles.css":              {Data: []byte(":root{}")},
		"styles.css.gz":           {Data: gzipped(t, ":root{}")},
		"chunk-ZZZZZZZZ.js":       {Data: []byte("export const chunk = 1")},
		"media/logo-1A2B3C4D.svg": {Data: []byte("<svg/>")},
		"favicon.ico":             {Data: []byte("i")},
		"chunk-kBDg_m1u.js":       {Data: []byte("export const mixed = 1")},
		"theme-standard.css":      {Data: []byte("lowercase")},
	}
}

func TestHashedAssetIsServedFromItsBuildTimeFileAndIsImmutable(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(compressedTree(t))
	request := httptest.NewRequest(http.MethodGet, "/cockpit/main-ABCDEF12.js", nil)
	request.Header.Set("Accept-Encoding", "gzip, deflate")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	header := recorder.Header()
	if recorder.Code != 200 || header.Get("Content-Encoding") != "gzip" || header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		header.Get("Vary") != "Origin, Accept-Encoding" || header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("hashed asset = %d %v", recorder.Code, header)
	}
	if gunzipped(t, recorder.Body.Bytes()) != "export const main = 1" || !bytes.Equal(recorder.Body.Bytes(), gzipped(t, "export const main = 1")) {
		t.Error("the asset was not served from its build-time compressed file")
	}
	// Without the header, the identity file is served, still immutable.
	identity, body := get(t, handler, "/cockpit/main-ABCDEF12.js")
	if identity.Header.Get("Content-Encoding") != "" || body != "export const main = 1" || identity.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || identity.Header.Get("Vary") != "Origin, Accept-Encoding" {
		t.Errorf("identity = %v %q", identity.Header, body)
	}
}

func TestAssetsWithoutAHashOrACompressedFileAreHandledHonestly(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(compressedTree(t))
	for target, want := range map[string]struct {
		encoding, cache string
	}{
		"/cockpit/styles.css":              {"gzip", ""},                                // compressed, not hashed: revalidated
		"/cockpit/chunk-ZZZZZZZZ.js":       {"", "public, max-age=31536000, immutable"}, // hashed, no .gz: identity
		"/cockpit/media/logo-1A2B3C4D.svg": {"", "public, max-age=31536000, immutable"},
		"/cockpit/favicon.ico":             {"", ""},
		"/cockpit/chunk-kBDg_m1u.js":       {"", "public, max-age=31536000, immutable"}, // Angular's mixed-case hash with an underscore
		"/cockpit/theme-standard.css":      {"", ""},                                    // a lower-case word is not a hash
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Accept-Encoding", "gzip")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != 200 || recorder.Header().Get("Content-Encoding") != want.encoding || recorder.Header().Get("Cache-Control") != want.cache {
			t.Errorf("%s = %d %v", target, recorder.Code, recorder.Header())
		}
	}
	for _, target := range []string{"/cockpit/main-ABCDEF12.js.gz", "/cockpit/index.html.gz", "/cockpit/nothing.gz"} {
		if response, _ := get(t, handler, target); response.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d: a build-time file must not be served under its own name", target, response.StatusCode)
		}
	}
}

func TestEntryDocumentIsCompressedPerResponseWithAFreshNonce(t *testing.T) {
	t.Parallel()
	handler := HandlerFor(compressedTree(t))
	nonces := map[string]bool{}
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/cockpit/", nil)
		request.Header.Set("Accept-Encoding", "gzip")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		header := recorder.Header()
		if recorder.Code != 200 || header.Get("Content-Encoding") != "gzip" || header.Get("Cache-Control") != "no-cache" || header.Get("Vary") != "Origin, Accept-Encoding" {
			t.Fatalf("entry document = %d %v", recorder.Code, header)
		}
		nonce := nonceInPolicy.FindStringSubmatch(header.Get("Content-Security-Policy"))
		body := gunzipped(t, recorder.Body.Bytes())
		if nonce == nil || !strings.Contains(body, nonce[1]) || strings.Contains(body, "STALE") || strings.Contains(body, noncePlaceholder) {
			t.Fatalf("body %q does not carry the response's nonce", body)
		}
		nonces[nonce[1]] = true
	}
	if len(nonces) != 2 {
		t.Error("two responses shared a nonce")
	}
	// Refusing gzip, with a zero quality or an identity-only list, gets plain HTML.
	for _, accept := range []string{"", "identity", "gzip;q=0", "gzip; q=0", "br"} {
		request := httptest.NewRequest(http.MethodGet, "/cockpit/", nil)
		if accept != "" {
			request.Header.Set("Accept-Encoding", accept)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Header().Get("Content-Encoding") != "" || !strings.Contains(recorder.Body.String(), "<app-root") {
			t.Errorf("Accept-Encoding %q = %v %q", accept, recorder.Header(), recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/cockpit/", nil)
	request.Header.Set("Accept-Encoding", "*")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Error("a wildcard Accept-Encoding was not served gzip")
	}
}

func TestAcceptsGzipReadsTheHeaderLikeAClient(t *testing.T) {
	t.Parallel()
	for accept, want := range map[string]bool{
		"gzip": true, "GZIP": true, "gzip, deflate, br": true, "deflate, gzip;q=0.5": true, " gzip ; q=1 ": true, "*": true, "*;q=0.1": true,
		"identity": false, "br": false, "": false, "gzip;q=0": false, "gzip;q=0.0": false, "gzip;Q=0": false, "gzip; Q = 0": false, "gzip;level=1;q=0": false,
		"*;q=0": false, "gzip;q=bad": true, "gzip;level=1": true, "gzip;q=0, *": false, "*, gzip;q=0": false, "gzip;q=0.5, *;q=0": true, "gzip, *;q=0": true,
		"gzip;q=1, *;q=0": true, "*;q=0, gzip": true, "identity, *;q=0.5": true,
	} {
		if got := AcceptsGzip([]string{accept}); got != want {
			t.Errorf("Accept-Encoding %q = %v, want %v", accept, got, want)
		}
	}
	if !AcceptsGzip([]string{"br", "gzip"}) || AcceptsGzip(nil) {
		t.Error("separate header lines are not read as one list")
	}
}

// TestOnlyBuildHashesAreImmutable checks the rule against the file names of a
// real production build (cockpit/web/dist after `pnpm build`) and against names
// that merely look similar.
func TestOnlyBuildHashesAreImmutable(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"main-SS4IWIAX.js": true, "styles-J6F5IS5P.css": true, "chunk-BGJpRc85.js": true, "chunk-BzbuF09_.js": true, "chunk-kBDgmD1u.js": true,
		"chunk-Cxlh3kJt.js": true, "media/font-A1B2C3D4.woff2": true,
		"favicon.svg": false, "index.html": false, "prerendered-routes.json": false, "3rdpartylicenses.txt": false,
		"logo-20240101.svg": false, "app-12345678.js": false, "theme-standard.css": false, "icon-96x96.png": false, "x-ABCD.js": false, "plain.js": false,
	} {
		if got := isHashedAsset(name); got != want {
			t.Errorf("%s: immutable = %v, want %v", name, got, want)
		}
	}
}
