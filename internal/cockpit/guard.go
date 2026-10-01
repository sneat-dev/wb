// Package cockpit is the loopback daemon's Cockpit surface: the request guard
// every Cockpit route sits behind, the API mux, and the mounts that attach
// both to the dashboard listener (spec/features/cockpit).
package cockpit

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/cockpit/web"
)

// PagePrefix and APIPrefix are the two subtrees Cockpit owns on the loopback
// listener (cockpit#req:cockpit-mount).
const (
	PagePrefix = web.MountPath
	APIPrefix  = "/api/v1/cockpit/"
)

// apiRoot is APIPrefix without its slash, which names the API subtree too.
var apiRoot = strings.TrimSuffix(APIPrefix, "/")

// CanonicalHost picks the one loopback name Cockpit's session cookie is
// scoped to from the daemon's listen address: "::1" when the listener is the
// IPv6 loopback address, otherwise "127.0.0.1" (including a listener named
// "localhost", or an address that does not parse).
func CanonicalHost(listenAddress string) string {
	host, _, err := net.SplitHostPort(listenAddress)
	if ip := net.ParseIP(host); err == nil && ip != nil && ip.Equal(net.IPv6loopback) {
		return "::1"
	}
	return "127.0.0.1"
}

// origin renders a host and optional port as the host part of an origin.
func origin(host, port string) string {
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// loopbackName reports whether a Host header's host part names the loopback
// interface. Only the three names localhost, 127.0.0.1 and ::1 (written [::1]
// in a Host header) qualify (cockpit#req:host-header-
// check); an address such as 127.0.0.2 is refused like any other host.
func loopbackName(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// splitHost separates a Host header into its host name and optional port. A
// bracketed IPv6 literal loses its brackets. An empty or malformed value
// yields an empty host, which loopbackName refuses, as does a port that is
// present but not a number.
func splitHost(value string) (host, port string) {
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

// Guard refuses a request whose Host header does not name a loopback host
// with status 421, before next runs: that is what stops a page that rebinds
// DNS to the loopback address. The port is not checked beyond being a number,
// so an SSH forward to another local port works.
//
// canonical is the loopback name the daemon's listener has (see
// CanonicalHost). A GET or HEAD for a page on any other loopback name is
// redirected to the same path on http://<canonical>:<port>, using the port
// the browser reached; any other method there is refused with 421 so it is
// never replayed to another origin. An API request on an alias is served.
func Guard(canonical string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, port := splitHost(request.Host)
		if !loopbackName(host) {
			http.Error(writer, "misdirected request: Cockpit answers only on a loopback host name\n", http.StatusMisdirectedRequest)
			return
		}
		isAPI := request.URL.Path == apiRoot || strings.HasPrefix(request.URL.Path, APIPrefix)
		if strings.EqualFold(host, canonical) || isAPI {
			next.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(writer, "misdirected request: use the canonical origin\n", http.StatusMisdirectedRequest)
			return
		}
		http.Redirect(writer, request, "http://"+origin(canonical, port)+request.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
}
