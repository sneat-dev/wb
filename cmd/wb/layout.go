package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdlayout"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/spf13/cobra"
)

func newLayoutCmd(inv *invocation) *cobra.Command {
	return cmdlayout.New(func() string { return inv.projectsRoot }, cmdlayout.Dependencies{
		Audit: layout.Audit, Clean: layout.Clean, Migrate: layout.Migrate,
		ExitError: func(code int, message string) error { return &exitError{code: code, message: message} },
	})
}
