package main

import (
	"context"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

func lifecycleCheckoutUpdated(errOut io.Writer) func(context.Context, orchestrate.CheckoutUpdate) {
	return func(ctx context.Context, update orchestrate.CheckoutUpdate) {
		repository, err := lifecyclehooks.RepositoryIdentity(update.Checkout)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, "warning: lifecycle hooks were not dispatched:", fmt.Errorf("identify updated checkout: %w", err))
			return
		}
		report, err := lifecyclehooks.Dispatch(ctx, []lifecyclehooks.Event{{
			Name: lifecyclehooks.EventCheckoutUpdated, Repository: repository,
			Checkout: update.Checkout, OldSHA: update.OldSHA, NewSHA: update.NewSHA, Cause: update.Cause,
		}})
		for _, warning := range report.Warnings {
			_, _ = fmt.Fprintln(errOut, "warning:", warning)
		}
		if err != nil {
			_, _ = fmt.Fprintln(errOut, "warning: lifecycle hooks were not dispatched:", err)
		}
	}
}
