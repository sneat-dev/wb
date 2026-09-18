// Package peers serves the one versioned peers read model
// peer-connectivity#req:peers-api requires, mounted identically in three
// places: the local daemon API (`GET /api/v1/peers`), the hub dashboard
// reads (`GET /v0/workbench/peers`), and — later, behind
// admin-requires-owner-credential — the admin write routes. This package
// owns only the reads; Task 1's CLI writes go through the owner RPC instead.
package peers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// SchemaVersion is the one version every response names, so a consumer can
// tell the shape it is decoding.
const SchemaVersion = 1

// Record is one peer as every mount reports it: the trust record, its
// derived status, and nothing that Task 2 or later tasks have not filled in
// yet. Node IDs are shown truncated to 8 characters; they are not secret.
type Record struct {
	SchemaVersion   int        `json:"schema_version"`
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	NodeID          string     `json:"node_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
	ResetPending    bool       `json:"reset_pending,omitempty"`
	WBVersion       string     `json:"wb_version,omitempty"`
	OS              string     `json:"os,omitempty"`
	Arch            string     `json:"arch,omitempty"`
	Protocol        int        `json:"protocol,omitempty"`
}

// Session is the live session a peer detail response carries. It is always
// nil until Task 2 adds sessions.
type Session struct {
	ConnectedAt   time.Time `json:"connected_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	RemoteAddress string    `json:"remote_address,omitempty"`
	Protocol      int       `json:"protocol,omitempty"`
}

// Counters is the traffic-and-event-counters shape, all zero until Task 6.
type Counters struct {
	RXPayloadBytes uint64 `json:"rx_payload_bytes"`
	TXPayloadBytes uint64 `json:"tx_payload_bytes"`
	RXMessages     uint64 `json:"rx_messages"`
	TXMessages     uint64 `json:"tx_messages"`
	RXEvents       uint64 `json:"rx_events"`
	TXEvents       uint64 `json:"tx_events"`
}

// Detail is the full `GET .../{id}` response: the record, the current
// session (nil for now), the counters (empty for now), and whether an admin
// session (Task 7) can act on this peer from wherever this response is read.
type Detail struct {
	Record
	Session        *Session `json:"session"`
	Counters       Counters `json:"counters"`
	AdminAvailable bool     `json:"admin_available"`
}

// ListResponse is the `GET .../peers` shape.
type ListResponse struct {
	SchemaVersion int      `json:"schema_version"`
	Peers         []Record `json:"peers"`
}

// Source is what NewHandler needs from its host. The hub side and (in a
// later task) the laptop's upstream side each implement it, so the same
// handler serves identical JSON everywhere it is mounted.
type Source interface {
	ListPeers(context.Context) ([]Record, error)
	GetPeer(ctx context.Context, idOrName string) (Detail, bool, error)
}

// Authorize is the viewer check NewHandler runs before ListPeers/GetPeer, so
// this read route carries the same discipline as every sibling dashboard
// read route instead of being the one exception. A nil error admits the
// request; every current caller wires one equivalent to the always-true
// loopback-operator viewer the sibling read APIs already use, so nothing
// behaves differently today — the hook exists so a future non-trivial
// viewer (a hosted deployment, or Task 7's admin session) has a real gate to
// plug into rather than this route staying structurally ungated.
type Authorize func(*http.Request) error

// NewHandler serves prefix (list) and prefix+"/{id}" (detail) as GET-only
// JSON routes. prefix is exactly what the mount point is (no trailing
// slash), so the same handler factory produces byte-identical JSON at
// /api/v1/peers and at /v0/workbench/peers. authorize may be nil, which
// disables the check (no current production caller does this).
func NewHandler(prefix string, source Source, authorize Authorize) http.Handler {
	prefix = strings.TrimSuffix(prefix, "/")
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if authorize != nil {
			if err := authorize(request); err != nil {
				writeError(writer, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		if source == nil {
			writeError(writer, http.StatusServiceUnavailable, "peers_unavailable")
			return
		}
		rest := strings.TrimPrefix(request.URL.Path, prefix)
		switch {
		case rest == "" || rest == "/":
			list, err := source.ListPeers(request.Context())
			if err != nil {
				writeError(writer, http.StatusServiceUnavailable, "peers_unavailable")
				return
			}
			writeJSON(writer, http.StatusOK, ListResponse{SchemaVersion: SchemaVersion, Peers: list})
		case strings.HasPrefix(rest, "/"):
			id := strings.TrimPrefix(rest, "/")
			if id == "" {
				writeError(writer, http.StatusNotFound, "peer_not_found")
				return
			}
			detail, found, err := source.GetPeer(request.Context(), id)
			if err != nil {
				writeError(writer, http.StatusServiceUnavailable, "peers_unavailable")
				return
			}
			if !found {
				writeError(writer, http.StatusNotFound, "peer_not_found")
				return
			}
			writeJSON(writer, http.StatusOK, detail)
		default:
			writeError(writer, http.StatusNotFound, "peer_not_found")
		}
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"error": code})
}
