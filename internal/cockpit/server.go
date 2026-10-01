package cockpit

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/cockpit/web"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// LoginPath and LogoutPath are the two session routes under PagePrefix
// (cockpit#req:owner-session). The login code travels in LoginPath's "code"
// query parameter.
const (
	LoginPath  = PagePrefix + "session/login"
	LogoutPath = PagePrefix + "session/logout"
)

// LoginCodeRPCPath is the owner-channel route that mints a login code. The
// daemon registers it on the mux its unix socket serves, behind the owner
// token, and never on the loopback listener. The daemon's file bridge
// dispatches into that same mux but forwards only the DaemonService
// procedures it lists, so it refuses this path.
const LoginCodeRPCPath = "/wb.cockpit.v1/login-code"

// sessionRoute is the metadata route that reports the caller's principal.
const sessionRoute = "session"

// Options is what the daemon hands Cockpit at startup.
type Options struct {
	// CanonicalHost is the loopback name the listener has (see CanonicalHost).
	CanonicalHost string
	// Config is wb.yaml's cockpit: section, after defaults.
	Config wbconfig.CockpitConfig
	// Now is the clock login codes and sessions expire against; nil means
	// time.Now.
	Now func() time.Time
	// Random is the source of login codes and session identifiers; nil means
	// crypto/rand.
	Random io.Reader
	// Sessions holds the owner sessions; nil means a fresh in-memory store.
	Sessions SessionStore
}

// MetadataHandler serves one metadata route for a resolved principal.
type MetadataHandler func(http.ResponseWriter, *http.Request, Principal)

// OwnerHandler serves one owner route for a request with an owner session.
type OwnerHandler func(http.ResponseWriter, *http.Request)

// ownerRoute is one registered owner route.
type ownerRoute struct {
	method     string
	capability Capability
	handler    OwnerHandler
}

// Server is Cockpit's state for one daemon run: its login codes, its owner
// sessions and its routes.
type Server struct {
	canonical string
	config    wbconfig.CockpitConfig
	hosted    string
	now       func() time.Time
	codes     *loginCodes
	sessions  SessionStore
	app       http.Handler

	// The two registries are written until Mounts freezes them and only read
	// after, so serving needs no lock.
	registration sync.Mutex
	frozen       bool
	metadata     map[string]MetadataHandler
	owner        map[string]ownerRoute
}

// New builds Cockpit over the embedded application.
func New(options Options) *Server {
	return newServer(options, web.Handler())
}

// newServer is New over an injectable application handler.
func newServer(options Options, app http.Handler) *Server {
	now, random := options.Now, options.Random
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	sessions := options.Sessions
	if sessions == nil {
		sessions = newMemorySessions(random)
	}
	server := &Server{
		canonical: options.CanonicalHost, config: options.Config, hosted: originOf(options.Config.HostedURL),
		now: now, codes: &loginCodes{random: random}, sessions: sessions,
		app: app, metadata: map[string]MetadataHandler{}, owner: map[string]ownerRoute{},
	}
	// Any resolved principal may ask who it is; fleet.read is the capability
	// both principals hold.
	server.HandleMetadata(sessionRoute, CapabilityFleetRead, server.serveSession)
	// Logout needs the session and no capability beyond it.
	server.HandleOwner(http.MethodPost, LogoutPath, "", server.logout)
	return server
}

// register runs add unless the registries are frozen, which is a programming
// error: a route added while requests are served would race with them.
func (server *Server) register(route string, add func()) {
	server.registration.Lock()
	defer server.registration.Unlock()
	if server.frozen {
		panic("cockpit: route " + route + " registered after the mounts were taken")
	}
	add()
}

// HandleMetadata registers the metadata GET route APIPrefix+name, which
// requires capability. A metadata route is the only kind the hosted origin
// may read (cockpit#req:cross-origin-allowance), so its handler must return
// nothing beyond the metadata field set, and registering one with a
// capability that is not a metadata capability panics. Routes are registered
// before Mounts is called.
func (server *Server) HandleMetadata(name string, capability Capability, handler MetadataHandler) {
	if !slices.Contains(metadataCapabilities, capability) {
		panic("cockpit: metadata route " + name + " requires " + string(capability) + ", which is not a metadata capability; register it with HandleOwner")
	}
	server.register(name, func() { server.metadata[name] = handler })
}

