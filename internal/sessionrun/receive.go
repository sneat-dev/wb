package sessionrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"path/filepath"
)

type ReceiveRequest struct {
	ProjectsRoot string
	Raw          []byte
}

func ConfiguredMachine() (string, error) {
	config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
	if err != nil {
		return "", err
	}
	return config.Machine, nil
}
func MoveStore(projectsRoot string) (sessionmove.Store, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return sessionmove.Store{}, err
	}
	return sessionmove.NewStore(filepath.Join(home, sessionmove.DirName)), nil
}
func ParkTargetStore(projectsRoot string) (sessionpark.TargetStore, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return sessionpark.TargetStore{}, err
	}
	return sessionpark.NewTargetStore(filepath.Join(home, sessionpark.TargetDirName)), nil
}

type ReceiveDependencies struct {
	LocalMachine func() (string, error)
	Store        func(string) (sessionmove.Store, error)
	Receive      func(context.Context, sessionreceive.Options) (sessionreceive.Result, error)
}

func DefaultReceiveDependencies() ReceiveDependencies {
	return ReceiveDependencies{LocalMachine: ConfiguredMachine, Store: MoveStore, Receive: sessionreceive.Receive}
}

type ReceiveService struct{ deps ReceiveDependencies }

func NewReceive(deps ReceiveDependencies) *ReceiveService { return &ReceiveService{deps: deps} }
func (s *ReceiveService) Receive(ctx context.Context, request ReceiveRequest) (sessionreceive.Result, error) {
	machine, err := s.deps.LocalMachine()
	if err != nil {
		return sessionreceive.Result{}, fmt.Errorf("load validated local remote.machine for session receive: %w", err)
	}
	store, err := s.deps.Store(request.ProjectsRoot)
	if err != nil {
		return sessionreceive.Result{}, err
	}
	return s.deps.Receive(ctx, sessionreceive.Options{Store: store, ProjectsRoot: request.ProjectsRoot, LocalMachine: machine, RawRequest: request.Raw})
}

type ReceiveParkDependencies struct {
	LocalMachine func() (string, error)
	Store        func(string) (sessionpark.TargetStore, error)
	Receive      func(context.Context, sessionparkreceive.Options) (sessionparkreceive.Result, error)
}

func DefaultReceiveParkDependencies() ReceiveParkDependencies {
	return ReceiveParkDependencies{LocalMachine: ConfiguredMachine, Store: ParkTargetStore, Receive: sessionparkreceive.Receive}
}

type ReceiveParkService struct{ deps ReceiveParkDependencies }

func NewReceivePark(deps ReceiveParkDependencies) *ReceiveParkService {
	return &ReceiveParkService{deps: deps}
}
func (s *ReceiveParkService) ReceivePark(ctx context.Context, request ReceiveRequest) (sessionparkreceive.Result, error) {
	machine, err := s.deps.LocalMachine()
	if err != nil {
		return sessionparkreceive.Result{}, fmt.Errorf("load validated local remote.machine for parked-session receive: %w", err)
	}
	store, err := s.deps.Store(request.ProjectsRoot)
	if err != nil {
		return sessionparkreceive.Result{}, err
	}
	return s.deps.Receive(ctx, sessionparkreceive.Options{Store: store, ProjectsRoot: request.ProjectsRoot, LocalMachine: machine, RawEnvelope: request.Raw})
}

type ReceiveMessageDependencies struct {
	LocalMachine func() (string, error)
	Store        func(string) (sessionmove.Store, error)
	Receive      func(context.Context, sessionmessage.Options) (sessionmessage.Result, error)
	Directory    func(string) (string, error)
}

func DefaultReceiveMessageDependencies() ReceiveMessageDependencies {
	return ReceiveMessageDependencies{LocalMachine: ConfiguredMachine, Store: MoveStore, Receive: sessionmessage.Receive, Directory: DirForRead}
}

type ReceiveMessageService struct{ deps ReceiveMessageDependencies }

func NewReceiveMessage(deps ReceiveMessageDependencies) *ReceiveMessageService {
	return &ReceiveMessageService{deps: deps}
}
func (s *ReceiveMessageService) ReceiveMessage(ctx context.Context, request ReceiveRequest) (sessionmessage.Result, error) {
	machine, err := s.deps.LocalMachine()
	if err != nil {
		return sessionmessage.Result{}, fmt.Errorf("load validated local remote.machine for message receiver: %w", err)
	}
	store, err := s.deps.Store(request.ProjectsRoot)
	if err != nil {
		return sessionmessage.Result{}, err
	}
	sessions, err := s.deps.Directory(request.ProjectsRoot)
	if err != nil {
		return sessionmessage.Result{}, err
	}
	return s.deps.Receive(ctx, sessionmessage.Options{Store: store, ProjectsRoot: request.ProjectsRoot, LocalMachine: machine, SessionDir: sessions, RawMessage: request.Raw})
}
