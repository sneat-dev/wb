package cockpit

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sneat-dev/wb/internal/dashboard"
)

// keyFixture is a daemon with everything an owner alone may read: the
// machines' SSH routes on the session route, an owner content route, and the
// daemon log on the dashboard's route, which asks Server.IsOwner.
type keyFixture struct {
	*fixture
	log     http.Handler
	logPath string
}

const (
	routeSentinel   = "SENTINEL-ssh-host.example"
	contentSentinel = "SENTINEL-FILE-CONTENT-71c2"
	logSentinel     = "SENTINEL-DAEMON-LOG-93ab"
	contentPath     = APIPrefix + "content"
)

func newKeyFixture(t *testing.T) *keyFixture {
	t.Helper()
	f := newFixture(t, nil)
	f.server.SetMachineRoutes(func() []MachineRoute {
		return []MachineRoute{{MachineID: "mach-vm", SSH: SSHRoute{Host: routeSentinel, WBPath: "wb"}}}
	})
	f.server.HandleOwner(http.MethodGet, contentPath, CapabilityRepoContentRead, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(contentSentinel))
	})
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logPath, []byte(logSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &keyFixture{fixture: f, logPath: logPath, log: dashboard.NewHandler(dashboard.Options{
		Version: "test", LogPath: logPath,
		Mounts: f.server.Mounts(), Owner: f.server.IsOwner,
	})}
}

// readLog asks the dashboard's log route the way c describes.
func (f *keyFixture) readLog(c call) *httptest.ResponseRecorder {
	c.method = http.MethodGet
	if c.host == "" {
		c.host = testHost
	}
	recorder := httptest.NewRecorder()
	f.log.ServeHTTP(recorder, f.request(c, "/api/v1/log"))
	return recorder
}

// everything is what the answers to c hold, on every owner-only route and on
// the session route: bodies and headers, for a sentinel search.
func (f *keyFixture) everything(c call) (session, content, log *httptest.ResponseRecorder, dump string) {
	session, content = f.do(withTarget(c, sessionPath)), f.do(withTarget(c, contentPath))
	log = f.readLog(c)
	for _, recorder := range []*httptest.ResponseRecorder{session, content, log} {
		dump += recorder.Body.String() + fmt.Sprint(recorder.Header())
	}
	return session, content, log, dump
}

func withTarget(c call, target string) call {
	c.target = target
	return c
}

