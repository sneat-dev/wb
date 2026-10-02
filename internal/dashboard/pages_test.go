package dashboard

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var (
	scriptTag    = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	inlineHandle = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// The dashboard's pages share an origin, and so a storage, with Cockpit, which
// keeps the owner's session key there (cockpit#req:session-key). So no page of
// the dashboard may run a script that is not a file of the origin: the policy
// refuses inline script, and the pages need none.
func TestDashboardPagesRunOnlyScriptFilesOfTheirOwnOrigin(t *testing.T) {
	t.Parallel()
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "test"})
	get := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		return recorder
	}

	for _, directive := range strings.Split(Policy, ";") {
		if fields := strings.Fields(directive); fields[0] == "script-src" || fields[0] == "default-src" {
			if len(fields) != 2 || fields[1] != "'self'" {
				t.Errorf("the policy's %s is %q, want 'self' and nothing else", fields[0], fields[1:])
			}
		}
	}
	for _, need := range []string{"object-src 'none'", "base-uri 'self'", "frame-ancestors 'self'"} {
		if !strings.Contains(Policy, need) {
			t.Errorf("the policy %q lacks %q", Policy, need)
		}
	}

	for page, script := range map[string]string{"/": AssetsPrefix + "index.js", "/metrics": AssetsPrefix + "metrics.js", "/anything-else": AssetsPrefix + "index.js"} {
		recorder := get(page)
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Security-Policy") != Policy {
			t.Errorf("%s = %d with the policy %q, want 200 and %q", page, recorder.Code, recorder.Header().Get("Content-Security-Policy"), Policy)
		}
		tags := scriptTag.FindAllStringSubmatch(body, -1)
		if len(tags) != 1 || strings.TrimSpace(tags[0][1]) != `src="`+script+`"` || tags[0][2] != "" {
			t.Errorf("%s has the scripts %q, want exactly one, the file %s, with no inline body", page, tags, script)
		}
		if found := inlineHandle.FindString(body); found != "" || strings.Contains(strings.ToLower(body), "javascript:") {
			t.Errorf("%s has an inline event handler or a javascript: address (%q)", page, found)
		}

		served := get(script)
		if served.Code != http.StatusOK || served.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
			served.Header().Get("Cache-Control") != "no-cache" || served.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s = %d %v, want a script that is revalidated and not sniffed", script, served.Code, served.Header())
		}
		// The scripts build the page from elements and text. None of the ways
		// to turn a string into markup or into code is used.
		for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment", "DOMParser", "eval(", "new Function", "setAttribute('on", `setAttribute("on`, "srcdoc"} {
			if strings.Contains(served.Body.String(), sink) {
				t.Errorf("%s uses %s", script, sink)
			}
		}
	}
	if get(AssetsPrefix+"index.js").Body.String() != indexScript || get(AssetsPrefix+"metrics.js").Body.String() != metricsScript {
		t.Error("a script route does not serve its embedded file")
	}
}
