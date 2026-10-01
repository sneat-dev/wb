package cockpit

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sneat-dev/wb/internal/wbconfig"
)

const (
	testHost      = "127.0.0.1:8766"
	testOrigin    = "http://127.0.0.1:8766"
	hostedOrigin  = "https://hosted.example.test"
	fleetPath     = APIPrefix + "fleet"
	sessionPath   = APIPrefix + sessionRoute
	fleetResponse = `{"fleet":"fleet data"}`
)

// fixture is one daemon run: a Cockpit server over a clock the test moves,
// with a stand-in fleet metadata route registered the way a later task
// registers the real one.
type fixture struct {
	t      *testing.T
	server *Server
	page   http.Handler
	api    http.Handler
	now    time.Time
}

func newFixture(t *testing.T, change func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, now: time.Unix(1_700_000_000, 0).UTC()}
	options := Options{
		CanonicalHost: "127.0.0.1",
		Config:        wbconfig.CockpitConfig{HostedURL: hostedOrigin + "/wb/cockpit/", AnonymousMetadata: true},
		Now:           func() time.Time { return f.now },
	}
	if change != nil {
		change(&options)
	}
	f.server = newServer(options, served)
	f.server.HandleMetadata("fleet", CapabilityFleetRead, func(writer http.ResponseWriter, _ *http.Request, _ Principal) {
		_, _ = writer.Write([]byte(fleetResponse))
	})
	return f
}

// call is one request: its headers are name, value pairs, and a repeated
// name adds a second header line.
type call struct {
	method  string
	host    string
	target  string
	headers []string
	cookie  *http.Cookie
}

func (f *fixture) do(c call) *httptest.ResponseRecorder {
	f.t.Helper()
	if c.method == "" {
		c.method = http.MethodGet
	}
	if c.host == "" {
		c.host = testHost
	}
	request := httptest.NewRequest(c.method, c.target, nil)
	request.Host = c.host
	for i := 0; i < len(c.headers); i += 2 {
		request.Header.Add(c.headers[i], c.headers[i+1])
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	// The mounts are taken at the first request, as the daemon takes them
	// once every route is registered.
	if f.page == nil {
		mounts := f.server.Mounts()
		f.page, f.api = mounts[PagePrefix], mounts[APIPrefix]
	}
	handler := f.page
	if strings.HasPrefix(c.target, APIPrefix) {
		handler = f.api
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// mint asks the owner channel for a login code.
func (f *fixture) mint() string {
	f.t.Helper()
	recorder := httptest.NewRecorder()
	f.server.LoginCodeHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, LoginCodeRPCPath, nil))
	var response loginCodeResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		f.t.Fatal(err)
	}
	if recorder.Code != 200 || response.Code == "" || response.Path != LoginPath || !response.ExpiresAt.Equal(f.now.Add(60*time.Second)) {
		f.t.Fatalf("login code = %d %+v", recorder.Code, response)
	}
	return response.Code
}

func (f *fixture) exchange(code string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(call{target: LoginPath + "?code=" + code})
}

// login mints a code, exchanges it and returns the session cookie.
func (f *fixture) login() *http.Cookie {
	f.t.Helper()
	recorder := f.exchange(f.mint())
	cookies := recorder.Result().Cookies()
	if recorder.Code != http.StatusSeeOther || len(cookies) != 1 {
		f.t.Fatalf("login = %d with %d cookies", recorder.Code, len(cookies))
	}
	return cookies[0]
}

// who reads the session route and returns the principal it reports.
func (f *fixture) who(c call) Principal {
	f.t.Helper()
	c.target = sessionPath
	recorder := f.do(c)
	var principal Principal
	if err := json.Unmarshal(recorder.Body.Bytes(), &principal); err != nil || recorder.Code != 200 {
		f.t.Fatalf("session = %d %q (%v)", recorder.Code, recorder.Body.String(), err)
	}
	return principal
}

func (f *fixture) logout(cookie *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(call{method: http.MethodPost, target: LogoutPath, headers: headers, cookie: cookie})
}

var canonicalJSON = []string{"Origin", testOrigin, "Content-Type", "application/json"}

// cockpit#ac:login-code-is-single-use
func TestLoginCodeIsRefusedOnceUsedOrExpiredAndSetsNoCookie(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)

	used := f.mint()
	first := f.exchange(used)
	cookies := first.Result().Cookies()
	if first.Code != http.StatusSeeOther || first.Header().Get("Location") != "/cockpit/" || len(cookies) != 1 {
		t.Fatalf("first exchange = %d to %q with %d cookies", first.Code, first.Header().Get("Location"), len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "wb_cockpit_session_8766" || cookie.Value == "" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Path != "/" || cookie.MaxAge != 12*60*60 || !cookie.Expires.Equal(f.now.Add(12*time.Hour)) || cookie.Domain != "" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if strings.Contains(first.Header().Get("Location"), used) || first.Header().Get("Cache-Control") != "no-store" || first.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("login response headers = %v", first.Header())
	}

	expired := f.mint()
	f.now = f.now.Add(61 * time.Second)
	for name, code := range map[string]string{"used": used, "expired": expired, "unknown": "never-minted", "empty": ""} {
		recorder := f.exchange(code)
		if recorder.Code != http.StatusUnauthorized || len(recorder.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s code = %d with cookies %v, want 401 and none", name, recorder.Code, recorder.Header().Values("Set-Cookie"))
		}
	}

	// Presenting a code consumes it even when it had expired: it stays
	// refused if the clock is later found to be within its life.
	f.now = f.now.Add(-61 * time.Second)
	if recorder := f.exchange(expired); recorder.Code != http.StatusUnauthorized || len(recorder.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("expired code presented again = %d", recorder.Code)
	}
}

