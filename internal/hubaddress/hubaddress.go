// Package hubaddress holds the one rule for an address a machine credential is
// sent to: an HTTPS origin, or an http:// origin only on a loopback host, with
// no user information, query, fragment or path. It is a leaf package so that
// both internal/remotestate (remote.url) and internal/sessionmove
// (session_move.targets.<machine>.http.url) apply the same rule;
// internal/remotestate imports internal/sessionmove through
// internal/worktrees, so the rule cannot live in either of them.
package hubaddress

import (
	"net"
	"net/url"
	"strings"
)

// Valid reports whether raw is an origin a bearer credential may be sent to.
//
// Plain http is refused except on a loopback host because the credential
// travels in the Authorization header of every request, and a loopback origin
// is the one case with no network to intercept it.
func Valid(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
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

// IsLoopbackHost reports whether host is localhost or a loopback address.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
