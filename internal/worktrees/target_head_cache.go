package worktrees

import (
	"context"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

func withTargetHeadCache(ctx context.Context) context.Context {
	return worktreelanding.WithTargetHeadCache(ctx)
}