func TestLoginCodeLivesForSixtySeconds(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	late, onTime := f.mint(), f.mint()
	f.now = f.now.Add(60*time.Second - time.Nanosecond)
	if recorder := f.exchange(onTime); recorder.Code != http.StatusSeeOther {
		t.Fatalf("code just inside its life = %d", recorder.Code)
	}
	f.now = f.now.Add(time.Nanosecond)
	if recorder := f.exchange(late); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("code at 60 seconds = %d", recorder.Code)
	}
}

func TestLoginCodesAreDistinctAndOnlyTheNewestSixteenArePending(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	stale := f.mint()
	f.now = f.now.Add(61 * time.Second)
	codes := make([]string, 0, maxPendingLoginCodes+1)
	for range maxPendingLoginCodes + 1 {
		codes = append(codes, f.mint())
	}
	if pending := len(f.server.codes.pending); pending != maxPendingLoginCodes {
		t.Fatalf("pending codes = %d: the expired one or the oldest was kept", pending)
	}
	seen := map[string]bool{stale: true}
	for i, code := range codes {
		if seen[code] || len(code) < 43 {
			t.Fatalf("code %d (%d characters) repeats or is short", i, len(code))
		}
		seen[code] = true
		want := http.StatusSeeOther
		if i == 0 {
			want = http.StatusUnauthorized
		}
		if recorder := f.exchange(code); recorder.Code != want {
			t.Errorf("code %d = %d, want %d", i, recorder.Code, want)
		}
	}
}

// cockpit#ac:session-ends-on-logout-and-restart
func TestSessionEndsOnLogoutAndOnRestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	loggedOut, kept := f.login(), f.login()
	if f.who(call{cookie: loggedOut}).Name != PrincipalOwner {
		t.Fatal("a fresh session is not the owner")
	}
	recorder := f.logout(loggedOut, canonicalJSON...)
	cleared := recorder.Result().Cookies()
	if recorder.Code != http.StatusNoContent || len(cleared) != 1 || cleared[0].Name != loggedOut.Name || cleared[0].MaxAge >= 0 || cleared[0].Value != "" {
		t.Fatalf("logout = %d %+v", recorder.Code, cleared)
	}
	if got := f.who(call{cookie: loggedOut}); got.Name != PrincipalAnonymousLocal || got.Has(CapabilityRepoContentRead) {
		t.Fatalf("after logout the old cookie is %+v", got)
	}
	if f.who(call{cookie: kept}).Name != PrincipalOwner {
		t.Fatal("logging one session out ended another")
	}

	// A restart is a new Server: sessions live only in the old one's memory.
	restarted := newFixture(t, nil)
	if got := restarted.who(call{cookie: kept}); got.Name != PrincipalAnonymousLocal || got.Has(CapabilityRepoContentRead) {
		t.Fatalf("after a restart the old cookie is %+v", got)
	}
}

func TestSessionExpiresAfterTwelveHoursWhateverTheBrowserSends(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	f.now = f.now.Add(12*time.Hour - time.Nanosecond)
	if f.who(call{cookie: cookie}).Name != PrincipalOwner {
		t.Fatal("the session ended early")
	}
	f.now = f.now.Add(time.Nanosecond)
	if f.who(call{cookie: cookie}).Name != PrincipalAnonymousLocal {
		t.Fatal("the session outlived twelve hours")
	}
	// The next login drops the expired session from memory.
	f.login()
	if sessions := len(f.server.sessions.(*memorySessions).expires); sessions != 1 {
		t.Fatalf("sessions held = %d, want only the live one", sessions)
	}
}

func TestSessionCookieIsNamedAfterThePortTheRequestArrivedOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	forwarded := f.do(call{host: "127.0.0.1:9000", target: LoginPath + "?code=" + f.mint()}).Result().Cookies()[0]
	bare := f.do(call{host: "127.0.0.1", target: LoginPath + "?code=" + f.mint()}).Result().Cookies()[0]
	if forwarded.Name != "wb_cockpit_session_9000" || bare.Name != "wb_cockpit_session_80" {
		t.Fatalf("cookie names = %q, %q", forwarded.Name, bare.Name)
	}
	if f.who(call{host: "127.0.0.1:9000", cookie: forwarded}).Name != PrincipalOwner {
		t.Fatal("the session does not work on the port it was set on")
	}
	// The same cookie on another port is another daemon's: it is not read.
	if f.who(call{cookie: forwarded}).Name != PrincipalAnonymousLocal {
		t.Fatal("a cookie named for port 9000 was honoured on port 8766")
	}
	if f.who(call{cookie: &http.Cookie{Name: "wb_cockpit_session_8766", Value: ""}}).Name != PrincipalAnonymousLocal {
		t.Fatal("an empty session cookie was honoured")
	}
}

// cockpit#ac:proxied-request-needs-a-session
func TestProxiedRequestOrDisabledAnonymousAccessNeedsASession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	refused := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "fleet data") || !strings.Contains(recorder.Body.String(), `"error"`) {
			t.Errorf("response = %d %q, want 401 and no fleet data", recorder.Code, recorder.Body.String())
		}
	}
	if recorder := f.do(call{target: fleetPath}); recorder.Code != 200 || recorder.Body.String() != fleetResponse {
		t.Fatalf("a direct loopback request = %d %q", recorder.Code, recorder.Body.String())
	}
	headers := map[string]string{
		"X-Forwarded-For": "203.0.113.9", "Forwarded": "for=203.0.113.9", "X-Forwarded-Host": "wb.example.test",
		"X-Forwarded-Proto": "https", "X-Real-IP": "203.0.113.9", "Via": "1.1 proxy", "CF-Connecting-IP": "203.0.113.9",
		"X-Real-Ip": "", "Via ": "",
	}
	for name, value := range headers {
		refused(t, f.do(call{target: fleetPath, headers: []string{strings.TrimSpace(name), value}}))
		refused(t, f.do(call{target: sessionPath, headers: []string{strings.TrimSpace(name), value}}))
	}
	// Go's server canonicalises the names of incoming headers, so a handler
	// never sees "x-forwarded-for" from the wire. The check does not rely on
	// that: a name placed in the header map in any case still counts.
	for _, name := range []string{"x-forwarded-for", "X-REAL-IP", "cf-connecting-ip", "forwarded", "VIA"} {
		request := httptest.NewRequest(http.MethodGet, fleetPath, nil)
		request.Host = testHost
		request.Header[name] = []string{"203.0.113.9"}
		recorder := httptest.NewRecorder()
		f.api.ServeHTTP(recorder, request)
		refused(t, recorder)
	}
	// An owner session is what a proxied request needs.
	cookie := f.login()
	if recorder := f.do(call{target: fleetPath, headers: []string{"X-Forwarded-For", "203.0.113.9"}, cookie: cookie}); recorder.Code != 200 || recorder.Body.String() != fleetResponse {
		t.Fatalf("a proxied request with a session = %d", recorder.Code)
	}

	off := newFixture(t, func(options *Options) { options.Config.AnonymousMetadata = false })
	refused(t, off.do(call{target: fleetPath}))
	refused(t, off.do(call{target: sessionPath}))
	if off.who(call{cookie: off.login()}).Name != PrincipalOwner {
		t.Fatal("a session does not work with anonymous_metadata off")
	}
}

