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
// owner session. A request from the hosted origin never has a session, even
// when the browser sent the cookie (cockpit#req:cross-origin-allowance).
func (server *Server) principal(request *http.Request, from originKind) (Principal, bool) {
	if from != originHosted && server.sessionID(request) != "" {
		return owner(), true
	}
	if forwarded(request) || !server.config.AnonymousMetadata {
		return Principal{}, false
	}
	return anonymousLocal(), true
}
