package cockpit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/cockpit/web"
	"github.com/sneat-dev/wb/internal/dashboard"
)

// cacheHit hides the one field of the overview that legitimately differs
// between two handlers sharing a projects root: whether the second read was
// served from the first one's cache.
var cacheHit = regexp.MustCompile(`"cache_hit":(true|false)`)

var served = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
	_, _ = writer.Write([]byte("served"))
})

func do(handler http.Handler, host, target string) *httptest.ResponseRecorder {
	return doMethod(handler, http.MethodGet, host, target)
}

func doMethod(handler http.Handler, method, host, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.Host = host
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestGuardDecidesByHostMethodAndPath(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		canonical string
		method    string
		host      string
		target    string
		status    int
		location  string
	}{
		{"canonical page", "127.0.0.1", "GET", "127.0.0.1:8766", "/cockpit/", 200, ""},
		{"canonical page without port", "127.0.0.1", "GET", "127.0.0.1", "/cockpit/", 200, ""},
		{"canonical page accepts POST", "127.0.0.1", "POST", "127.0.0.1:8766", "/cockpit/", 200, ""},
		{"canonical api", "127.0.0.1", "GET", "127.0.0.1:8766", "/api/v1/cockpit/fleet", 200, ""},
		{"any port is accepted", "127.0.0.1", "GET", "127.0.0.1:1", "/cockpit/", 200, ""},
		{"localhost page is redirected", "127.0.0.1", "GET", "localhost:8766", "/cockpit/", 307, "http://127.0.0.1:8766/cockpit/"},
		{"HEAD is redirected too", "127.0.0.1", "HEAD", "localhost:8766", "/cockpit/", 307, "http://127.0.0.1:8766/cockpit/"},
		{"localhost page keeps path and query", "127.0.0.1", "GET", "localhost:8766", "/cockpit/fleet?x=1", 307, "http://127.0.0.1:8766/cockpit/fleet?x=1"},
		{"host name is case-insensitive", "127.0.0.1", "GET", "LocalHost:9", "/cockpit/", 307, "http://127.0.0.1:9/cockpit/"},
		{"ipv6 alias is redirected", "127.0.0.1", "GET", "[::1]:8766", "/cockpit/", 307, "http://127.0.0.1:8766/cockpit/"},
		{"ipv6 alias without port", "127.0.0.1", "GET", "[::1]", "/cockpit/", 307, "http://127.0.0.1/cockpit/"},
		{"localhost without port", "127.0.0.1", "GET", "localhost", "/cockpit/", 307, "http://127.0.0.1/cockpit/"},
		{"POST on an alias is never replayed", "127.0.0.1", "POST", "localhost:8766", "/cockpit/", 421, ""},
		{"DELETE on an alias is never replayed", "127.0.0.1", "DELETE", "[::1]:8766", "/cockpit/x", 421, ""},
		{"localhost api is served, not redirected", "127.0.0.1", "GET", "localhost:8766", "/api/v1/cockpit/fleet", 200, ""},
		{"api root without slash is served on an alias", "127.0.0.1", "GET", "localhost:8766", "/api/v1/cockpit", 200, ""},
		{"POST api on an alias is served", "127.0.0.1", "POST", "localhost:8766", "/api/v1/cockpit/x", 200, ""},
		{"ipv6 api is served", "127.0.0.1", "GET", "[::1]:8766", "/api/v1/cockpit/fleet", 200, ""},
		{"ipv6 listener: canonical page", "::1", "GET", "[::1]:8766", "/cockpit/", 200, ""},
		{"ipv6 listener: 127.0.0.1 is the alias", "::1", "GET", "127.0.0.1:8766", "/cockpit/", 307, "http://[::1]:8766/cockpit/"},
		{"ipv6 listener: localhost is the alias", "::1", "GET", "localhost:8766", "/cockpit/a", 307, "http://[::1]:8766/cockpit/a"},
		{"ipv6 listener: alias without port", "::1", "GET", "localhost", "/cockpit/", 307, "http://[::1]/cockpit/"},
		{"ipv6 listener: 127.0.0.1 api is served", "::1", "GET", "127.0.0.1:8766", "/api/v1/cockpit/fleet", 200, ""},
		{"foreign host page", "127.0.0.1", "GET", "attacker.example:8766", "/cockpit/", 421, ""},
		{"foreign host api", "127.0.0.1", "GET", "attacker.example:8766", "/api/v1/cockpit/fleet", 421, ""},
		{"foreign host without port", "127.0.0.1", "GET", "attacker.example", "/cockpit/", 421, ""},
		{"other loopback address", "127.0.0.1", "GET", "127.0.0.2:8766", "/cockpit/", 421, ""},
		{"loopback name as a suffix", "127.0.0.1", "GET", "localhost.attacker.example:8766", "/cockpit/", 421, ""},
		{"loopback name as a prefix of a label", "127.0.0.1", "GET", "127.0.0.1.attacker.example", "/cockpit/", 421, ""},
		{"empty host", "127.0.0.1", "GET", "", "/cockpit/", 421, ""},
		{"unbracketed ipv6", "127.0.0.1", "GET", "::1", "/cockpit/", 421, ""},
		{"half-bracketed", "127.0.0.1", "GET", "[::1", "/cockpit/", 421, ""},
		{"extra colons", "127.0.0.1", "GET", "localhost:80:80", "/cockpit/", 421, ""},
		{"non-numeric port", "127.0.0.1", "GET", "127.0.0.1:http", "/cockpit/", 421, ""},
		{"non-numeric port on an alias api", "127.0.0.1", "GET", "localhost:abc", "/api/v1/cockpit/fleet", 421, ""},
		{"empty port", "127.0.0.1", "GET", "localhost:", "/cockpit/", 421, ""},
		{"port out of range", "127.0.0.1", "GET", "localhost:70000", "/cockpit/", 421, ""},
	} {
		recorder := doMethod(Guard(test.canonical, served), test.method, test.host, test.target)
		if recorder.Code != test.status || recorder.Header().Get("Location") != test.location {
			t.Errorf("%s: %s %q %s = %d %q, want %d %q", test.name, test.method, test.host, test.target, recorder.Code, recorder.Header().Get("Location"), test.status, test.location)
		}
		if (test.status == 200) != strings.Contains(recorder.Body.String(), "served") {
			t.Errorf("%s: handler reached = %t for status %d", test.name, test.status != 200, test.status)
		}
	}
}

