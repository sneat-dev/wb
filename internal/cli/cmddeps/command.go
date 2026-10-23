package cmddeps

import (
	"context"
	"io"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Campaign    func(io.Writer, bool, string) *cliprogress.Campaign
	Select      func(context.Context, depsrun.Selection) ([]deps.Repository, error)
	Graph       func(context.Context, depsrun.GraphRequest) (depsrun.GraphResult, error)
	Drift       func(context.Context, depsrun.DriftRequest) (deps.DriftReport, error)
	Peers       func(context.Context, deps.PeerOptions) (deps.PeerReport, error)
	Set         func(context.Context, depsrun.SetRequest) (depsrun.SetResult, error)
	Bump        func(context.Context, depsrun.BumpRequest) (depsrun.BumpResult, error)
	Seed        func(context.Context, depsrun.SeedRequest) (depsrun.SeedResult, error)
	OpenBrowser func(string) error
}

func Operations(service *depsrun.Service, openBrowser func(string) error) Dependencies {
	return Dependencies{Campaign: cliprogress.NewCampaign, Select: service.Select, Graph: service.Graph, Drift: service.Drift, Peers: service.Peers, Set: service.Set, Bump: service.Bump, Seed: service.Seed, OpenBrowser: openBrowser}
}
func New(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "deps", Aliases: []string{"dep"}, Short: "Inspect and coordinate dependencies across repositories"}
	command.AddCommand(newSet(runtime, operations), newBump(runtime, operations), newGraph(runtime, operations), newDrift(runtime, operations), newPeers(runtime, operations))
	return command
}