// HandleOwner registers an owner route — one that returns content or changes
// state — at path, under PagePrefix or APIPrefix, for one method. It requires
// an owner session holding capability; an empty capability requires the
// session alone. A route whose method is not GET changes state, and also
// requires the canonical origin and a JSON content type
// (cockpit#req:owner-routes). No owner route ever receives the cross-origin
// allowance. Routes are registered before Mounts is called.
func (server *Server) HandleOwner(method, path string, capability Capability, handler OwnerHandler) {
	server.register(path, func() { server.owner[path] = ownerRoute{method: method, capability: capability, handler: handler} })
}

// Mounts returns Cockpit's two subtrees, each behind Guard with the canonical
// host, in the shape dashboard.Options.Mounts takes. They are mounted whether
// or not wb.yaml has a hub: section. Taking the mounts freezes the route
// registries: a registration after it panics.
func (server *Server) Mounts() map[string]http.Handler {
	server.registration.Lock()
	server.frozen = true
	server.registration.Unlock()
	return map[string]http.Handler{
		PagePrefix: pagePolicy(Guard(server.canonical, http.HandlerFunc(server.servePage))),
		APIPrefix:  Guard(server.canonical, http.HandlerFunc(server.serveAPI)),
	}
}

// pagePolicy sets the strict page Content-Security-Policy before next writes
// anything, so every response under PagePrefix carries it whichever code path
// answers (the guard's 421 and redirect, a refusal, the login exchange). The
// application handler replaces it with its own nonce-matched policy. It is
// not applied to APIPrefix: those responses are JSON or redirects, never
// documents, so a page policy there would govern nothing.
func pagePolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", web.FreshPolicy())
		next.ServeHTTP(writer, request)
	})
}

// MountsWith returns Cockpit's mounts added to others (the hub's, which is nil
// without a hub: section). The others are copied, never modified.
func (server *Server) MountsWith(others map[string]http.Handler) map[string]http.Handler {
	return mergeMounts(others, server.Mounts())
}

// MintLoginCode issues a single-use login code. Only the owner channel may
// reach it.
func (server *Server) MintLoginCode() (LoginCode, error) {
	return server.codes.mint(server.now())
}

// loginCodeResponse is what LoginCodeRPCPath answers. The login URL is Path
// on the canonical origin with the code in its "code" query parameter.
type loginCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	Path      string    `json:"path"`
}

// LoginCodeHandler serves LoginCodeRPCPath. The caller mounts it behind the
// owner token; it does no authentication of its own.
func (server *Server) LoginCodeHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setAPIHeaders(writer)
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeAPIError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		issued, err := server.MintLoginCode()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, "mint a login code: "+err.Error())
			return
		}
		_ = json.NewEncoder(writer).Encode(loginCodeResponse{Code: issued.Code, ExpiresAt: issued.ExpiresAt, Path: LoginPath})
	})
}

// setAPIHeaders marks a response as JSON that is neither stored nor sniffed.
func setAPIHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

// writeAPIError writes status with a JSON {"error": message} body.
func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

