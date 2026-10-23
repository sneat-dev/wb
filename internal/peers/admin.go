package peers

import "time"

const RPCPrefix = "/wb.peers.v1/"

type InviteRequest struct {
	Name   string `json:"name"`
	Rotate bool   `json:"rotate"`
}
type InviteResponse struct {
	PeerID    string    `json:"peer_id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	Rotated   bool      `json:"rotated"`
}
type NameOrIDRequest struct {
	Peer string `json:"peer"`
}
type TrustResponse struct {
	PeerID         string    `json:"peer_id"`
	Name           string    `json:"name"`
	Trust          string    `json:"trust"`
	ResetPending   bool      `json:"reset_pending"`
	TrustChangedAt time.Time `json:"trust_changed_at"`
}
type DisconnectResponse struct {
	PeerID       string `json:"peer_id"`
	Name         string `json:"name"`
	Disconnected bool   `json:"disconnected"`
	Message      string `json:"message"`
}