// cockpit#ac:replayed-cookie-is-not-the-owner. Another server on the loopback
// host is sent the session cookie by the browser, whatever its port, and can
// replay it from outside the browser with any header it likes and no Origin.
// It never holds the session key, so it is never the owner: on every
// owner-only route it is answered like a request with no session, and on the
// session route it is anonymous-local, not an error.
func TestAReplayedCookieWithoutTheSessionKeyIsNeverTheOwner(t *testing.T) {
	t.Parallel()
	f := newKeyFixture(t)
	cookie := f.login()
	key := f.keys[cookie.Value]
	other := f.login()
	otherKey := f.keys[other.Value]
	if key == "" || otherKey == "" || key == otherKey {
		t.Fatalf("two logins were given the keys %q and %q, want two distinct ones", key, otherKey)
	}

	for name, c := range map[string]call{
		"the cookie alone": {cookie: cookie, noKey: true},
		"the cookie alone, claiming the Cockpit page": {cookie: cookie, noKey: true, headers: []string{"Origin", testOrigin, "Sec-Fetch-Site", "same-origin"}},
		"the cookie and an empty key":                 {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, ""}},
		"the cookie and a wrong key":                  {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, "not-the-key"}},
		"the cookie and the key cut short":            {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, key[:len(key)-1]}},
		"the cookie and the key with a tail":          {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, key + "A"}},
		"the cookie and another session's key":        {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, otherKey}},
		"the cookie and the session identifier":       {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, cookie.Value}},
		"the cookie and the key sent twice":           {cookie: cookie, noKey: true, headers: []string{SessionKeyHeader, key, SessionKeyHeader, key}},
		"the cookie and the key as a bearer":          {cookie: cookie, noKey: true, headers: []string{"Authorization", "Bearer " + key}},
		"the cookie and the key in a second cookie":   {cookie: cookie, noKey: true, headers: []string{"Cookie", "wb_cockpit_session_key=" + key}},
		"the key alone":                               {headers: []string{SessionKeyHeader, key}},
		"the key and an unknown cookie":               {cookie: &http.Cookie{Name: cookie.Name, Value: "not-a-session"}, headers: []string{SessionKeyHeader, key}},
	} {
		session, content, log, dump := f.everything(c)
		if session.Code != http.StatusOK || !strings.Contains(session.Body.String(), `"principal":"anonymous-local"`) {
			t.Errorf("%s: the session route = %d %s, want anonymous-local and no error", name, session.Code, session.Body.String())
		}
		if content.Code != http.StatusUnauthorized {
			t.Errorf("%s: the owner content route = %d, want 401", name, content.Code)
		}
		if log.Code != http.StatusUnauthorized || !strings.Contains(log.Body.String(), `"error":"owner_session_required"`) {
			t.Errorf("%s: the daemon log = %d %s, want 401 owner_session_required", name, log.Code, log.Body.String())
		}
		for _, sentinel := range []string{routeSentinel, "machine_routes", contentSentinel, logSentinel, f.logPath, `"principal":"owner"`} {
			if strings.Contains(dump, sentinel) {
				t.Errorf("%s was answered with %q", name, sentinel)
			}
		}
		// It cannot end the owner's session either.
		c.method, c.target = http.MethodPost, LogoutPath
		c.headers = append(append([]string{}, c.headers...), "Origin", testOrigin, "Content-Type", "application/json")
		if strings.Contains(name, "claiming the Cockpit page") {
			c.headers = c.headers[2:]
		}
		if recorder := f.do(c); recorder.Code != http.StatusUnauthorized || len(recorder.Result().Cookies()) != 0 {
			t.Errorf("%s: logout = %d with %d cookies, want 401 and nothing changed", name, recorder.Code, len(recorder.Result().Cookies()))
		}
	}

	// The same cookie with the key, as the Cockpit page sends it, is the owner
	// everywhere, after all of the above.
	for name, c := range map[string]call{
		"the cookie and its key":                       {cookie: cookie},
		"the cookie and its key from the Cockpit page": {cookie: cookie, headers: []string{"Origin", testOrigin, "Sec-Fetch-Site", "same-origin"}},
		"the other session with its own key":           {cookie: other},
	} {
		session, content, log, _ := f.everything(c)
		if !strings.Contains(session.Body.String(), `"principal":"owner"`) || !strings.Contains(session.Body.String(), routeSentinel) {
			t.Errorf("%s: the session route = %d %s, want the owner with the routes", name, session.Code, session.Body.String())
		}
		if content.Code != http.StatusOK || content.Body.String() != contentSentinel {
			t.Errorf("%s: the owner content route = %d %q", name, content.Code, content.Body.String())
		}
		if log.Code != http.StatusOK || !strings.Contains(log.Body.String(), logSentinel) {
			t.Errorf("%s: the daemon log = %d %q", name, log.Code, log.Body.String())
		}
	}

	// Where a request has no anonymous principal (through a proxy, or with
	// anonymous-local off) the cookie alone is refused, like no cookie.
	if recorder := f.do(call{target: sessionPath, cookie: cookie, noKey: true, headers: []string{"X-Forwarded-For", "203.0.113.7"}}); recorder.Code != http.StatusUnauthorized {
		t.Errorf("a proxied request with the cookie alone = %d, want 401", recorder.Code)
	}
	if got := f.who(call{cookie: cookie, headers: []string{"X-Forwarded-For", "203.0.113.7"}}); got.Name != PrincipalOwner {
		t.Errorf("a proxied request with the cookie and the key is %+v, want the owner", got)
	}
}

