// Package workerrun coordinates sandbox-inherited worker leases and execution.
package workerrun

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/runenv"
	"github.com/sneat-dev/wb/internal/runqueue"
)

type ConnectRequest struct {
	ProjectsRoot, WorkerID string
	Roots                  []string
	CPUCapacity            uint32
}
type Dependencies struct {
	Client   func(context.Context, string, io.Writer) (daemonv1connect.DaemonServiceClient, error)
	Snapshot func() buildinfo.Report
}
type Service struct {
	deps Dependencies
	ops  operations
}

func New(deps Dependencies) Service { return Service{deps: deps, ops: defaultOperations()} }

type operations struct {
	After            func(time.Duration) <-chan time.Time
	Ticker           func(time.Duration) (<-chan time.Time, func())
	PID              func() int
	PermitsDirectory func([]string, string) (bool, error)
	AdmitExplicit    func(context.Context, string, int, runqueue.Participant) (runqueue.Admission, error)
	Admit            func(context.Context, string, []string, runqueue.Participant, *runqueue.Ticket) (runqueue.Admission, error)
	Release          func(*runqueue.Lease)
	Run              func(context.Context, *daemonv1.WorkerAssignment, int, io.Writer, io.Writer) error
}

func defaultOperations() operations {
	return operations{
		After: time.After, Ticker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		PID: os.Getpid, PermitsDirectory: workerPermitsDirectory, AdmitExplicit: runqueue.AdmitExplicit, Admit: runqueue.Admit,
		Release: func(lease *runqueue.Lease) { lease.Release() }, Run: runAssignment,
	}
}
func runAssignment(ctx context.Context, assignment *daemonv1.WorkerAssignment, units int, stdout, stderr io.Writer) error {
	child := process.CommandContext(ctx, assignment.Argv[0], assignment.Argv[1:]...)
	child.Dir = assignment.WorkingDirectory
	child.Env = runenv.Worker(os.Environ(), assignment.Argv, assignment.OperationId, units, runqueue.EffectiveGOFLAGS())
	child.Stdout, child.Stderr = stdout, stderr
	return child.Run()
}
