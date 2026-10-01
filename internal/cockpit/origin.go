package cockpit

import (
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// originKind is where a request says it comes from.
type originKind int

const (
	// originNone is a request with no Origin header: a navigation, a
	// same-origin GET, or a client that is not a browser.
	originNone originKind = iota
	// originCanonical is the Cockpit page itself.
	originCanonical
	// originHosted is the hosted Cockpit page, the one foreign origin that
	// may read metadata.
	originHosted
	// originForeign is anything else, including the literal "null".
	originForeign
)

// originOf renders the origin of a URL the way a browser serialises it in an
// Origin header: lower-case scheme and host, and no port when it is the
// scheme's default. The path, and so a trailing slash, plays no part. It
// returns "" for a URL with no host, which matches no request.
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	scheme, port := strings.ToLower(parsed.Scheme), parsed.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	return scheme + "://" + origin(strings.ToLower(parsed.Hostname()), port)
}

// canonicalOrigin is the origin Cockpit's pages are served on for request:
// the canonical loopback name with the port the request arrived on.
func (server *Server) canonicalOrigin(request *http.Request) string {
	_, port := splitHost(request.Host)
	return "http://" + origin(server.canonical, port)
}

// originKindOf classifies request by its Origin header. The comparison is
// exact, and a request with more than one Origin header is foreign.
func (server *Server) originKindOf(request *http.Request) originKind {
	values := request.Header.Values("Origin")
	switch {
	case len(values) == 0:
		return originNone
	case len(values) > 1:
		return originForeign
	case values[0] == server.canonicalOrigin(request):
		return originCanonical
	case server.hosted != "" && values[0] == server.hosted:
		return originHosted
	}
	return originForeign
}

// allowHosted writes the cross-origin allowance for the hosted origin and
// exposes the ETag, so the hosted page can revalidate with it
// (cockpit-views#req:hosted-origin-conditional-requests). It never allows
// credentials.
func (server *Server) allowHosted(writer http.ResponseWriter) {
	writer.Header().Set("Access-Control-Allow-Origin", server.hosted)
	writer.Header().Set("Access-Control-Expose-Headers", "ETag")
}

// preflightHeaders are the request headers the hosted page may send: the
// safelisted ones, which a browser names in a preflight only when their value
// is unusual, and If-None-Match, which a conditional request carries.
var preflightHeaders = []string{"accept", "accept-language", "content-language", "content-type", "if-none-match"}

// preflightMaxAge is how long, in seconds, a browser may reuse a preflight
// answer.
const preflightMaxAge = "600"

// preflight answers the hosted origin's preflight for a metadata route. A
// public page asking a loopback address needs the private-network allowance
// as well. Only GET is allowed, with no header beyond preflightHeaders.
func (server *Server) preflight(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Vary", writer.Header().Get("Vary")+", Access-Control-Request-Method, Access-Control-Request-Headers")
	if request.Header.Get("Access-Control-Request-Method") != http.MethodGet {
		writeAPIError(writer, http.StatusForbidden, "only GET is allowed from the hosted origin")
		return
	}
	var asked []string
	for _, name := range strings.Split(strings.Join(request.Header.Values("Access-Control-Request-Headers"), ","), ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !slices.Contains(preflightHeaders, name) {
			writeAPIError(writer, http.StatusForbidden, "that request header is not allowed from the hosted origin")
			return
		}
		asked = append(asked, name)
	}
	server.allowHosted(writer)
	writer.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
	if len(asked) > 0 {
		writer.Header().Set("Access-Control-Allow-Headers", strings.Join(asked, ", "))
	}
	writer.Header().Set("Access-Control-Allow-Private-Network", "true")
	writer.Header().Set("Access-Control-Max-Age", preflightMaxAge)
	writer.WriteHeader(http.StatusNoContent)
}

// jsonContent reports whether request declares a JSON body.
func jsonContent(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
