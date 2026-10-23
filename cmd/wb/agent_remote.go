package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
)

func newAgentService() *agentrun.Service {
	deps := agentrun.DefaultDependencies()
	deps.BeforeCreate = func(root string, repos []string) error {
		return checkoutsetup.BeforeCreate(root, repos, checkoutsetup.DefaultHookDependencies(hookExecutable))
	}
	deps.AfterCreate = func(root string, stderr io.Writer, base string, results []worktrees.CreateResult) {
		checkoutsetup.AfterCreate(checkoutmarker.DescribeOptions{ProjectsRoot: root, BaseBranch: base, Version: "wb " + buildinfo.Version()}, stderr, results, checkoutsetup.DefaultMarkerDependencies())
	}
	return agentrun.New(deps)
}

// RunAgentRemote is the private SSH entry; its project defaults precede Cobra parsing.
func RunAgentRemote(inv *invocation, stdin io.Reader, stdout, stderr io.Writer) int {
	if inv.projectsRoot == "" {
		inv.projectsRoot = defaultProjectsRoot()
	}
	return agentrun.Serve(context.Background(), newAgentService().Operations(), inv.projectsRoot, stdin, stdout, stderr)
}
