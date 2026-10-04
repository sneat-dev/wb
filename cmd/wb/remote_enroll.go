package main

import (
	"context"

	"github.com/sneat-dev/wb/internal/remoterun"
)

// Peers join genuinely uses the same executable restart authority as enrollment.
func restartDaemonAfterRemoteEnroll(ctx context.Context, root string) error {
	return remoterun.RestartDaemonAfterEnroll(ctx, root)
}