func TestCanonicalHostFollowsTheListener(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"127.0.0.1:8766": "127.0.0.1",
		"localhost:8766": "127.0.0.1",
		"[::1]:8766":     "::1",
		"[::2]:8766":     "127.0.0.1",
		"0.0.0.0:8766":   "127.0.0.1",
		"not an address": "127.0.0.1",
		"":               "127.0.0.1",
	} {
		if got := CanonicalHost(address); got != want {
			t.Errorf("CanonicalHost(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestPagePrefixIsTheWebMountPath(t *testing.T) {
	t.Parallel()
	if PagePrefix != web.MountPath {
		t.Fatalf("PagePrefix = %q, web.MountPath = %q", PagePrefix, web.MountPath)
	}
}

func TestAPIHandlerAnswersAJSON404(t *testing.T) {
	t.Parallel()
	recorder := do(APIHandler(), "127.0.0.1:8766", "/api/v1/cockpit/fleet")
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 404 || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") || body["error"] == "" {
		t.Fatalf("status = %d, type = %q, body = %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}

func TestMountsServeBothSubtreesBehindTheGuard(t *testing.T) {
	t.Parallel()
	mounts := mountsFor("127.0.0.1", served)
	if len(mounts) != 2 {
		t.Fatalf("mounts = %v", mounts)
	}
	if recorder := do(mounts[PagePrefix], "127.0.0.1:1", "/cockpit/"); recorder.Body.String() != "served" {
		t.Errorf("page = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder := do(mounts[APIPrefix], "127.0.0.1:1", "/api/v1/cockpit/fleet"); recorder.Code != 404 {
		t.Errorf("api = %d", recorder.Code)
	}
	for prefix, handler := range mounts {
		if recorder := do(handler, "attacker.example:1", prefix); recorder.Code != 421 {
			t.Errorf("%s with a foreign host = %d, want 421", prefix, recorder.Code)
		}
	}
}

func TestUnbuiltCockpitPageIsOneLinePlainText(t *testing.T) {
	t.Parallel()
	unbuilt := web.UnbuiltHandler()
	recorder := do(mountsFor("127.0.0.1", unbuilt)[PagePrefix], "127.0.0.1:8766", "/cockpit/")
	body := recorder.Body.String()
	if recorder.Code != 200 || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/plain") ||
		strings.Count(body, "\n") != 1 || !strings.Contains(body, "pnpm install && pnpm build") {
		t.Fatalf("status = %d, type = %q, body = %q", recorder.Code, recorder.Header().Get("Content-Type"), body)
	}
}

func TestMountsServesTheEmbeddedApplicationBehindTheGuard(t *testing.T) {
	t.Parallel()
	mounts := Mounts("127.0.0.1")
	if recorder := do(mounts[PagePrefix], "127.0.0.1:1", "/cockpit/"); recorder.Code != 200 {
		t.Errorf("page = %d", recorder.Code)
	}
	if recorder := do(mounts[PagePrefix], "attacker.example", "/cockpit/"); recorder.Code != 421 {
		t.Errorf("foreign page = %d", recorder.Code)
	}
}

func TestMountsWithKeepsTheOthersAndDoesNotModifyThem(t *testing.T) {
	t.Parallel()
	hub := map[string]http.Handler{"/workbench/": served}
	merged := MountsWith(hub, "127.0.0.1")
	if len(hub) != 1 || len(merged) != 3 || merged["/workbench/"] == nil || merged[PagePrefix] == nil || merged[APIPrefix] == nil {
		t.Fatalf("hub = %v, merged = %v", hub, merged)
	}
	if len(MountsWith(nil, "::1")) != 2 {
		t.Fatal("Cockpit is not mounted without a hub")
	}
}

// TestExistingRoutesAnswerAsBeforeWithCockpitMounted compares every existing
// dashboard route, with and without Cockpit mounted, byte for byte including
// the complete header map (cockpit#ac:dashboard-command-is-unchanged), both
// with a hub's mounts and with none (the no-hub path is the one that changed).
// `wb dashboard --local` reads none of these mounts and is covered by
// cmd/wb's dashboard tests.
func TestExistingRoutesAnswerAsBeforeWithCockpitMounted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := func(mounts map[string]http.Handler) dashboard.Options {
		return dashboard.Options{
			ProjectsRoot: root, Version: "1.2.3", DaemonPID: 1, SchedulerGeneration: 2, Mounts: mounts,
			Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		}
	}
	hub := map[string]http.Handler{"/workbench/": served, "/v0/workbench/": served}
	for name, others := range map[string]map[string]http.Handler{"hub": hub, "no hub": nil} {
		before := dashboard.NewHandler(options(others))
		after := dashboard.NewHandler(options(MountsWith(others, "127.0.0.1")))
		for _, target := range []string{"/", "/metrics", "/coverage", "/api/v1/health", "/api/v1/overview", "/api/v1/log", "/api/v1/peers", "/workbench/", "/v0/workbench/x", "/nowhere"} {
			want, got := do(before, "127.0.0.1:8766", target), do(after, "127.0.0.1:8766", target)
			if want.Code != got.Code || cacheHit.ReplaceAllString(want.Body.String(), "") != cacheHit.ReplaceAllString(got.Body.String(), "") || !reflect.DeepEqual(want.Header(), got.Header()) {
				t.Errorf("%s: %s changed: %d %q %v -> %d %q %v", name, target, want.Code, want.Body.String(), want.Header(), got.Code, got.Body.String(), got.Header())
			}
		}
		// An existing route is not subject to the Cockpit guard.
		if recorder := do(after, "attacker.example:8766", "/api/v1/health"); recorder.Code != 200 {
			t.Errorf("%s: /api/v1/health with a foreign host = %d, want unchanged 200", name, recorder.Code)
		}
	}
}