// Reaching Cockpit through a proxy is not a supported path (the Feature's Not
// Doing): a request with a forwarding header needs a session, and nothing
// here gives a proxy a way to get one that a direct request lacks. What does
// happen is pinned: the login exchange is protected by its code alone, so a
// valid code presented through a proxy is still exchanged, and when the proxy
// says the browser used https the cookie is Secure.
func TestLoginPresentedThroughAProxySetsASecureCookieOnlyForHTTPS(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	for name, test := range map[string]struct {
		headers []string
		secure  bool
	}{
		"direct":                 {nil, false},
		"proxied over https":     {[]string{"X-Forwarded-For", "203.0.113.9", "X-Forwarded-Proto", "https"}, true},
		"proxied, HTTPS in caps": {[]string{"X-Forwarded-Proto", " HTTPS "}, true},
		"proxied over http":      {[]string{"X-Forwarded-For", "203.0.113.9", "X-Forwarded-Proto", "http"}, false},
		"proxied, no scheme":     {[]string{"X-Forwarded-For", "203.0.113.9"}, false},
	} {
		recorder := f.do(call{target: LoginPath + "?code=" + f.mint(), headers: test.headers})
		cookies := recorder.Result().Cookies()
		if recorder.Code != http.StatusSeeOther || len(cookies) != 1 || cookies[0].Secure != test.secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Errorf("%s: login = %d, cookies %+v, want Secure = %t", name, recorder.Code, cookies, test.secure)
			continue
		}
		if got := f.who(call{headers: test.headers, cookie: cookies[0]}); got.Name != PrincipalOwner {
			t.Errorf("%s: the session it set is %+v", name, got)
		}
		out := f.logout(cookies[0], append(append([]string{}, canonicalJSON...), test.headers...)...)
		if cleared := out.Result().Cookies(); out.Code != http.StatusNoContent || len(cleared) != 1 || cleared[0].Secure != test.secure {
			t.Errorf("%s: logout = %d %+v", name, out.Code, cleared)
		}
	}
	// A code that is not valid is refused through a proxy as it is directly.
	if recorder := f.do(call{target: LoginPath + "?code=guess", headers: []string{"X-Forwarded-Proto", "https"}}); recorder.Code != http.StatusUnauthorized || len(recorder.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("an invalid code through a proxy = %d", recorder.Code)
	}
}

// cockpit#ac:only-the-hosted-origin-may-read-cross-origin
func TestOnlyTheHostedOriginMayReadCrossOrigin(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()

	hosted := f.do(call{target: fleetPath, headers: []string{"Origin", hostedOrigin}, cookie: cookie})
	if hosted.Code != 200 || hosted.Body.String() != fleetResponse ||
		hosted.Header().Get("Access-Control-Allow-Origin") != hostedOrigin ||
		len(hosted.Header().Values("Access-Control-Allow-Credentials")) != 0 || hosted.Header().Get("Vary") != "Origin, Accept-Encoding" {
		t.Fatalf("hosted origin = %d %v", hosted.Code, hosted.Header())
	}
	for _, foreign := range []string{"https://other.example.test", "null"} {
		recorder := f.do(call{target: fleetPath, headers: []string{"Origin", foreign}, cookie: cookie})
		if recorder.Code != http.StatusForbidden || strings.Contains(recorder.Body.String(), "fleet data") ||
			len(recorder.Header().Values("Access-Control-Allow-Origin")) != 0 || recorder.Header().Get("Vary") != "Origin, Accept-Encoding" {
			t.Errorf("Origin %s = %d %q %v, want 403 with no allowance", foreign, recorder.Code, recorder.Body.String(), recorder.Header())
		}
	}
	for name, headers := range map[string][]string{
		"hosted origin":           {"Origin", hostedOrigin},
		"hosted origin with JSON": {"Origin", hostedOrigin, "Content-Type", "application/json"},
	} {
		recorder := f.logout(cookie, headers...)
		if recorder.Code != http.StatusForbidden || len(recorder.Header().Values("Set-Cookie")) != 0 || len(recorder.Header().Values("Access-Control-Allow-Origin")) != 0 {
			t.Errorf("logout from the %s = %d %v, want 403", name, recorder.Code, recorder.Header())
		}
	}
	if f.who(call{cookie: cookie}).Name != PrincipalOwner {
		t.Fatal("the refused logout ended the session")
	}
}

