package daemonhost

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestDaemonServesOnlyWhatIsBoundToLoopback(t *testing.T) {
	t.Parallel()
	for name, bound := range map[string]net.Addr{
		"loopback v4":         &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8766},
		"another loopback v4": &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 8766},
		"loopback v6":         &net.TCPAddr{IP: net.IPv6loopback, Port: 8766},
	} {
		if err := requireLoopbackBound(bound, usageError); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, bound := range map[string]net.Addr{
		"non-loopback v4": &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 8766},
		"non-loopback v6": &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 8766},
		"unspecified":     &net.TCPAddr{IP: net.IPv4zero, Port: 8766},
		"not TCP":         &net.UnixAddr{Name: "/tmp/x.sock", Net: "unix"},
	} {
		err := requireLoopbackBound(bound, usageError)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), bound.String()) {
			t.Errorf("%s: err = %v, want a usage error naming %s", name, err, bound)
		}
	}
}
