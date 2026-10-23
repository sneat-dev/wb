// Package cockpit is the loopback daemon's Cockpit surface: the request guard
// every Cockpit route sits behind, the API mux, and the mounts that attach
// both to the dashboard listener (spec/features/cockpit).
package cockpit

import (
	"net"
	"net/http"
	"strings"

	"github.com/sneat-dev/wb/cockpit/web"
	"github.com/sneat-dev/wb/internal/loopbackhost"
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
// scoped to from the daemon's listen address: the address the daemon listens
// on when that is a loopback IP literal (127.0.0.2, ::1, ...), written in its
// shortest form, otherwise "127.0.0.1" (a listener named "localhost", or an
// address that does not parse or is not loopback). A page is therefore
// served on the address the daemon really has, and every other loopback name
// redirects to it.
func CanonicalHost(listenAddress string) string {
	host, _, err := net.SplitHostPort(listenAddress)
	if ip := net.ParseIP(host); err == nil && ip != nil && ip.IsLoopback() {
		return ip.String()
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
// interface (cockpit#req:host-header-check). The rule is package
// loopbackhost's, which the dashboard's own routes on this listener share.
func loopbackName(host string) bool { return loopbackhost.Named(host) }

// splitHost separates a Host header into its host name and optional port, by
// the same shared rule.
func splitHost(value string) (host, port string) { return loopbackhost.Split(value) }

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
