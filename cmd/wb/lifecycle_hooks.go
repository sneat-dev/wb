package main

import (
	"context"
	"io"

	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

func lifecycleCheckoutUpdated(out io.Writer) func(context.Context, orchestrate.CheckoutUpdate) {
	return landingcontext.CheckoutUpdated(out, lifecyclehooks.Dispatch)
}
