package cmdpeers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/peers"
)

func TestPeersListGoldenTextAndJSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-2 * time.Hour)
	lag37 := int64(37)
	lag99 := int64(99)
	downstream := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "alex-macbook", Role: "downstream", Status: "connected", LastSeenAt: &lastSeen}
	upstream := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "vm1.sneat.dev", Name: "vm1.sneat.dev", Role: "upstream", Status: "offline", ResetPending: true}
	withLag := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_2", Name: "dev-mac", Role: "downstream", Status: "offline", Lag: &lag37}
	lagAndReset := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_3", Name: "old-laptop", Role: "downstream", Status: "blocked", Lag: &lag99, ResetPending: true}
	rows := []peers.Record{downstream, upstream, withLag, lagAndReset}

	var text bytes.Buffer
	writePeersTable(&text, now, rows)
	const rowFormat = "%-13s %-11s %-10s %-10s %-9s %-6s %s\n"
	wantText := fmt.Sprintf(rowFormat, "NAME", "ROLE", "STATUS", "LAST SEEN", "CONNECTED", "CURSOR", "LAG") +
		fmt.Sprintf(rowFormat, "alex-macbook", "downstream", "connected", "2h ago", "-", "-", "-") +
		fmt.Sprintf(rowFormat, "vm1.sneat.dev", "upstream", "offline", "-", "-", "-", "reset") +
		fmt.Sprintf(rowFormat, "dev-mac", "downstream", "offline", "-", "-", "-", "37") +
		// A pending reset takes priority over a present lag value in the
		// rendered column: the last-known count is stale until
		// reconciliation completes.
		fmt.Sprintf(rowFormat, "old-laptop", "downstream", "blocked", "-", "-", "-", "reset")
	if text.String() != wantText {
		t.Fatalf("golden text mismatch:\ngot:  %q\nwant: %q", text.String(), wantText)
	}

	var gotJSON bytes.Buffer
	if err := json.NewEncoder(&gotJSON).Encode(peers.ListResponse{SchemaVersion: peers.SchemaVersion, Peers: rows}); err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"schema_version":1,"peers":[` +
		`{"schema_version":1,"id":"machine_1","name":"alex-macbook","role":"downstream","status":"connected","created_at":"0001-01-01T00:00:00Z","last_seen_at":"2026-09-18T10:00:00Z"},` +
		`{"schema_version":1,"id":"vm1.sneat.dev","name":"vm1.sneat.dev","role":"upstream","status":"offline","created_at":"0001-01-01T00:00:00Z","reset_pending":true},` +
		`{"schema_version":1,"id":"machine_2","name":"dev-mac","role":"downstream","status":"offline","created_at":"0001-01-01T00:00:00Z","lag":37},` +
		`{"schema_version":1,"id":"machine_3","name":"old-laptop","role":"downstream","status":"blocked","created_at":"0001-01-01T00:00:00Z","reset_pending":true,"lag":99}` +
		"]}\n"
	if gotJSON.String() != wantJSON {
		t.Fatalf("golden JSON mismatch:\ngot:  %s\nwant: %s", gotJSON.String(), wantJSON)
	}
}
func TestPeersGetGoldenTextAndJSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-90 * time.Minute)
	downstream := peers.Detail{Record: peers.Record{
		SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "alex-macbook", Role: "downstream",
		Status: "connected", NodeID: "abcd1234", LastSeenAt: &lastSeen,
	}}
	wantDownstreamText := "NAME           alex-macbook\n" +
		"ROLE           downstream\n" +
		"STATUS         connected\n" +
		"PEER ID        machine_1\n" +
		"NODE ID        abcd1234\n" +
		"LAST SEEN      1h ago\n" +
		"RESET PENDING  false\n" +
		"SESSION        none\n" +
		"COUNTERS       rx_events=0 tx_events=0 rx_bytes=0 tx_bytes=0\n" +
		"ADMIN          false\n"
	var downstreamText bytes.Buffer
	writePeerDetail(&downstreamText, now, downstream)
	if downstreamText.String() != wantDownstreamText {
		t.Fatalf("golden downstream detail text mismatch:\ngot:  %q\nwant: %q", downstreamText.String(), wantDownstreamText)
	}
	wantDownstreamJSON := `{"schema_version":1,"id":"machine_1","name":"alex-macbook","role":"downstream","status":"connected","node_id":"abcd1234","created_at":"0001-01-01T00:00:00Z","last_seen_at":"2026-09-18T10:30:00Z","session":null,"counters":{"rx_payload_bytes":0,"tx_payload_bytes":0,"rx_messages":0,"tx_messages":0,"rx_events":0,"tx_events":0},"admin_available":false}` + "\n"
	var downstreamJSON bytes.Buffer
	if err := json.NewEncoder(&downstreamJSON).Encode(downstream); err != nil {
		t.Fatal(err)
	}
	if downstreamJSON.String() != wantDownstreamJSON {
		t.Fatalf("golden downstream detail JSON mismatch:\ngot:  %s\nwant: %s", downstreamJSON.String(), wantDownstreamJSON)
	}

	upstream := peers.Detail{Record: peers.Record{
		SchemaVersion: peers.SchemaVersion, ID: "vm1.sneat.dev", Name: "vm1.sneat.dev", Role: "upstream", Status: "offline",
	}}
	wantUpstreamText := "NAME           vm1.sneat.dev\n" +
		"ROLE           upstream\n" +
		"STATUS         offline\n" +
		"PEER ID        vm1.sneat.dev\n" +
		"NODE ID        -\n" +
		"LAST SEEN      -\n" +
		"RESET PENDING  false\n" +
		"SESSION        none\n" +
		"COUNTERS       rx_events=0 tx_events=0 rx_bytes=0 tx_bytes=0\n" +
		"ADMIN          false\n"
	var upstreamText bytes.Buffer
	writePeerDetail(&upstreamText, now, upstream)
	if upstreamText.String() != wantUpstreamText {
		t.Fatalf("golden upstream detail text mismatch:\ngot:  %q\nwant: %q", upstreamText.String(), wantUpstreamText)
	}
}
