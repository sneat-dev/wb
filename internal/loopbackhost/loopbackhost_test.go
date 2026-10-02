package loopbackhost

import (
	"net/http/httptest"
	"testing"
)

// TestOnlyTheThreeLoopbackNamesAreLoopbackHosts is the rule of
// cockpit#req:host-header-check: localhost, 127.0.0.1 and ::1, with or without
// a numeric port and in any case, and nothing else, not even another address of
// the loopback network.
func TestOnlyTheThreeLoopbackNamesAreLoopbackHosts(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]struct {
		host, port string
		loopback   bool
	}{
		"127.0.0.1:8766":        {"127.0.0.1", "8766", true},
		"127.0.0.1":             {"127.0.0.1", "", true},
		"localhost:8766":        {"localhost", "8766", true},
		"LOCALHOST":             {"LOCALHOST", "", true},
		"[::1]:8766":            {"::1", "8766", true},
		"[::1]":                 {"::1", "", true},
		"127.0.0.2:8766":        {"127.0.0.2", "8766", false},
		"attacker.example":      {"attacker.example", "", false},
		"attacker.example:8766": {"attacker.example", "8766", false},
		"127.0.0.1:http":        {"", "", false},
		"127.0.0.1:99999":       {"", "", false},
		"::1":                   {"", "", false},
		"[bad":                  {"", "", false},
		"":                      {"", "", false},
		"localhost.":            {"localhost.", "", false},
	} {
		host, port := Split(value)
		if host != want.host || port != want.port || Named(host) != want.loopback {
			t.Errorf("%q: host %q port %q loopback %v, want %q %q %v", value, host, port, Named(host), want.host, want.port, want.loopback)
		}
		request := httptest.NewRequest("GET", "/", nil)
		request.Host = value
		if Request(request) != want.loopback {
			t.Errorf("%q: a request with that Host is loopback = %v, want %v", value, Request(request), want.loopback)
		}
	}
}
