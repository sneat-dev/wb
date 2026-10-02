// Package loopbackhost is the one rule for a Host header that names the
// loopback interface (cockpit#req:host-header-check). Cockpit's guard and the
// dashboard's own routes on the same listener both ask it, so that a page which
// rebinds DNS to the loopback address is refused by one rule and not by two
// that could drift apart.
package loopbackhost

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Named reports whether a Host header's host part names the loopback
// interface. Only the three names localhost, 127.0.0.1 and ::1 (written [::1]
// in a Host header) qualify; an address such as 127.0.0.2 is refused like any
// other host.
func Named(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Split separates a Host header into its host name and optional port. A
// bracketed IPv6 literal loses its brackets. An empty or malformed value
// yields an empty host, which Named refuses, as does a port that is present
// but not a number.
func Split(value string) (host, port string) {
	if host, port, err := net.SplitHostPort(value); err == nil {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", ""
		}
		return host, port
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return value[1 : len(value)-1], ""
	}
	if strings.ContainsAny(value, "[]:") {
		return "", ""
	}
	return value, ""
}

// Request reports whether request's Host header names a loopback host.
func Request(request *http.Request) bool {
	host, _ := Split(request.Host)
	return Named(host)
}
