package loopbackhost

import (
	"net/http/httptest"
	"testing"
)

// TestLoopbackHostsAreTheLoopbackNamesAndAddresses is the rule of
// cockpit#req:host-header-check, shared with the daemon's --listen check: any
// address of 127.0.0.0/8, ::1 in any spelling, localhost and *.localhost, with
// or without a numeric port and in any case, and nothing else.
func TestLoopbackHostsAreTheLoopbackNamesAndAddresses(t *testing.T) {
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
		"127.0.0.2:8766":        {"127.0.0.2", "8766", true},
		"127.255.255.254":       {"127.255.255.254", "", true},
		"[0:0:0:0:0:0:0:1]:80":  {"0:0:0:0:0:0:0:1", "80", true},
		"[::ffff:127.0.0.1]":    {"::ffff:127.0.0.1", "", true},
		"app.localhost:8766":    {"app.localhost", "8766", false},
		"foo.localhost":         {"foo.localhost", "", false},
		"foo.localhost.":        {"foo.localhost.", "", false},
		".localhost":            {".localhost", "", false},
		"localhost.evil.com":    {"localhost.evil.com", "", false},
		"user@localhost":        {"user@localhost", "", false},
		"user:pw@127.0.0.1":     {"", "", false},
		"user:pw@127.0.0.1:80":  {"", "", false},
		"[::1%lo0]":             {"::1%lo0", "", false},
		"[::1%lo0]:8766":        {"::1%lo0", "8766", false},
		"0x7f.1":                {"0x7f.1", "", false},
		"0x7f.0.0.1":            {"0x7f.0.0.1", "", false},
		"127.1":                 {"127.1", "", false},
		"2130706433":            {"2130706433", "", false},
		"0177.0.0.1":            {"0177.0.0.1", "", false},
		"127.000.000.001":       {"127.000.000.001", "", false},
		"0.0.0.0":               {"0.0.0.0", "", false},
		"[::]":                  {"::", "", false},
		"[::]:8766":             {"::", "8766", false},
		"localhost:abc":         {"", "", false},
		"xn--lcalhost-x4a.test": {"xn--lcalhost-x4a.test", "", false},
		"xn--localhost-2ya":     {"xn--localhost-2ya", "", false},
		"128.0.0.1:8766":        {"128.0.0.1", "8766", false},
		"[::2]":                 {"::2", "", false},
		"localhost.evil.test":   {"localhost.evil.test", "", false},
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
