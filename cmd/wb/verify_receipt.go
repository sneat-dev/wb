package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/graduation"
	"github.com/spf13/cobra"
)

func newVerifyReceiptCmd() *cobra.Command {
	observer := graduation.DefaultObserver()
	return cmdworktree.NewReceipt(cmdworktree.ReceiptDependencies{Compose: observer.ComposeFiles, Observe: observer.ObserveRemoteTarget})
}
