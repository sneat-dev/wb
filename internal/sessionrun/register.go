package sessionrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"os"
)

type RegisterRequest struct {
	ProjectsRoot string
	Record       session.Record
}
type RegisterDependencies struct {
	CurrentPID     func() int
	ParentPID      func() int
	RuntimeProcess func(int, string) bool
	Directory      func(string) (string, error)
	Register       func(string, session.Record) (session.Record, error)
}

func DefaultRegisterDependencies() RegisterDependencies {
	return RegisterDependencies{CurrentPID: os.Getpid, ParentPID: os.Getppid, RuntimeProcess: session.IsRuntimeProcess, Directory: DirForWrite, Register: session.Register}
}

type RegisterService struct{ deps RegisterDependencies }

func NewRegister(deps RegisterDependencies) *RegisterService { return &RegisterService{deps: deps} }
func (s *RegisterService) Register(_ context.Context, request RegisterRequest) (session.Record, error) {
	record := request.Record
	if record.PID == s.deps.CurrentPID() {
		return session.Record{}, fmt.Errorf("session PID %d is WB itself; register the live harness with --pid $PPID from its tool-call shell", record.PID)
	}
	if record.PID == s.deps.ParentPID() && !s.deps.RuntimeProcess(record.PID, record.Runtime) {
		return session.Record{}, fmt.Errorf("session PID %d is the intermediate shell; register the live harness with --pid $PPID from its tool-call shell", record.PID)
	}
	directory, err := s.deps.Directory(request.ProjectsRoot)
	if err != nil {
		return session.Record{}, err
	}
	return s.deps.Register(directory, record)
}

type PruneDependencies struct {
	Directory func(string) (string, error)
	Prune     func(string) (int, error)
}

func DefaultPruneDependencies() PruneDependencies {
	return PruneDependencies{Directory: DirForWrite, Prune: session.Prune}
}

type PruneService struct{ deps PruneDependencies }

func NewPrune(deps PruneDependencies) *PruneService { return &PruneService{deps: deps} }
func (s *PruneService) Prune(_ context.Context, projectsRoot string) (int, error) {
	directory, err := s.deps.Directory(projectsRoot)
	if err != nil {
		return 0, err
	}
	return s.deps.Prune(directory)
}
