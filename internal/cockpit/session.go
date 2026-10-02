package cockpit

import (
	"crypto/sha256"
	"crypto/subtle"
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

// SessionKeyHeader is the request header that carries the session key
// (cockpit#req:session-key). The cookie is sent by the browser to every server
// on the loopback host, whatever its port; the key is held by the Cockpit page
// alone, in the storage of its own origin, and sent only by its own requests.
const SessionKeyHeader = "X-Wb-Cockpit-Session-Key"

// KeyDigest is the SHA-256 digest of a session key: the only form of the key
// the daemon keeps once it has handed the key to the owner channel.
type KeyDigest [sha256.Size]byte

// DigestKey is the digest of key.
func DigestKey(key string) KeyDigest { return sha256.Sum256([]byte(key)) }

// SessionStore holds owner sessions. The daemon's store is in memory, so
// every session ends when the daemon restarts; the interface is the seam a
// test replaces. An identifier and a key are secrets: an implementation never
// logs either, and never holds the key itself.
type SessionStore interface {
	// Create starts a session at now, bound to the session key whose digest
	// is key, and returns its identifier.
	Create(now time.Time, key KeyDigest) (string, error)
	// Valid reports whether id names a session that has not ended or expired
	// at now and key is the session key it was created with. The key is
	// compared in constant time.
	Valid(id, key string, now time.Time) bool
	// End ends the session id names, if any, and forgets its key.
	End(id string)
}

// memorySession is one live session: when it ends and the digest of its key.
type memorySession struct {
	expires time.Time
	key     KeyDigest
}

// memorySessions is the in-memory SessionStore. It is keyed by a digest of
// the identifier, so the identifier is not held and a lookup's timing says
// nothing about it.
type memorySessions struct {
	random io.Reader

	mu   sync.Mutex
	live map[[sha256.Size]byte]memorySession
}

func newMemorySessions(random io.Reader) *memorySessions {
	return &memorySessions{random: random, live: map[[sha256.Size]byte]memorySession{}}
}

func (sessions *memorySessions) Create(now time.Time, key KeyDigest) (string, error) {
	id, err := newSecret(sessions.random)
	if err != nil {
		return "", err
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for digest, session := range sessions.live {
		if !now.Before(session.expires) {
			delete(sessions.live, digest)
		}
	}
	sessions.live[sha256.Sum256([]byte(id))] = memorySession{expires: now.Add(sessionLifetime), key: key}
	return id, nil
}

func (sessions *memorySessions) Valid(id, key string, now time.Time) bool {
	presented := DigestKey(key)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	session, found := sessions.live[sha256.Sum256([]byte(id))]
	// The digests are compared whether or not the session was found, so the
	// answer's timing does not tell an unknown session from a wrong key.
	matches := subtle.ConstantTimeCompare(session.key[:], presented[:]) == 1
	return found && matches && now.Before(session.expires)
}

func (sessions *memorySessions) End(id string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	delete(sessions.live, sha256.Sum256([]byte(id)))
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
// has none. A session takes both halves (cockpit#req:session-key): the cookie,
// and the session key in SessionKeyHeader, exactly once. The cookie alone is
// no session, because every other server on the loopback host receives it and
// could replay it; such a request is simply not the owner's. Every cookie
// carrying the session name is tried, so a cookie another loopback server
// planted under that name with a longer path cannot hide the real one. A
// request the browser marks as initiated by another site is given no session,
// whatever it carries.
func (server *Server) sessionID(request *http.Request) string {
	if !sameSiteFetch(request) {
		return ""
	}
	keys := request.Header.Values(SessionKeyHeader)
	if len(keys) != 1 || keys[0] == "" {
		return ""
	}
	now := server.now()
	for _, cookie := range request.CookiesNamed(sessionCookieName(request)) {
		if cookie.Value != "" && server.sessions.Valid(cookie.Value, keys[0], now) {
			return cookie.Value
		}
	}
	return ""
}
