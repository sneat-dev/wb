package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"os"
)

func newSessionDependencies(inv *invocation) cmdsession.Dependencies {
	return cmdsession.Dependencies{
		Register: sessionrun.NewRegister(sessionrun.DefaultRegisterDependencies()).Register,
		Join: func(ctx context.Context, path string) error {
			service, err := collaborationFactory(inv)()
			if err != nil {
				return err
			}
			_, err = service.Join(ctx, path)
			return err
		},
		List:           sessionrun.NewList(sessionrun.DefaultListDependencies()).List,
		Prune:          sessionrun.NewPrune(sessionrun.DefaultPruneDependencies()).Prune,
		Move:           newSessionMoveService(inv).Move,
		Park:           sessionrun.NewPark(sessionrun.DefaultParkDependencies()).Park,
		Resume:         sessionrun.NewResume(sessionrun.DefaultResumeDependencies()).Resume,
		Receive:        sessionrun.NewReceive(sessionrun.DefaultReceiveDependencies()).Receive,
		ReceivePark:    sessionrun.NewReceivePark(sessionrun.DefaultReceiveParkDependencies()).ReceivePark,
		Send:           sessionrun.NewMessage(sessionrun.DefaultMessageDependencies()).Send,
		ReceiveMessage: sessionrun.NewReceiveMessage(sessionrun.DefaultReceiveMessageDependencies()).ReceiveMessage,
	}
}
func newSessionCmd(inv *invocation) *cobra.Command {
	return cmdsession.New(newCLIRuntime(inv), newSessionDependencies(inv))
}

func installSessionResolver(inv *invocation) {
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) {
		directory, err := sessionrun.DirForRead(inv.projectsRoot)
		if err != nil {
			return worktrees.AgentIdentity{}, false
		}
		record, ok := session.ResolveForProcess(directory, os.Getpid())
		if !ok {
			return worktrees.AgentIdentity{}, false
		}
		nativeID := record.NativeHarnessID
		if nativeID == "" {
			nativeID = record.AgentID
		}
		return worktrees.AgentIdentity{
			Runtime:     record.Runtime,
			AgentID:     nativeID,
			Model:       record.Model,
			PID:         record.PID,
			WBSessionID: record.WBSessionID,
			Registered:  true,
		}, true
	})
}
