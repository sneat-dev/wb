package peersrun

import (
	"context"
	"net/http"
	"time"

	"github.com/sneat-dev/wb/internal/peers"
)

type RefusalKind int

const (
	Usage RefusalKind = iota
	Findings
)

type Refusal struct {
	Kind    RefusalKind
	Message string
}

func (e *Refusal) Error() string                     { return e.Message }
func refusal(kind RefusalKind, message string) error { return &Refusal{Kind: kind, Message: message} }

type TrustAction int

const (
	Block TrustAction = iota
	Unblock
)

type AdminOperations struct {
	Invite     func(context.Context, peers.InviteRequest) (peers.InviteResponse, error)
	Trust      func(context.Context, TrustAction, string) (peers.TrustResponse, error)
	Disconnect func(context.Context, string) (peers.DisconnectResponse, error)
}
type Dependencies struct {
	ConfigPath    func() string
	Admin         func(context.Context, string) (AdminOperations, error)
	ListenAddress func(string) (string, error)
	Do            func(*http.Request) (*http.Response, error)
	Abs           func(string) (string, error)
}
type JoinDependencies struct {
	ConfigPath         func() string
	Verify             func(context.Context, string, string) error
	Restart            func(context.Context, string) error
	EnsureNodeIdentity func(string) error
}
type Service struct {
	deps Dependencies
	join JoinDependencies
}

func New(deps Dependencies, join JoinDependencies) Service { return Service{deps: deps, join: join} }

type InviteRequest struct {
	Root, Name, TokenFile string
	Rotate                bool
}
type InviteOutput struct {
	PeerID    string    `json:"peer_id"`
	Name      string    `json:"name"`
	Token     string    `json:"token,omitempty"`
	TokenFile string    `json:"token_file,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Rotated   bool      `json:"rotated"`
}
type InviteResult struct {
	Output             InviteOutput
	AttemptedTokenFile string
	TokenWriteError    error
}
type JoinRequest struct {
	Root, HubURL, TokenFile   string
	TokenStdin, RestartDaemon bool
}
type JoinOutput struct {
	HubURL        string `json:"hub_url"`
	ConfigPath    string `json:"config_path"`
	TokenFile     string `json:"token_file"`
	Verified      bool   `json:"verified"`
	DaemonRestart bool   `json:"daemon_restart"`
}
type ListRequest struct{ Root string }
type ListResult struct {
	Response peers.ListResponse
	Finding  error
}
type GetRequest struct{ Root, Peer string }
type TrustRequest struct {
	Root, Peer string
	Action     TrustAction
}
type TrustResult struct {
	Response peers.TrustResponse
	Upstream bool
}
type DisconnectRequest struct{ Root, Peer string }
