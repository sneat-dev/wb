package cockpit

import (
	"net/http"
	"net/textproto"
	"slices"
)

// Capability is one permission a Cockpit route or action requires
// (cockpit#req:capability-vocabulary).
type Capability string

// The capability vocabulary. A Feature that defines an action adds the
// action's capability.
const (
	CapabilityFleetRead       Capability = "fleet.read"
	CapabilityMachineRead     Capability = "machine.read"
	CapabilityRepoRead        Capability = "repo.read"
	CapabilityWorktreeRead    Capability = "worktree.read"
	CapabilityBranchRead      Capability = "branch.read"
	CapabilityPRRead          Capability = "pr.read"
	CapabilityAgentRead       Capability = "agent.read"
	CapabilityRepoContentRead Capability = "repo.content.read"
)

// The two principals a request resolves to.
const (
	PrincipalAnonymousLocal = "anonymous-local"
	PrincipalOwner          = "owner"
)

// metadataCapabilities is what anonymous-local holds; contentCapabilities is
// what only an owner session adds.
var (
	metadataCapabilities = []Capability{
		CapabilityFleetRead, CapabilityMachineRead, CapabilityRepoRead, CapabilityWorktreeRead,
		CapabilityBranchRead, CapabilityPRRead, CapabilityAgentRead,
	}
	contentCapabilities = []Capability{CapabilityRepoContentRead}
)

// Principal is who a request acts as and what it may do.
type Principal struct {
	Name         string       `json:"principal"`
	Capabilities []Capability `json:"capabilities"`
}

// Has reports whether the principal holds capability.
func (principal Principal) Has(capability Capability) bool {
	return slices.Contains(principal.Capabilities, capability)
}

func anonymousLocal() Principal {
	return Principal{Name: PrincipalAnonymousLocal, Capabilities: slices.Clone(metadataCapabilities)}
}

func owner() Principal {
	return Principal{Name: PrincipalOwner, Capabilities: slices.Concat(metadataCapabilities, contentCapabilities)}
}

// forwardingHeaders are the headers a proxy or tunnel adds
// (cockpit#req:forwarded-requests-are-never-anonymous), in canonical form.
var forwardingHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Via", "Cf-Connecting-Ip",
}

// forwarded reports whether request carries a forwarding header, whatever
// its value and however its name is cased.
func forwarded(request *http.Request) bool {
	for name := range request.Header {
		if slices.Contains(forwardingHeaders, textproto.CanonicalMIMEHeaderKey(name)) {
			return true
		}
	}
	return false
}

// principal resolves request, which has passed Guard, to its principal. The
// second result is false when it has none and must be answered 401: it came
// through a proxy, or anonymous-local is switched off, and it carries no
// owner session. A request with the session cookie and no session key is not
// an error: it resolves like one with no cookie, to anonymous-local where that
// is on (cockpit#req:session-key). A request from the hosted origin never has
// a session, even when the browser sent the cookie
// (cockpit#req:cross-origin-allowance).
func (server *Server) principal(request *http.Request, from originKind) (Principal, bool) {
	if from != originHosted && server.sessionID(request) != "" {
		return owner(), true
	}
	if forwarded(request) || !server.config.AnonymousMetadata {
		return Principal{}, false
	}
	return anonymousLocal(), true
}

// LocalReader reports whether request was made on this machine by the Cockpit
// page itself or by a client that names no origin: the Host header names a
// loopback host (cockpit#req:host-header-check) and the Origin is the canonical
// one or absent, never the hosted origin and never a foreign one. It is the one
// classification of "a reader on this machine", for a caller that must tell
// such a reader from the hosted page, which reads the same metadata routes
// (cockpit-views#req:remote-exporter-transports counts only the former as
// demand).
func (server *Server) LocalReader(request *http.Request) bool {
	if host, _ := splitHost(request.Host); !loopbackName(host) {
		return false
	}
	from := server.originKindOf(request)
	return from == originNone || from == originCanonical
}

// IsOwner reports whether request acts as the owner principal, for a route the
// daemon serves on the same listener outside Cockpit's mounts and so outside
// Guard (cockpit#req:daemon-log-is-owner-only). It applies what the mounts
// apply to an owner read: the Host header names a loopback host
// (cockpit#req:host-header-check), the request comes from the Cockpit page or
// carries no Origin at all, never from the hosted or a foreign origin, and it
// carries a live owner session (cockpit#req:owner-session): the session cookie
// and, in SessionKeyHeader, the session key bound to it
// (cockpit#req:session-key). The cookie alone, which any other server on the
// loopback host is sent and can replay, is not the owner. Nothing else makes a
// request the owner's: there is no anonymous fallback here.
func (server *Server) IsOwner(request *http.Request) bool {
	return server.LocalReader(request) && server.sessionID(request) != ""
}
