// Package hubaddress holds the one rule for an address a machine credential is
// sent to, and the proxy policy of a client that sends one. It is a leaf
// package so that both internal/remotestate (remote.url) and
// internal/sessionmove (session_move.targets.<machine>.http.url) apply the same
// rule; internal/remotestate imports internal/sessionmove through
// internal/worktrees, so the rule cannot live in either of them.
package hubaddress

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Valid reports whether raw is an origin a bearer credential may be sent to: an
// https origin, or an http origin only on a loopback host, with no user
// information, no query (not even an empty one), no fragment and no path
// beyond an optional single "/". There is no base path: the routes a
// credential is sent to are fixed paths on the origin.
//
// Plain http is refused except on a loopback host because the credential
// travels in the Authorization header of every request, and a loopback origin
// is the one case with no network to intercept it. Even there the credential
// goes to whatever process listens on that local port (a tunnel's local end
// included), so https is the better choice wherever it is available.
func Valid(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(trimmed, "?#") || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		return IsLoopbackHost(parsed.Hostname())
	}
	return false
}

// IsLoopbackHost reports whether host is exactly "localhost" or a loopback IP
// address. The name is matched in lower case only: Go's own exemption of
// loopback hosts from proxying is case-sensitive, and one spelling leaves
// nothing to disagree about.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Origin is raw as the origin a request is built on: trimmed, with the scheme
// and host in lower case and no trailing slash. It is meant for an address
// that passed Valid.
func Origin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

// Proxy is the proxy policy of a client that sends a machine credential.
var Proxy = ProxyFrom(http.ProxyFromEnvironment)

// ProxyFrom is the policy over environment, the function that says which proxy
// the environment names for a request. A request that is not https, or that is
// for a loopback host, is never proxied: an http request through a proxy is
// sent to it whole, Authorization header included. An https request follows
// the environment, because it passes through a proxy as a CONNECT tunnel that
// does not see the header.
func ProxyFrom(environment func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(request *http.Request) (*url.URL, error) {
		if request.URL.Scheme != "https" || IsLoopbackHost(strings.ToLower(request.URL.Hostname())) {
			return nil, nil
		}
		return environment(request)
	}
}
