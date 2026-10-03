package cmdhooks

import (
	"context"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

// GitOperations are the existing hook service boundaries used by these commands.
type GitOperations struct {
	Apply      func(hooks.ApplyOptions) (hooks.ApplyResult, error)
	Check      func(string, string, string, string) (hooks.CheckReport, error)
	LocalRepos func(string, string) ([]discover.Repo, error)
	Run        func(hooks.RunOptions) (hooks.RunResult, error)
	Classify   func(io.Reader, string) (hooks.Classification, error)
	LoadPolicy func(string, string) (hooks.Policy, error)
	ReadEvents func(string) ([]hooks.Event, error)
	Executable func() string
	Now        func() time.Time
	Exit       func(int)
}

// DrainOptions transport only the hidden durable-worker command's arguments.
type DrainOptions struct {
	ConfigPath, StateDir, ReceiptPath string
	Parallel                          int
}

// LifecycleOperations keep mutable dispatcher instances below the CLI boundary.
type LifecycleOperations struct {
	Check    func(string) (lifecyclehooks.CheckReport, error)
	Status   func(int) (lifecyclehooks.Status, error)
	Resume   func() (lifecyclehooks.ResumeReport, error)
	Retry    func(string) (lifecyclehooks.Report, error)
	GC       func(lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error)
	Backfill func(context.Context, string, string, bool) (lifecyclehooks.BackfillPlan, error)
	Drain    func(context.Context, DrainOptions) (lifecyclehooks.Report, error)
}

// AgentOperations isolate protocol argument handling from actual policy and settings writes.
type AgentOperations struct {
	Inspect         func(agentguard.ToolCall, agentguard.Options) agentguard.Decision
	OpenInput       func(string) (io.ReadCloser, error)
	Executable      func() string
	ResolveGovernor func(string) string
	Home            func() (string, error)
	MergeSettings   func(string, string) ([]byte, bool, error)
	WriteSettings   func(string, []byte) error
	Quote           func(string) string
}
