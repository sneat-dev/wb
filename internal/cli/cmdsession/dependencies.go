package cmdsession

import (
	"context"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/sessionrun"
)

type Dependencies struct {
	Register       func(context.Context, sessionrun.RegisterRequest) (session.Record, error)
	Join           func(context.Context, string) error
	List           func(context.Context, sessionrun.ListRequest, func(string)) ([]sessionrun.Row, error)
	Prune          func(context.Context, string) (int, error)
	Move           func(context.Context, sessionrun.MoveRequest, func([]secretscan.Finding)) (sessionrun.MoveResult, error)
	Park           func(context.Context, sessionrun.ParkRequest) (sessionrun.ParkResult, error)
	Resume         func(context.Context, sessionrun.ResumeRequest) (sessionrun.ResumeResult, error)
	Receive        func(context.Context, sessionrun.ReceiveRequest) (sessionreceive.Result, error)
	ReceivePark    func(context.Context, sessionrun.ReceiveRequest) (sessionparkreceive.Result, error)
	ReceiveMessage func(context.Context, sessionrun.ReceiveRequest) (sessionmessage.Result, error)
	Send           func(context.Context, sessionrun.MessageRequest) (sessionmessenger.Result, error)
}
