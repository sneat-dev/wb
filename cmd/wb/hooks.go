package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/claudesettings"
	"github.com/sneat-dev/wb/internal/cli/cmdhooks"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/wbexec"
	"github.com/spf13/cobra"
)

func newHooksCmd(inv *invocation) *cobra.Command {
	return cmdhooks.New(newCLIRuntime(inv), cmdhooks.GitOperations{
		Apply: hooks.Apply, Check: hooks.Check, LocalRepos: hooks.LocalRepos, Run: hooks.Run,
		Classify: hooks.ClassifyPendingPush, LoadPolicy: hooks.LoadPolicy, ReadEvents: hooks.ReadEvents,
		Executable: hookExecutable, Now: time.Now, Exit: os.Exit,
	}, cmdhooks.LifecycleOperations{
		Check: func(config string) (lifecyclehooks.CheckReport, error) {
			d := lifecyclehooks.DefaultDispatcher()
			if config != "" {
				d.ConfigPath = config
			}
			return d.Check()
		},
		Status: func(limit int) (lifecyclehooks.Status, error) {
			return lifecyclehooks.DefaultDispatcher().Status(limit)
		},
		Resume: func() (lifecyclehooks.ResumeReport, error) { return lifecyclehooks.DefaultDispatcher().Resume() },
		Retry:  func(id string) (lifecyclehooks.Report, error) { return lifecyclehooks.DefaultDispatcher().Retry(id) },
		GC: func(options lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) {
			return lifecyclehooks.DefaultDispatcher().GC(options)
		},
		Backfill: func(ctx context.Context, root, filter string, apply bool) (lifecyclehooks.BackfillPlan, error) {
			return lifecyclehooks.Backfill(ctx, root, filter, lifecyclehooks.DefaultDispatcher(), apply)
		},
		Drain: func(ctx context.Context, options cmdhooks.DrainOptions) (lifecyclehooks.Report, error) {
			d := lifecyclehooks.DefaultDispatcher()
			d.ConfigPath = options.ConfigPath
			d.StateDir = options.StateDir
			d.ReceiptPath = options.ReceiptPath
			return d.Drain(ctx, options.Parallel)
		},
	}, cmdhooks.AgentOperations{
		Inspect: agentguard.Inspect, OpenInput: func(path string) (io.ReadCloser, error) { return os.Open(path) },
		Executable: hookExecutable, ResolveGovernor: wbexec.ResolveGovernorExecutable, Home: os.UserHomeDir,
		MergeSettings: claudesettings.MergeAgentHook, WriteSettings: claudesettings.WriteAtomically, Quote: shellQuote,
	})
}

// These adapters bind the native hook executable and shell quoting authorities.
func hookExecutable() string         { return wbexec.HookExecutable() }
func shellQuote(value string) string { return wbexec.QuoteShellWord(value) }
