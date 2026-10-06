package cmdhooks

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/wbexec"
)

func fakeCommands() commands {
	return commands{
		runtime: shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "/projects", Filter: "acme"} }},
		git: GitOperations{
			Apply: func(hooks.ApplyOptions) (hooks.ApplyResult, error) {
				return hooks.ApplyResult{Report: hooks.CheckReport{RepoRoot: "/repository"}}, nil
			},
			Check: func(string, string, string, string) (hooks.CheckReport, error) {
				return hooks.CheckReport{RepoRoot: "/repository"}, nil
			},
			LocalRepos: func(string, string) ([]discover.Repo, error) {
				return []discover.Repo{{Org: "acme", Name: "app", Path: "/app"}}, nil
			},
			Run: func(hooks.RunOptions) (hooks.RunResult, error) { return hooks.RunResult{}, nil },
			Classify: func(io.Reader, string) (hooks.Classification, error) {
				return hooks.Classification{Tier: hooks.TierSkip, Reason: "private checkpoint"}, nil
			},
			LoadPolicy: func(string, string) (hooks.Policy, error) { return hooks.Policy{}, nil },
			ReadEvents: func(string) ([]hooks.Event, error) { return nil, nil }, Executable: func() string { return "/verified/wb" }, Now: func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }, Exit: func(int) {}},
		life: LifecycleOperations{
			Check:  func(string) (lifecyclehooks.CheckReport, error) { return lifecyclehooks.CheckReport{}, nil },
			Status: func(int) (lifecyclehooks.Status, error) { return lifecyclehooks.Status{}, nil },
			Resume: func() (lifecyclehooks.ResumeReport, error) { return lifecyclehooks.ResumeReport{}, nil },
			Retry:  func(string) (lifecyclehooks.Report, error) { return lifecyclehooks.Report{}, nil },
			GC:     func(lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) { return lifecyclehooks.GCReport{}, nil },
			Backfill: func(context.Context, string, string, bool) (lifecyclehooks.BackfillPlan, error) {
				return lifecyclehooks.BackfillPlan{}, nil
			},
			Drain: func(context.Context, DrainOptions) (lifecyclehooks.Report, error) {
				return lifecyclehooks.Report{}, nil
			}},
		agent: AgentOperations{
			Inspect:   func(agentguard.ToolCall, agentguard.Options) agentguard.Decision { return agentguard.Decision{} },
			OpenInput: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(`{}`)), nil }, Executable: func() string { return "/verified/wb" }, ResolveGovernor: func(s string) string { return s }, Home: func() (string, error) { return "/home", nil }, MergeSettings: func(string, string) ([]byte, bool, error) { return []byte("{\"hooks\":{}}\n"), true, nil }, WriteSettings: func(string, []byte) error { return nil }, Quote: wbexec.QuoteShellWord,
		},
	}
}

type failWriter struct {
	remaining int
	err       error
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return len(p), nil
}
