package cmdpeers

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/peers"
)

func TestWritePeerDetailRendersConnectedSession(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	detail := peers.Detail{Session: &peers.Session{}}
	writePeerDetail(&buf, time.Now(), detail)
	out := buf.String()
	if !strings.Contains(out, "connected") {
		t.Fatalf("writePeerDetail output = %q, want it to report the session as connected", out)
	}
	if strings.Contains(out, "SESSION        none") {
		t.Fatalf("writePeerDetail output = %q, want it not to fall back to 'none'", out)
	}
}
func TestWritePeerDetailShowsConnectedSessionAndDefaultNodeID(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	detail := peers.Detail{
		Record:  peers.Record{Name: "laptop", Role: "member", Status: "active", ID: "peer-1"},
		Session: &peers.Session{ConnectedAt: now},
	}
	writePeerDetail(&out, now, detail)
	rendered := out.String()
	for _, want := range []string{"SESSION        connected", "NODE ID        -"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}