// cockpit#ac:session-key-ends-with-its-session. A key is good for its own
// session and for as long as that session: a new login has a new key, and
// expiry and logout drop it.
func TestTheSessionKeyEndsWithItsSession(t *testing.T) {
	t.Parallel()
	f := newKeyFixture(t)
	first := f.login()
	firstKey := f.keys[first.Value]

	// A new login in the same browser replaces the cookie and has its own key;
	// the earlier key does not open the new session.
	second := f.login()
	if got := f.who(call{cookie: second, noKey: true, headers: []string{SessionKeyHeader, firstKey}}); got.Name != PrincipalAnonymousLocal {
		t.Errorf("the earlier login's key with the new cookie is %+v", got)
	}
	if got := f.who(call{cookie: second}); got.Name != PrincipalOwner {
		t.Fatalf("the new login is %+v", got)
	}

	// Logout ends the session and its key.
	if recorder := f.logout(second, canonicalJSON...); recorder.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", recorder.Code)
	}
	if _, content, log, dump := f.everything(call{cookie: second}); content.Code != http.StatusUnauthorized || log.Code != http.StatusUnauthorized || strings.Contains(dump, `"principal":"owner"`) {
		t.Errorf("after logout the cookie and key = %d, %d: %s", content.Code, log.Code, dump)
	}

	// Expiry does too, whatever the browser still sends.
	f.now = f.now.Add(sessionLifetime - time.Nanosecond)
	if got := f.who(call{cookie: first}); got.Name != PrincipalOwner {
		t.Fatalf("just before expiry the session is %+v", got)
	}
	f.now = f.now.Add(time.Nanosecond)
	if _, content, log, dump := f.everything(call{cookie: first}); content.Code != http.StatusUnauthorized || log.Code != http.StatusUnauthorized || strings.Contains(dump, `"principal":"owner"`) {
		t.Errorf("after expiry the cookie and key = %d, %d: %s", content.Code, log.Code, dump)
	}
	// The next login drops the expired session, and its key digest, from memory.
	f.login()
	if held := len(f.server.sessions.(*memorySessions).live); held != 1 {
		t.Errorf("sessions held = %d, want only the live one", held)
	}
}

