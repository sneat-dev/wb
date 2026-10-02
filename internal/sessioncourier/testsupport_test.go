package sessioncourier

import (
	"context"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

// Helpers kept for tests only: no production caller remains.

// DeliverSSH is the simple production entry point for one SSH delivery.
func DeliverSSH(ctx context.Context, config sessionmove.SSHConfig, raw []byte) (sessionreceive.Result, error) {
	deliverer, err := NewSSHDeliverer(config)
	if err != nil {
		return sessionreceive.Result{}, err
	}
	return deliverer.Deliver(ctx, raw)
}