func TestHostedOriginNeverActsWithTheOwnerSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	if got := f.who(call{headers: []string{"Origin", hostedOrigin}, cookie: cookie}); got.Name != PrincipalAnonymousLocal || got.Has(CapabilityRepoContentRead) {
		t.Fatalf("the hosted origin with the owner cookie is %+v", got)
	}
	// Through a proxy, or with anonymous access off, it is refused even with
	// the cookie — and may read that refusal.
	proxied := f.do(call{target: sessionPath, headers: []string{"Origin", hostedOrigin, "Via", "1.1 proxy"}, cookie: cookie})
	if proxied.Code != http.StatusUnauthorized || proxied.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
		t.Fatalf("proxied hosted origin = %d %v", proxied.Code, proxied.Header())
	}
	off := newFixture(t, func(options *Options) { options.Config.AnonymousMetadata = false })
	if recorder := off.do(call{target: fleetPath, headers: []string{"Origin", hostedOrigin}, cookie: off.login()}); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("hosted origin with anonymous access off = %d", recorder.Code)
	}
}

func TestHostedOriginPreflightGetsThePrivateNetworkAllowance(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	preflight := func(origin, target, method string) *httptest.ResponseRecorder {
		return f.do(call{method: http.MethodOptions, target: target, headers: []string{
			"Origin", origin, "Access-Control-Request-Method", method, "Access-Control-Request-Private-Network", "true",
		}})
	}
	for _, target := range []string{fleetPath, sessionPath} {
		recorder := preflight(hostedOrigin, target, "GET")
		header := recorder.Header()
		if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 || header.Get("Access-Control-Allow-Origin") != hostedOrigin ||
			header.Get("Access-Control-Allow-Private-Network") != "true" || header.Get("Access-Control-Allow-Methods") != "GET" ||
			len(header.Values("Access-Control-Allow-Credentials")) != 0 || len(header.Values("Access-Control-Allow-Headers")) != 0 ||
			header.Get("Access-Control-Max-Age") != "600" || header.Get("Vary") != "Origin, Accept-Encoding, Access-Control-Request-Method, Access-Control-Request-Headers" {
			t.Errorf("preflight for %s = %d %v", target, recorder.Code, header)
		}
	}
	withHeaders := func(asked ...string) *httptest.ResponseRecorder {
		headers := []string{"Origin", hostedOrigin, "Access-Control-Request-Method", "GET"}
		for _, value := range asked {
			headers = append(headers, "Access-Control-Request-Headers", value)
		}
		return f.do(call{method: http.MethodOptions, target: fleetPath, headers: headers})
	}
	for name, test := range map[string]struct {
		asked []string
		want  string
	}{
		"content type and accept": {[]string{"Content-Type, Accept"}, "content-type, accept"},
		"two header lines":        {[]string{"accept-language", " Content-Language ,"}, "accept-language, content-language"},
		"an empty list":           {[]string{" , "}, ""},
	} {
		recorder := withHeaders(test.asked...)
		if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Headers") != test.want || recorder.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
			t.Errorf("%s = %d %v, want Allow-Headers %q", name, recorder.Code, recorder.Header(), test.want)
		}
	}
	for _, asked := range [][]string{{"Authorization"}, {"content-type, x-requested-with"}, {"accept", "cookie"}, {"*"}} {
		header := withHeaders(asked...).Header()
		if code := withHeaders(asked...).Code; code != http.StatusForbidden || len(header.Values("Access-Control-Allow-Origin")) != 0 ||
			len(header.Values("Access-Control-Allow-Private-Network")) != 0 || len(header.Values("Access-Control-Allow-Headers")) != 0 {
			t.Errorf("preflight asking for %v = %d %v, want 403 with no allowance", asked, code, header)
		}
	}
	for name, recorder := range map[string]*httptest.ResponseRecorder{
		"a POST preflight":             preflight(hostedOrigin, fleetPath, "POST"),
		"another origin":               preflight("https://other.example.test", fleetPath, "GET"),
		"the null origin":              preflight("null", fleetPath, "GET"),
		"a route that is not metadata": preflight(hostedOrigin, APIPrefix+"readme", "GET"),
	} {
		header := recorder.Header()
		if recorder.Code != http.StatusForbidden || len(header.Values("Access-Control-Allow-Origin")) != 0 || len(header.Values("Access-Control-Allow-Private-Network")) != 0 {
			t.Errorf("%s = %d %v, want 403 with no allowance", name, recorder.Code, header)
		}
	}
}

func TestForeignOriginIsRefusedOnEveryCockpitRoute(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	code := f.mint()
	origins := map[string][]string{
		"another site":             {"Origin", "https://other.example.test"},
		"null":                     {"Origin", "null"},
		"an empty Origin":          {"Origin", ""},
		"the hosted origin's path": {"Origin", hostedOrigin + "/"},
		"the hosted host on http":  {"Origin", "http://hosted.example.test"},
		"another loopback port":    {"Origin", "http://127.0.0.1:9999"},
		"the localhost alias":      {"Origin", "http://localhost:8766"},
		"two Origin headers":       {"Origin", testOrigin, "Origin", "https://other.example.test"},
		"hosted twice":             {"Origin", hostedOrigin, "Origin", hostedOrigin},
	}
	targets := []string{sessionPath, fleetPath, APIPrefix + "nowhere", PagePrefix, PagePrefix + "main.js", LoginPath + "?code=" + code}
	for name, headers := range origins {
		for _, target := range targets {
			if recorder := f.do(call{target: target, headers: headers, cookie: cookie}); recorder.Code != http.StatusForbidden || len(recorder.Header().Values("Set-Cookie")) != 0 || recorder.Header().Get("Vary") != "Origin, Accept-Encoding" {
				t.Errorf("%s on %s = %d %v, want 403 with Vary: Origin, Accept-Encoding", name, target, recorder.Code, recorder.Header())
			}
		}
		if recorder := f.logout(cookie, append([]string{"Content-Type", "application/json"}, headers...)...); recorder.Code != http.StatusForbidden {
			t.Errorf("logout from %s = %d, want 403", name, recorder.Code)
		}
	}
	// The hosted origin is allowed on the metadata routes and nowhere else.
	for _, target := range []string{APIPrefix + "nowhere", PagePrefix, LoginPath + "?code=" + code} {
		recorder := f.do(call{target: target, headers: []string{"Origin", hostedOrigin}, cookie: cookie})
		if recorder.Code != http.StatusForbidden || len(recorder.Header().Values("Access-Control-Allow-Origin")) != 0 {
			t.Errorf("hosted origin on %s = %d, want 403", target, recorder.Code)
		}
	}
	// None of those refusals consumed the code or ended the session.
	if recorder := f.exchange(code); recorder.Code != http.StatusSeeOther {
		t.Fatalf("the code was consumed by a refused request: %d", recorder.Code)
	}
	if f.who(call{cookie: cookie}).Name != PrincipalOwner {
		t.Fatal("a refused request ended the session")
	}
}

