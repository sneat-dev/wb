package main

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/cmdcockpit"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/spf13/cobra"
	"net/http"
)

func newCockpitCmd(inv *invocation) *cobra.Command {
	return cmdcockpit.New(newCLIRuntime(inv), newCockpitDependencies())
}
func newCockpitDependencies() cmdcockpit.Dependencies {
	return cmdcockpit.Dependencies{
		Local: func(ctx context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
			return cockpitrun.NewLocalService(daemonruntime.DefaultDependencies(usageError), requestCockpitLogin).Local(ctx, request)
		},
		HostedURL: func() (string, error) {
			return loadHostedCockpitURL(wbconfig.DefaultPath())
		},
		Export: func(ctx context.Context, request cockpitrun.ExportRequest) cockpitrun.ExportResult {
			return cockpitrun.Export(ctx, cockpitrun.DefaultExportDependencies(), request)
		},
		Open: openBrowser, IsTerminal: console.IsTerminal,
	}
}
func requestCockpitLogin(ctx context.Context, client *http.Client) (cockpit.LoginCode, string, error) {
	var issued struct {
		Code string `json:"code"`
		Key  string `json:"key"`
		Path string `json:"path"`
	}
	err := (&peerAdminClient{httpClient: client}).call(ctx, cockpit.LoginCodeRPCPath, struct{}{}, &issued)
	return cockpit.LoginCode{Code: issued.Code, Key: issued.Key}, issued.Path, err
}

func loadHostedCockpitURL(path string) (string, error) {
	config, err := wbconfig.LoadCockpit(path)
	return config.HostedURL, err
}
