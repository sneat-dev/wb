package cockpit

import (
	"crypto/sha256"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sessionLifetime is how long an owner session lasts. The cookie carries the
// same expiry, and the store enforces it whatever the browser does
// (cockpit#req:owner-session).
const sessionLifetime = 12 * time.Hour

// sessionCookiePrefix is the session cookie's name without the port.
const sessionCookiePrefix = "wb_cockpit_session_"

// SessionStore holds owner sessions. The daemon's store is in memory, so
// every session ends when the daemon restarts; the interface is the seam a
// test replaces. An identifier is a secret: an implementation never logs it.
type SessionStore interface {
	// Create starts a session at now and returns its identifier.
	Create(now time.Time) (string, error)
	// Valid reports whether id names a session that has not ended or expired
	// at now.
	Valid(id string, now time.Time) bool
	// End ends the session id names, if any.
	End(id string)
}

// memorySessions is the in-memory SessionStore. It is keyed by a digest of
// the identifier, so the identifier is not held and a lookup's timing says
// nothing about it.
type memorySessions struct {
	random io.Reader

	mu      sync.Mutex
	expires map[[sha256.Size]byte]time.Time
}

func newMemorySessions(random io.Reader) *memorySessions {
	return &memorySessions{random: random, expires: map[[sha256.Size]byte]time.Time{}}
}

func (sessions *memorySessions) Create(now time.Time) (string, error) {
	id, err := newSecret(sessions.random)
	if err != nil {
		return "", err
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for key, expires := range sessions.expires {
		if !now.Before(expires) {
			delete(sessions.expires, key)
		}
	}
	sessions.expires[sha256.Sum256([]byte(id))] = now.Add(sessionLifetime)
	return id, nil
}

func (sessions *memorySessions) Valid(id string, now time.Time) bool {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	expires, found := sessions.expires[sha256.Sum256([]byte(id))]
	return found && now.Before(expires)
}

func (sessions *memorySessions) End(id string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	delete(sessions.expires, sha256.Sum256([]byte(id)))
}

// sessionCookieName names the session cookie after the port the request
// arrived on, so two daemons reached on one host — one of them through a
// forward — keep separate sessions. A Host header with no port arrived on
// the scheme's default.
func sessionCookieName(request *http.Request) string {
	_, port := splitHost(request.Host)
	if port == "" {
		port = "80"
	}
	return sessionCookiePrefix + port
}

// sessionCookie builds the session cookie for request. A negative maxAge
// tells the browser to drop it. The cookie is Secure when a proxy says the
// browser reached it over https, so it is never sent back in clear text.
func sessionCookie(request *http.Request, value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookieName(request), Value: value, Path: "/",
		Expires: expires, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https"),
	}
}

// sameSiteFetch reports whether the browser, when it says where a request
// was initiated, says it was this origin or the user (a typed address, a
// bookmark). A request with no Sec-Fetch-Site header is not from a browser
// that sends one, and is judged by its Origin and cookie alone.
func sameSiteFetch(request *http.Request) bool {
	values := request.Header.Values("Sec-Fetch-Site")
	return len(values) == 0 || (len(values) == 1 && (values[0] == "same-origin" || values[0] == "none"))
}

// sessionID returns the identifier of request's live session, or "" when it
// has none. Every cookie carrying the session name is tried, so a cookie
// another loopback server planted under that name with a longer path cannot
// hide the real one. A request the browser marks as initiated by another
// site is given no session, whatever cookie it carries.
func (server *Server) sessionID(request *http.Request) string {
	if !sameSiteFetch(request) {
		return ""
	}
	now := server.now()
	for _, cookie := range request.CookiesNamed(sessionCookieName(request)) {
		if cookie.Value != "" && server.sessions.Valid(cookie.Value, now) {
			return cookie.Value
		}
	}
	return ""
}