func TestCanonicalOriginAndOriginlessRequestsAreServed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	for name, c := range map[string]call{
		"no Origin":            {target: fleetPath},
		"the canonical origin": {target: fleetPath, headers: []string{"Origin", testOrigin}},
		"a forwarded port":     {host: "127.0.0.1:9000", target: fleetPath, headers: []string{"Origin", "http://127.0.0.1:9000"}},
		"no port":              {host: "127.0.0.1", target: fleetPath, headers: []string{"Origin", "http://127.0.0.1"}},
	} {
		recorder := f.do(c)
		if recorder.Code != 200 || len(recorder.Header().Values("Access-Control-Allow-Origin")) != 0 || recorder.Header().Get("Vary") != "Origin, Accept-Encoding" ||
			recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s = %d %v", name, recorder.Code, recorder.Header())
		}
	}
	if recorder := f.do(call{target: PagePrefix, headers: []string{"Origin", testOrigin}}); recorder.Body.String() != "served" || recorder.Header().Get("Vary") != "Origin, Accept-Encoding" {
		t.Errorf("page from the canonical origin = %d", recorder.Code)
	}
	if recorder := f.do(call{target: APIPrefix + "nowhere", headers: []string{"Origin", testOrigin}}); recorder.Code != http.StatusNotFound || recorder.Header().Get("Vary") != "Origin, Accept-Encoding" {
		t.Errorf("unknown route = %d %v", recorder.Code, recorder.Header())
	}
	for _, method := range []string{http.MethodPost, http.MethodOptions, http.MethodHead, http.MethodDelete} {
		if recorder := f.do(call{method: method, target: fleetPath, headers: []string{"Origin", testOrigin}}); recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET" {
			t.Errorf("%s on a metadata route = %d", method, recorder.Code)
		}
	}
	if recorder := f.do(call{method: http.MethodPost, target: fleetPath, headers: []string{"Origin", hostedOrigin}}); recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST from the hosted origin = %d", recorder.Code)
	}
}

func TestIPv6CanonicalOriginIsBracketed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(options *Options) { options.CanonicalHost = "::1" })
	cookie := f.do(call{host: "[::1]:8766", target: LoginPath + "?code=" + f.mint()}).Result().Cookies()[0]
	recorder := f.do(call{method: http.MethodPost, host: "[::1]:8766", target: LogoutPath, cookie: cookie, headers: []string{"Origin", "http://[::1]:8766", "Content-Type", "application/json"}})
	if cookie.Name != "wb_cockpit_session_8766" || recorder.Code != http.StatusNoContent {
		t.Fatalf("cookie %q, logout = %d", cookie.Name, recorder.Code)
	}
}

func TestHostedOriginIsDerivedFromTheURLNotItsSpelling(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"https://hosted.example.test/wb/cockpit/": "https://hosted.example.test",
		"https://hosted.example.test":             "https://hosted.example.test",
		"https://hosted.example.test/":            "https://hosted.example.test",
		" HTTPS://Hosted.Example.Test:443/x ":     "https://hosted.example.test",
		"https://hosted.example.test:8443/x":      "https://hosted.example.test:8443",
		"http://localhost:80/":                    "http://localhost",
		"http://localhost:4200/wb/cockpit/":       "http://localhost:4200",
		"http://[::1]:4200/":                      "http://[::1]:4200",
		"http://[::1]/":                           "http://[::1]",
		"":                                        "",
		"/only/a/path":                            "",
		"https://bad host/":                       "",
	} {
		if got := originOf(raw); got != want {
			t.Errorf("originOf(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, hostedURL := range []string{"https://hosted.example.test", "https://HOSTED.example.test:443/wb/cockpit"} {
		f := newFixture(t, func(options *Options) { options.Config.HostedURL = hostedURL })
		if recorder := f.do(call{target: fleetPath, headers: []string{"Origin", hostedOrigin}}); recorder.Code != 200 || recorder.Header().Get("Access-Control-Allow-Origin") != hostedOrigin {
			t.Errorf("hosted_url %q: %d %v", hostedURL, recorder.Code, recorder.Header())
		}
	}
	// With no usable hosted URL nothing is allowed, an empty Origin included.
	none := newFixture(t, func(options *Options) { options.Config.HostedURL = "" })
	for _, origin := range []string{"", hostedOrigin} {
		if recorder := none.do(call{target: fleetPath, headers: []string{"Origin", origin}}); recorder.Code != http.StatusForbidden {
			t.Errorf("no hosted URL, Origin %q = %d", origin, recorder.Code)
		}
	}
	// A hosted URL that is the daemon's own origin is the canonical origin.
	same := newFixture(t, func(options *Options) { options.Config.HostedURL = testOrigin + "/cockpit/" })
	if got := same.who(call{headers: []string{"Origin", testOrigin}, cookie: same.login()}); got.Name != PrincipalOwner {
		t.Errorf("the daemon's own origin as hosted_url is %+v", got)
	}
}

// cockpit#ac:session-reports-principal-and-capabilities
func TestSessionRouteReportsPrincipalAndCapabilities(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	metadata := []Capability{"fleet.read", "machine.read", "repo.read", "worktree.read", "branch.read", "pr.read", "agent.read"}
	if got, want := f.who(call{}), (Principal{Name: "anonymous-local", Capabilities: metadata}); !reflect.DeepEqual(got, want) {
		t.Errorf("without a session = %+v, want %+v", got, want)
	}
	want := Principal{Name: "owner", Capabilities: append(append([]Capability{}, metadata...), "repo.content.read")}
	if got := f.who(call{cookie: f.login()}); !reflect.DeepEqual(got, want) {
		t.Errorf("with an owner session = %+v, want %+v", got, want)
	}
	recorder := f.do(call{target: sessionPath})
	if !strings.Contains(recorder.Body.String(), `"principal":"anonymous-local"`) || !strings.Contains(recorder.Body.String(), `"capabilities":["fleet.read"`) ||
		!strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Errorf("session body = %q %v", recorder.Body.String(), recorder.Header())
	}
	// The code browser base travels with the session, as configured.
	if !strings.Contains(recorder.Body.String(), `"code_browser_url":""`) {
		t.Errorf("an unset base is reported as %q", recorder.Body.String())
	}
	configured := newFixture(t, func(options *Options) { options.Config.CodeBrowserURL = "https://code.example.test/" })
	if body := configured.do(call{target: sessionPath}).Body.String(); !strings.Contains(body, `"code_browser_url":"https://code.example.test/"`) {
		t.Errorf("session body = %q", body)
	}
	// One principal's capabilities are not another's to change.
	first := anonymousLocal()
	first.Capabilities[0] = "tampered"
	if !anonymousLocal().Has(CapabilityFleetRead) || !owner().Has(CapabilityFleetRead) {
		t.Error("a principal's capabilities alias the shared vocabulary")
	}
}

