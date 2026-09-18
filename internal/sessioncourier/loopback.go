package sessioncourier

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

// LoopbackDeliverer runs session receive in-process for a successor on this
// machine. It never shells out to SSH.
type LoopbackDeliverer struct {
	LocalMachine string
	ProjectsRoot string
	Store        sessionmove.Store
	Receive      func(context.Context, sessionreceive.Options) (sessionreceive.Result, error)
}

func (d LoopbackDeliverer) Deliver(ctx context.Context, raw []byte) (sessionreceive.Result, error) {
	if d.LocalMachine == "" {
		return sessionreceive.Result{}, fmt.Errorf("loopback courier requires this machine's validated remote.machine")
	}
	receive := d.Receive
	if receive == nil {
		receive = sessionreceive.Receive
	}
	return receive(ctx, sessionreceive.Options{
		Store: d.Store, ProjectsRoot: d.ProjectsRoot, LocalMachine: d.LocalMachine, RawRequest: raw,
	})
}
