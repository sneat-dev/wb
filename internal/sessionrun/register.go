package sessionrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionterminal"
	"os"
)

type RegisterRequest struct {
	ProjectsRoot string
	Record       session.Record
}
type RegisterDependencies struct {
	CurrentPID       func() int
	ParentPID        func() int
	RuntimeProcess   func(int, string) bool
	Directory        func(string) (string, error)
	Register         func(string, session.Record) (session.Record, error)
	NativeID         func(string) (string, error)
	TerminalIdentity func() (sessionterminal.Identity, error)
}

func DefaultRegisterDependencies() RegisterDependencies {
	return RegisterDependencies{CurrentPID: os.Getpid, ParentPID: os.Getppid, RuntimeProcess: session.IsRuntimeProcess, Directory: DirForWrite, Register: session.RegisterCaller, TerminalIdentity: observedRegisterTerminal, NativeID: func(runtime string) (string, error) { return session.CallerNativeID(runtime, os.LookupEnv) }}
}

type RegisterService struct{ deps RegisterDependencies }

func NewRegister(deps RegisterDependencies) *RegisterService { return &RegisterService{deps: deps} }
func (s *RegisterService) Register(_ context.Context, request RegisterRequest) (session.Record, error) {
	record := request.Record
	if record.TmuxName != "" {
		return session.Record{}, fmt.Errorf("legacy tmux declarations are read-only; register Herdr or a terminal-free chat")
	}
	if s.deps.TerminalIdentity != nil {
		observed, err := s.deps.TerminalIdentity()
		if err != nil {
			return session.Record{}, err
		}
		if !record.Terminal.IsZero() && !observed.IsZero() && record.Terminal != observed {
			return session.Record{}, fmt.Errorf("declared Herdr identity contradicts this invocation")
		}
		if !observed.IsZero() {
			record.Terminal = observed
		}
	}

	if record.PID == s.deps.CurrentPID() {
		return session.Record{}, fmt.Errorf("session PID %d is WB itself; register the live harness with --pid $PPID from its tool-call shell", record.PID)
	}
	if record.PID == s.deps.ParentPID() && !s.deps.RuntimeProcess(record.PID, record.Runtime) {
		return session.Record{}, fmt.Errorf("session PID %d is the intermediate shell; register the live harness with --pid $PPID from its tool-call shell", record.PID)
	}
	if record.Runtime == "codex" && s.deps.RuntimeProcess(record.PID, record.Runtime) && s.deps.NativeID != nil {
		native, err := s.deps.NativeID(record.Runtime)
		if err != nil {
			return session.Record{}, err
		}
		declared := record.NativeHarnessID
		if declared == "" {
			declared = record.AgentID
		}
		if native != "" && declared != "" && native != declared {
			return session.Record{}, fmt.Errorf("registration native ID contradicts the current chat")
		}
		if native != "" {
			record.NativeHarnessID = native
		}
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

func observedRegisterTerminal() (sessionterminal.Identity, error) {
	identity := herdr.ReadIdentity(os.LookupEnv)
	// A socket override alone addresses a server; it is not evidence that the
	// caller is inside one of its panes. Partial environment degrades to none.
	if identity.PaneID == "" || identity.WorkspaceID == "" || identity.SocketPath == "" {
		return sessionterminal.Identity{}, nil
	}
	terminal := sessionterminal.Identity{Kind: "herdr", Socket: identity.SocketPath, WorkspaceID: identity.WorkspaceID, PaneID: identity.PaneID}
	if err := terminal.Validate(); err != nil {
		return sessionterminal.Identity{}, err
	}
	return terminal, nil
}
