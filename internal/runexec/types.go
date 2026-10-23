// Package runexec coordinates governed command operations without CLI policy.
package runexec

import (
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
	"time"
)

type ExecuteRequest struct {
	directory                string
	Argv                     []string
	ProjectsRoot, ConfigPath string
	AllowSaturatedHost       bool
	Stdin                    io.Reader
	Stdout, Stderr           io.Writer
	Observe                  func(QueueEvent)
}
type ExecuteResult struct {
	ExitCode    int
	ChildFailed bool
}
type ChangedRequest struct {
	Argv   []string
	Target string
}
type ChangedResult struct {
	Argv, Packages        []string
	Target, MergeBase     string
	MissingTarget, NoWork bool
}
type HistoryRequest struct{ Days int }
type HistoryResult struct {
	Summary runlog.Summary
	Path    string
	Days    int
}
type QueueRequest struct{ ProjectsRoot string }
type SubmitRequest struct {
	ProjectsRoot             string
	Argv                     []string
	WorkerID, IdempotencyKey string
	Stderr                   io.Writer
}
type Submission struct {
	WorkingDirectory         string
	Argv                     []string
	WorkerID, IdempotencyKey string
}
type QueueEventKind uint8

const (
	ImmediatelyAdmitted QueueEventKind = iota
	Queued
	WaitingHeartbeat
	AdmittedAfterWait
	Done
	CourtesyRunning
)

type QueueEvent struct {
	Kind            QueueEventKind
	Summary         string
	State           runqueue.State
	Waited, Elapsed time.Duration
	ExitCode        int
	At              time.Time
}