// serveAPI answers every route under APIPrefix. The cross-origin rule comes
// first: a foreign origin is refused everywhere, and the hosted origin
// everywhere but on a metadata route.
func (server *Server) serveAPI(writer http.ResponseWriter, request *http.Request) {
	setAPIHeaders(writer)
	// Every answer depends on the Origin header, whoever asks.
	writer.Header().Set("Vary", "Origin")
	from := server.originKindOf(request)
	metadata, isMetadata := server.metadata[strings.TrimPrefix(request.URL.Path, APIPrefix)]
	if from == originForeign || (from == originHosted && !isMetadata) {
		writeAPIError(writer, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if isMetadata {
		server.serveMetadata(writer, request, metadata, from)
		return
	}
	if route, found := server.owner[request.URL.Path]; found {
		server.serveOwner(writer, request, route, from)
		return
	}
	writeAPIError(writer, http.StatusNotFound, "not found")
}

// serveMetadata answers a metadata route: the hosted origin's preflight and
// allowance, the method (405), then the principal (401).
func (server *Server) serveMetadata(writer http.ResponseWriter, request *http.Request, handler MetadataHandler, from originKind) {
	if from == originHosted {
		if request.Method == http.MethodOptions {
			server.preflight(writer, request)
			return
		}
		server.allowHosted(writer)
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal, resolved := server.principal(request, from)
	if !resolved {
		writeAPIError(writer, http.StatusUnauthorized, "an owner session is required; run `wb cockpit`")
		return
	}
	handler(writer, request, principal)
}

// serveOwner answers an owner route for a request that is not from a foreign
// origin: the method (405), for a state-changing route the canonical origin
// (403) and a JSON content type (415), then the session (401) and its
// capability (403). A refused request has no effect.
func (server *Server) serveOwner(writer http.ResponseWriter, request *http.Request, route ownerRoute, from originKind) {
	if request.Method != route.method {
		writer.Header().Set("Allow", route.method)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if route.method != http.MethodGet {
		if from != originCanonical {
			writeAPIError(writer, http.StatusForbidden, "a state-changing request must come from the Cockpit page")
			return
		}
		if !jsonContent(request) {
			writeAPIError(writer, http.StatusUnsupportedMediaType, "a state-changing request must have a JSON content type")
			return
		}
	}
	if server.sessionID(request) == "" {
		writeAPIError(writer, http.StatusUnauthorized, "an owner session is required; run `wb cockpit`")
		return
	}
	if route.capability != "" && !owner().Has(route.capability) {
		writeAPIError(writer, http.StatusForbidden, "this session lacks the capability "+string(route.capability))
		return
	}
	route.handler(writer, request)
}

// sessionResponse is what the session route answers: the principal and its
// capabilities, and the configured code browser base, which the application
// builds its repository links from (cockpit#req:code-browser-link). The base
// is configuration, not state or content, and is not secret.
type sessionResponse struct {
	Principal
	CodeBrowserURL string `json:"code_browser_url"`
}

// serveSession reports the caller's principal and effective capabilities
// (cockpit#req:effective-permissions-are-discoverable) and the code browser
// base.
func (server *Server) serveSession(writer http.ResponseWriter, _ *http.Request, principal Principal) {
	_ = json.NewEncoder(writer).Encode(sessionResponse{Principal: principal, CodeBrowserURL: server.config.CodeBrowserURL})
}

// servePage answers everything under PagePrefix: the login exchange, the
// owner routes there and the application. No foreign origin, the hosted one
// included, may address a page route.
func (server *Server) servePage(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Vary", "Origin")
	from := server.originKindOf(request)
	if from == originForeign || from == originHosted {
		writer.Header().Set("Cache-Control", "no-store")
		http.Error(writer, "cross-origin request refused\n", http.StatusForbidden)
		return
	}
	if request.URL.Path == LoginPath {
		server.login(writer, request)
		return
	}
	if route, found := server.owner[request.URL.Path]; found {
		setAPIHeaders(writer)
		server.serveOwner(writer, request, route, from)
		return
	}
	server.app.ServeHTTP(writer, request)
}

// login exchanges a login code for the session cookie and redirects to the
// application, so the code does not stay in the address bar. It is the one
// route protected by its code instead of a session. A refused code sets no
// cookie.
func (server *Server) login(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed\n", http.StatusMethodNotAllowed)
		return
	}
	now := server.now()
	if !server.codes.exchange(request.URL.Query().Get("code"), now) {
		http.Error(writer, "this login code is unknown, already used or expired; run `wb cockpit` again\n", http.StatusUnauthorized)
		return
	}
	id, err := server.sessions.Create(now)
	if err != nil {
		http.Error(writer, "start a session: "+err.Error()+"\n", http.StatusInternalServerError)
		return
	}
	http.SetCookie(writer, sessionCookie(request, id, now.Add(sessionLifetime), int(sessionLifetime/time.Second)))
	http.Redirect(writer, request, PagePrefix, http.StatusSeeOther)
}

// logout ends the caller's session and tells the browser to drop the cookie.
// It is an owner route, so serveOwner has already required the session.
func (server *Server) logout(writer http.ResponseWriter, request *http.Request) {
	server.sessions.End(server.sessionID(request))
	http.SetCookie(writer, sessionCookie(request, "", time.Unix(0, 0), -1))
	writer.WriteHeader(http.StatusNoContent)
}