func TestOwnerRouteNeedsASessionAndNeverGetsTheCrossOriginAllowance(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	const content, land, page = APIPrefix + "readme", APIPrefix + "land", PagePrefix + "owner/export"
	effects := 0
	f.server.HandleOwner(http.MethodGet, content, CapabilityRepoContentRead, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("file bytes"))
	})
	f.server.HandleOwner(http.MethodGet, page, CapabilityRepoContentRead, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("file bytes"))
	})
	f.server.HandleOwner(http.MethodPost, land, "worktree.land", func(http.ResponseWriter, *http.Request) { effects++ })
	f.server.HandleOwner(http.MethodPost, APIPrefix+"touch", CapabilityRepoContentRead, func(http.ResponseWriter, *http.Request) { effects++ })
	cookie := f.login()
	for name, test := range map[string]struct {
		call   call
		status int
	}{
		"content, no session":              {call{target: content}, 401},
		"content, no session, proxied":     {call{target: content, headers: []string{"Via", "1.1 proxy"}}, 401},
		"content, owner":                   {call{target: content, cookie: cookie}, 200},
		"content, owner, canonical origin": {call{target: content, cookie: cookie, headers: []string{"Origin", testOrigin}}, 200},
		"content, owner, hosted origin":    {call{target: content, cookie: cookie, headers: []string{"Origin", hostedOrigin}}, 403},
		"content, hosted, no cookie":       {call{target: content, headers: []string{"Origin", hostedOrigin}}, 403},
		"content, hosted preflight": {call{method: http.MethodOptions, target: content, cookie: cookie, headers: []string{
			"Origin", hostedOrigin, "Access-Control-Request-Method", "GET", "Access-Control-Request-Private-Network", "true",
		}}, 403},
		"content, owner, another site":     {call{target: content, cookie: cookie, headers: []string{"Origin", "https://other.example.test"}}, 403},
		"content, owner, cross-site fetch": {call{target: content, cookie: cookie, headers: []string{"Sec-Fetch-Site", "cross-site"}}, 401},
		"content, POST":                    {call{method: http.MethodPost, target: content, cookie: cookie, headers: canonicalJSON}, 405},
		"page route, no session":           {call{target: page}, 401},
		"page route, owner":                {call{target: page, cookie: cookie}, 200},
		"page route, hosted origin":        {call{target: page, cookie: cookie, headers: []string{"Origin", hostedOrigin}}, 403},
		"action, owner lacking capability": {call{method: http.MethodPost, target: land, cookie: cookie, headers: canonicalJSON}, 403},
		"action, no session":               {call{method: http.MethodPost, target: land, headers: canonicalJSON}, 401},
		"action, owner, no Origin":         {call{method: http.MethodPost, target: APIPrefix + "touch", cookie: cookie, headers: []string{"Content-Type", "application/json"}}, 403},
		"action, owner, not JSON":          {call{method: http.MethodPost, target: APIPrefix + "touch", cookie: cookie, headers: []string{"Origin", testOrigin}}, 415},
		"action, owner, hosted origin":     {call{method: http.MethodPost, target: APIPrefix + "touch", cookie: cookie, headers: []string{"Origin", hostedOrigin, "Content-Type", "application/json"}}, 403},
		"action, GET":                      {call{target: APIPrefix + "touch", cookie: cookie}, 405},
	} {
		recorder := f.do(test.call)
		header := recorder.Header()
		if recorder.Code != test.status || (test.status == 200) != (recorder.Body.String() == "file bytes") || strings.Contains(recorder.Body.String(), "bytes") != (test.status == 200) {
			t.Errorf("%s = %d %q, want %d", name, recorder.Code, recorder.Body.String(), test.status)
		}
		if len(header.Values("Access-Control-Allow-Origin")) != 0 || len(header.Values("Access-Control-Allow-Private-Network")) != 0 ||
			len(header.Values("Access-Control-Allow-Methods")) != 0 || header.Get("Vary") != "Origin, Accept-Encoding" || header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v carry an allowance or lack Vary: Origin, Accept-Encoding", name, header)
		}
	}
	if effects != 0 {
		t.Fatalf("%d refused requests had an effect", effects)
	}
	if recorder := f.do(call{method: http.MethodPost, target: APIPrefix + "touch", cookie: cookie, headers: canonicalJSON}); recorder.Code != 200 || effects != 1 {
		t.Fatalf("the owner's action = %d with %d effects", recorder.Code, effects)
	}
}

