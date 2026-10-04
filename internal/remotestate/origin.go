package remotestate

import (
	"net/url"
	"strings"
)

func SameOrigin(a, b string) bool {
	parsedA, errA := url.Parse(a)
	parsedB, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	schemeA, schemeB := strings.ToLower(parsedA.Scheme), strings.ToLower(parsedB.Scheme)
	return schemeA == schemeB && normalizeOriginHost(parsedA, schemeA) == normalizeOriginHost(parsedB, schemeB)
}
func normalizeOriginHost(parsed *url.URL, scheme string) string {
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimSuffix(host, ".")
	port := parsed.Port()
	defaultPort := map[string]string{"https": "443", "http": "80"}[scheme]
	if port == "" || port == defaultPort {
		return host
	}
	return host + ":" + port
}
