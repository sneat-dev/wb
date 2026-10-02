package cockpit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/dashboard"
)

// cockpit#ac:daemon-log-needs-an-owner-session, over the wiring the
// daemon uses: the dashboard's log route asks Server.IsOwner. A marker planted
// in the log reaches the owner session and no other request, whatever it
// claims about itself.
func TestDaemonLogIsServedToTheOwnerSessionAndToNobodyElse(t *testing.T) {
	t.Parallel()
	const marker = "SENTINEL-DAEMON-LOG-5d1e"
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logPath, []byte("remote_unreachable: "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, nil)
	cookie := f.login()
	handler := dashboard.NewHandler(dashboard.Options{
		ProjectsRoot: t.TempDir(), Version: "test", LogPath: logPath,
		Mounts: f.server.Mounts(), Owner: f.server.IsOwner,
	})
	get := func(c call) *httptest.ResponseRecorder {
		t.Helper()
		if c.host == "" {
			c.host = testHost
		}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/log", nil)
		request.Host = c.host
		for i := 0; i < len(c.headers); i += 2 {
			request.Header.Add(c.headers[i], c.headers[i+1])
		}
		if c.cookie != nil {
			request.AddCookie(c.cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	for name, c := range map[string]call{
		"the owner session":                         {cookie: cookie},
		"the owner session from the Cockpit page":   {cookie: cookie, headers: []string{"Origin", testOrigin, "Sec-Fetch-Site", "same-origin"}},
		"the owner session through a proxy":         {cookie: cookie, headers: []string{"X-Forwarded-For", "203.0.113.7"}},
		"the owner session on a typed address":      {cookie: cookie, headers: []string{"Sec-Fetch-Site", "none"}},
		"the owner session on the IPv6 loopback":    {cookie: &http.Cookie{Name: cookie.Name, Value: cookie.Value}, host: "[::1]:8766"},
		"the owner session on the name localhost":   {cookie: &http.Cookie{Name: cookie.Name, Value: cookie.Value}, host: "localhost:8766"},
		"the owner session beside a planted cookie": {cookie: cookie, headers: []string{"Cookie", cookie.Name + "=planted"}},
	} {
		recorder := get(c)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), marker) || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s = %d %q, want the log, never stored", name, recorder.Code, recorder.Body.String())
		}
	}

	otherPort := &http.Cookie{Name: sessionCookiePrefix + "9999", Value: cookie.Value}
	for name, c := range map[string]call{
		"no session":                              {},
		"an unknown session":                      {cookie: &http.Cookie{Name: cookie.Name, Value: "not-a-session"}},
		"an empty session cookie":                 {cookie: &http.Cookie{Name: cookie.Name, Value: ""}},
		"a session cookie named for another port": {cookie: otherPort},
		"the session on a foreign host":           {cookie: cookie, host: "attacker.example:8766"},
		"the session on another loopback address": {cookie: cookie, host: "127.0.0.2:8766"},
		"the session with no host":                {cookie: cookie, host: "[bad"},
		"the session from the hosted origin":      {cookie: cookie, headers: []string{"Origin", hostedOrigin}},
		"the session from a foreign origin":       {cookie: cookie, headers: []string{"Origin", "https://attacker.example"}},
		"the session from the null origin":        {cookie: cookie, headers: []string{"Origin", "null"}},
		"the session with two origins":            {cookie: cookie, headers: []string{"Origin", testOrigin, "Origin", testOrigin}},
		"the session on a cross-site fetch":       {cookie: cookie, headers: []string{"Sec-Fetch-Site", "cross-site"}},
		"a proxied request with no session":       {headers: []string{"X-Forwarded-For", "127.0.0.1"}},
		"the daemon's owner token as a bearer":    {headers: []string{"Authorization", "Bearer owner-token"}},
	} {
		recorder := get(c)
		dump := recorder.Body.String()
		for header, values := range recorder.Header() {
			dump += header + strings.Join(values, "")
		}
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"error":"owner_session_required"`) {
			t.Errorf("%s = %d %q, want 401 owner_session_required", name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(dump, marker) || strings.Contains(dump, logPath) || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s was answered with the log or its path, or a storable response: %q %v", name, recorder.Body.String(), recorder.Header())
		}
	}

	// The session ends with the store's clock and with a logout, and the log
	// goes with it.
	f.now = f.now.Add(sessionLifetime + time.Second)
	if recorder := get(call{cookie: cookie}); recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), marker) {
		t.Errorf("an expired session = %d %q, want 401 and no log", recorder.Code, recorder.Body.String())
	}
	again := f.login()
	if recorder := get(call{cookie: again}); recorder.Code != http.StatusOK {
		t.Fatalf("a new session = %d, want the log", recorder.Code)
	}
	if recorder := f.logout(again, canonicalJSON...); recorder.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", recorder.Code)
	}
	if recorder := get(call{cookie: again}); recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), marker) {
		t.Errorf("a logged-out session = %d %q, want 401 and no log", recorder.Code, recorder.Body.String())
	}

	// With anonymous-local switched on, as it is here, an anonymous reader still
	// reads metadata: the gate took nothing else away.
	if principal := f.who(call{}); principal.Name != PrincipalAnonymousLocal {
		t.Errorf("an anonymous local reader is now %q", principal.Name)
	}
}