func panics(run func()) (message string) {
	defer func() { message, _ = recover().(string) }()
	run()
	return ""
}

func TestMisregisteredRoutePanics(t *testing.T) {
	t.Parallel()
	nothing := func(http.ResponseWriter, *http.Request, Principal) {}
	f := newFixture(t, nil)
	for _, capability := range []Capability{CapabilityRepoContentRead, "worktree.land", ""} {
		if message := panics(func() { f.server.HandleMetadata("readme", capability, nothing) }); !strings.Contains(message, "not a metadata capability") {
			t.Errorf("metadata route requiring %q: panic %q", capability, message)
		}
	}
	// The refused registration left nothing behind.
	if recorder := f.do(call{target: APIPrefix + "readme"}); recorder.Code != http.StatusNotFound {
		t.Fatalf("a refused registration is served: %d", recorder.Code)
	}
	// f.do took the mounts: the registries are frozen from then on.
	if message := panics(func() { f.server.HandleMetadata("attention", CapabilityFleetRead, nothing) }); !strings.Contains(message, "after the mounts were taken") {
		t.Errorf("late metadata route: panic %q", message)
	}
	if message := panics(func() {
		f.server.HandleOwner(http.MethodGet, APIPrefix+"late", CapabilityRepoContentRead, func(http.ResponseWriter, *http.Request) {})
	}); !strings.Contains(message, "after the mounts were taken") {
		t.Errorf("late owner route: panic %q", message)
	}
	for _, target := range []string{APIPrefix + "attention", APIPrefix + "late"} {
		if recorder := f.do(call{target: target}); recorder.Code != http.StatusNotFound {
			t.Errorf("%s was registered after the freeze: %d", target, recorder.Code)
		}
	}
	early := newFixture(t, nil)
	_ = early.server.MountsWith(nil)
	if message := panics(func() { early.server.HandleMetadata("attention", CapabilityFleetRead, nothing) }); message == "" {
		t.Error("MountsWith did not freeze the registries")
	}
}

func TestBrowserMarkedCrossSiteRequestGetsNoSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	for name, test := range map[string]struct {
		headers []string
		want    string
	}{
		"absent":              {nil, PrincipalOwner},
		"same-origin":         {[]string{"Sec-Fetch-Site", "same-origin"}, PrincipalOwner},
		"none":                {[]string{"Sec-Fetch-Site", "none"}, PrincipalOwner},
		"cross-site":          {[]string{"Sec-Fetch-Site", "cross-site"}, PrincipalAnonymousLocal},
		"same-site":           {[]string{"Sec-Fetch-Site", "same-site"}, PrincipalAnonymousLocal},
		"empty":               {[]string{"Sec-Fetch-Site", ""}, PrincipalAnonymousLocal},
		"an unknown value":    {[]string{"Sec-Fetch-Site", "Same-Origin"}, PrincipalAnonymousLocal},
		"two differing lines": {[]string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Site", "cross-site"}, PrincipalAnonymousLocal},
	} {
		if got := f.who(call{headers: test.headers, cookie: cookie}); got.Name != test.want {
			t.Errorf("Sec-Fetch-Site %s: %s, want %s", name, got.Name, test.want)
		}
	}
	// A cross-site request cannot log the owner out either.
	if recorder := f.logout(cookie, append([]string{"Sec-Fetch-Site", "cross-site"}, canonicalJSON...)...); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("cross-site logout = %d", recorder.Code)
	}
	if f.who(call{cookie: cookie}).Name != PrincipalOwner {
		t.Fatal("the refused logout ended the session")
	}
}

func TestPlantedCookieWithTheSessionNameDoesNotHideTheRealOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	// A browser sends the cookie with the longer path first.
	both := []string{"Cookie", cookie.Name + "=planted; " + cookie.Name + "=; " + cookie.Name + "=" + cookie.Value}
	if got := f.who(call{headers: both}); got.Name != PrincipalOwner {
		t.Fatalf("with a planted duplicate the owner is %+v", got)
	}
	if got := f.who(call{headers: []string{"Cookie", cookie.Name + "=planted; " + cookie.Name + "=also-planted"}}); got.Name != PrincipalAnonymousLocal {
		t.Fatalf("two planted cookies are %+v", got)
	}
	if recorder := f.logout(nil, append(append([]string{}, canonicalJSON...), both...)...); recorder.Code != http.StatusNoContent {
		t.Fatalf("logout with a planted duplicate = %d", recorder.Code)
	}
	if got := f.who(call{headers: both}); got.Name != PrincipalAnonymousLocal {
		t.Fatalf("after logout the real cookie is still %+v", got)
	}
}

func TestOneLoginCodePresentedConcurrentlyStartsOneSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	code := f.mint()
	page := f.server.Mounts()[PagePrefix]
	const presenters = 64
	results := make(chan int, presenters)
	start := make(chan struct{})
	for range presenters {
		go func() {
			<-start
			request := httptest.NewRequest(http.MethodGet, LoginPath+"?code="+code, nil)
			request.Host = testHost
			recorder := httptest.NewRecorder()
			page.ServeHTTP(recorder, request)
			if (recorder.Code == http.StatusSeeOther) != (len(recorder.Header().Values("Set-Cookie")) == 1) {
				results <- -1
				return
			}
			results <- recorder.Code
		}()
	}
	close(start)
	counts := map[int]int{}
	for range presenters {
		counts[<-results]++
	}
	if counts[http.StatusSeeOther] != 1 || counts[http.StatusUnauthorized] != presenters-1 {
		t.Fatalf("results = %v, want one 303 and the rest 401", counts)
	}
	if sessions := len(f.server.sessions.(*memorySessions).expires); sessions != 1 {
		t.Fatalf("sessions started = %d", sessions)
	}
}

func TestSessionsAndCodesAreSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	now := f.now
	sessions := f.server.sessions
	// Each worker holds at most one pending code, so no more workers than the
	// pending bound: beyond it a mint evicts another worker's unexchanged code.
	const workers = maxPendingLoginCodes
	done := make(chan error, workers)
	for range workers {
		go func() {
			for range 50 {
				id, err := sessions.Create(now)
				if err != nil || !sessions.Valid(id, now) {
					done <- errors.New("a session just created is not valid")
					return
				}
				sessions.End(id)
				if sessions.Valid(id, now) {
					done <- errors.New("an ended session is still valid")
					return
				}
				issued, err := f.server.MintLoginCode()
				if err != nil || !f.server.codes.exchange(issued.Code, now) || f.server.codes.exchange(issued.Code, now) {
					done <- errors.New("a code was not exchangeable exactly once")
					return
				}
			}
			done <- nil
		}()
	}
	for range workers {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if held := len(sessions.(*memorySessions).expires); held != 0 {
		t.Fatalf("sessions left = %d", held)
	}
}

func TestLogoutRequiresPostCanonicalOriginJSONAndASession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	cookie := f.login()
	for name, test := range map[string]struct {
		method  string
		headers []string
		cookie  *http.Cookie
		status  int
	}{
		"GET":                   {http.MethodGet, canonicalJSON, cookie, http.StatusMethodNotAllowed},
		"no Origin":             {http.MethodPost, []string{"Content-Type", "application/json"}, cookie, http.StatusForbidden},
		"no content type":       {http.MethodPost, []string{"Origin", testOrigin}, cookie, http.StatusUnsupportedMediaType},
		"a form":                {http.MethodPost, []string{"Origin", testOrigin, "Content-Type", "application/x-www-form-urlencoded"}, cookie, http.StatusUnsupportedMediaType},
		"text that says json":   {http.MethodPost, []string{"Origin", testOrigin, "Content-Type", "text/plain; application/json"}, cookie, http.StatusUnsupportedMediaType},
		"no session":            {http.MethodPost, canonicalJSON, nil, http.StatusUnauthorized},
		"an unknown session id": {http.MethodPost, canonicalJSON, &http.Cookie{Name: cookie.Name, Value: "guess"}, http.StatusUnauthorized},
	} {
		recorder := f.do(call{method: test.method, target: LogoutPath, headers: test.headers, cookie: test.cookie})
		if recorder.Code != test.status || len(recorder.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s = %d %v, want %d and no cookie change", name, recorder.Code, recorder.Header(), test.status)
		}
	}
	if f.who(call{cookie: cookie}).Name != PrincipalOwner {
		t.Fatal("a refused logout ended the session")
	}
	if recorder := f.logout(cookie, "Origin", testOrigin, "Content-Type", "application/json; charset=utf-8"); recorder.Code != http.StatusNoContent {
		t.Fatalf("logout with a charset parameter = %d", recorder.Code)
	}
}

func TestLoginIsAGetAndLeavesTheCodeUnusedOtherwise(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	code := f.mint()
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		recorder := f.do(call{method: method, target: LoginPath + "?code=" + code, headers: []string{"Origin", testOrigin}})
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET" || len(recorder.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s login = %d", method, recorder.Code)
		}
	}
	if recorder := f.exchange(code); recorder.Code != http.StatusSeeOther {
		t.Fatalf("login after refused methods = %d", recorder.Code)
	}
}

// failingSessions is a SessionStore that cannot start a session.
type failingSessions struct{ SessionStore }

func (failingSessions) Create(time.Time) (string, error) { return "", errors.New("store is full") }

func TestLoginAndMintingReportAFailureWithoutEstablishingAnything(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(options *Options) { options.Sessions = failingSessions{} })
	if recorder := f.exchange(f.mint()); recorder.Code != http.StatusInternalServerError || len(recorder.Header().Values("Set-Cookie")) != 0 || !strings.Contains(recorder.Body.String(), "store is full") {
		t.Fatalf("login with a failing store = %d %q", recorder.Code, recorder.Body.String())
	}

	broken := newFixture(t, func(options *Options) { options.Random = iotest.ErrReader(errors.New("no entropy")) })
	recorder := httptest.NewRecorder()
	broken.server.LoginCodeHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, LoginCodeRPCPath, nil))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "no entropy") || strings.Contains(recorder.Body.String(), `"code"`) {
		t.Fatalf("minting without entropy = %d %q", recorder.Code, recorder.Body.String())
	}
	if _, err := broken.server.MintLoginCode(); err == nil || len(broken.server.codes.pending) != 0 {
		t.Fatalf("MintLoginCode = %v with %d pending", err, len(broken.server.codes.pending))
	}
	if _, err := newMemorySessions(iotest.ErrReader(errors.New("no entropy"))).Create(time.Now()); err == nil {
		t.Fatal("a session was created without entropy")
	}

	get := httptest.NewRecorder()
	f.server.LoginCodeHandler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, LoginCodeRPCPath, nil))
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != "POST" || len(f.server.codes.pending) != 0 {
		t.Fatalf("GET on the login-code route = %d with %d pending", get.Code, len(f.server.codes.pending))
	}
}

func TestDefaultClockAndRandomSourceIssueWorkingSecrets(t *testing.T) {
	t.Parallel()
	server := New(Options{CanonicalHost: "127.0.0.1", Config: wbconfig.DefaultCockpitConfig()})
	before := time.Now()
	first, err := server.MintLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.MintLoginCode()
	if err != nil || first.Code == second.Code || first.ExpiresAt.Before(before.Add(60*time.Second)) {
		t.Fatalf("codes = %+v, %+v (%v)", first, second, err)
	}
	request := httptest.NewRequest(http.MethodGet, LoginPath+"?code="+first.Code, nil)
	request.Host = testHost
	recorder := httptest.NewRecorder()
	server.Mounts()[PagePrefix].ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || len(recorder.Result().Cookies()) != 1 {
		t.Fatalf("login = %d", recorder.Code)
	}
	if id := recorder.Result().Cookies()[0].Value; len(id) < 43 || id == first.Code {
		t.Fatalf("session identifier has %d characters or repeats the code", len(id))
	}
}
