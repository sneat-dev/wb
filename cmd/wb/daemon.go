package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/dashboard"
)

func newDaemonCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Serve WB's local API and operations dashboard",
	}
	command.AddCommand(newDaemonServeCmd())
	return command
}

func newDaemonServeCmd() *cobra.Command {
	var listenAddress string
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the read-only dashboard and API on a loopback address",
		Long: `Serve WB's embedded operations dashboard and versioned read-only API.

The listener is loopback-only. Publish it to registered machines through a
protected Cloudflare Tunnel or another authenticated reverse proxy; do not bind
the daemon directly to a public interface.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireLoopbackAddress(listenAddress); err != nil {
				return usageError(err.Error())
			}
			return serveDashboard(command, listenAddress)
		},
	}
	command.Flags().StringVar(&listenAddress, "listen", "127.0.0.1:8766", "loopback listen address")
	return command
}

func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --listen address %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen must use localhost or a loopback IP; publish it through an authenticated tunnel")
	}
	return nil
}

func serveDashboard(command *cobra.Command, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for WB daemon: %w", err)
	}
	server := &http.Server{
		Handler:           dashboard.NewHandler(dashboard.Options{ProjectsRoot: projectsRoot, Version: collectVersion().Version}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if _, err := fmt.Fprintf(command.OutOrStdout(), "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		_ = listener.Close()
		return err
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