// cockpit#ac:session-key-is-never-served. The key leaves the daemon once, in
// the owner channel's answer to `wb cockpit`. No response on the loopback
// listener carries it in a body or a header, to the owner or to anyone, the
// daemon keeps only its digest, and nothing it logs names it.
func TestTheSessionKeyIsInNoResponseNoLogLineAndNoStore(t *testing.T) {
	t.Parallel()
	f := newKeyFixture(t)
	code := f.mint()
	key := f.minted[code]
	digest := DigestKey(key)
	forms := []string{
		key, hex.EncodeToString(digest[:]),
		base64.StdEncoding.EncodeToString(digest[:]), base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	var served strings.Builder
	record := func(recorders ...*httptest.ResponseRecorder) {
		for _, recorder := range recorders {
			served.WriteString(recorder.Body.String() + fmt.Sprint(recorder.Header()))
		}
	}

	// Before the code is presented the daemon already holds no key.
	held := func() string {
		return fmt.Sprintf("%v %v", f.server.codes.pending, f.server.sessions.(*memorySessions).live)
	}
	if strings.Contains(held(), key) {
		t.Fatal("the pending login code holds the session key itself")
	}
	login := f.exchange(code)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != PagePrefix {
		t.Fatalf("login = %d to %q, want a redirect to the application with no fragment of its own", login.Code, login.Header().Get("Location"))
	}
	cookie := login.Result().Cookies()[0]
	record(login)
	if strings.Contains(held(), key) || f.server.sessions.(*memorySessions).live == nil {
		t.Fatal("the session store holds the session key itself")
	}

	wrong := []string{SessionKeyHeader, "not-the-key"}
	for _, c := range []call{
		{cookie: cookie},
		{cookie: cookie, headers: []string{"Origin", testOrigin}},
		{cookie: cookie, noKey: true},
		{cookie: cookie, noKey: true, headers: wrong},
		{},
		{headers: []string{"Origin", hostedOrigin}},
		{cookie: cookie, headers: []string{"Origin", hostedOrigin}},
		{cookie: cookie, headers: []string{"Origin", "https://attacker.example"}},
		{cookie: cookie, headers: []string{"X-Forwarded-For", "203.0.113.7"}},
		{cookie: cookie, host: "attacker.example:8766"},
	} {
		session, content, log, _ := f.everything(c)
		record(session, content, log)
		record(f.do(withTarget(c, fleetPath)), f.do(withTarget(c, PagePrefix)), f.do(withTarget(c, APIPrefix+"missing")))
		c.method = http.MethodOptions
		record(f.do(withTarget(c, sessionPath)))
	}
	record(f.exchange(code), f.exchange("unknown"))
	record(f.logout(cookie, wrong...), f.logout(cookie, canonicalJSON...), f.do(call{target: sessionPath, cookie: cookie}))

	for _, form := range forms {
		if strings.Contains(served.String(), form) {
			t.Errorf("a response on the loopback listener carries the session key or its digest (%q)", form)
		}
	}
	// The battery did reach the owner's answers, so the search was not vacuous.
	for _, sentinel := range []string{routeSentinel, contentSentinel, logSentinel, fleetResponse} {
		if !strings.Contains(served.String(), sentinel) {
			t.Errorf("the battery never read %q", sentinel)
		}
	}
}

// The store compares the digest of the presented key with the digest it
// holds, with crypto/subtle, and makes the comparison whether or not the
// session exists.
func TestTheSessionStoreMatchesAKeyByItsDigestOnly(t *testing.T) {
	t.Parallel()
	sessions := newMemorySessions(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	now := time.Unix(1_700_000_000, 0)
	const key = "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"
	id, err := sessions.Create(now, DigestKey(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions.live {
		if session.key != DigestKey(key) || !session.expires.Equal(now.Add(sessionLifetime)) {
			t.Fatalf("the store holds %+v, want the key's digest and the expiry", session)
		}
	}
	var zero KeyDigest
	for name, test := range map[string]struct {
		id, key string
		at      time.Time
		want    bool
	}{
		"the session and its key":           {id, key, now, true},
		"its last instant":                  {id, key, now.Add(sessionLifetime - time.Nanosecond), true},
		"expired":                           {id, key, now.Add(sessionLifetime), false},
		"no key":                            {id, "", now, false},
		"a key one character short":         {id, key[1:], now, false},
		"a key differing in its last byte":  {id, key[:len(key)-1] + "j", now, false},
		"a key differing in its first byte": {id, "j" + key[1:], now, false},
		"an unknown session":                {"unknown", key, now, false},
		"an unknown session and no key":     {"unknown", "", now, false},
		"an unknown session, the zero key":  {"unknown", string(zero[:]), now, false},
	} {
		if got := sessions.Valid(test.id, test.key, test.at); got != test.want {
			t.Errorf("%s: valid = %v, want %v", name, got, test.want)
		}
	}
	sessions.End(id)
	if sessions.Valid(id, key, now) || len(sessions.live) != 0 {
		t.Error("an ended session still holds its key")
	}
}

// A session whose key was never handed out cannot be opened by presenting
// nothing: the digest of the empty key is not the zero digest, and a request
// with no key header is not looked up at all.
func TestASessionWithNoKnownKeyHasNoOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	id, err := f.server.sessions.Create(f.now, KeyDigest{})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookiePrefix + "8766", Value: id}
	for name, headers := range map[string][]string{
		"no key":        nil,
		"an empty key":  {SessionKeyHeader, ""},
		"a guessed key": {SessionKeyHeader, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		if got := f.who(call{cookie: cookie, headers: headers}); got.Name != PrincipalAnonymousLocal {
			t.Errorf("%s: a session with no known key is %+v", name, got)
		}
	}
}

// A code and its key are minted together or not at all.
func TestMintingWithEntropyForTheCodeOnlyIssuesNothing(t *testing.T) {
	t.Parallel()
	random := io.MultiReader(bytes.NewReader(make([]byte, secretBytes)), iotest.ErrReader(errors.New("no entropy left")))
	f := newFixture(t, func(options *Options) { options.Random = random })
	if issued, err := f.server.MintLoginCode(); err == nil || issued != (LoginCode{}) || len(f.server.codes.pending) != 0 {
		t.Fatalf("minting = %+v, %v with %d pending, want a failure and nothing pending", issued, err, len(f.server.codes.pending))
	}
}
