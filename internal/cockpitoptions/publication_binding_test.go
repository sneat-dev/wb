package cockpitoptions

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type bindingProvider struct {
	remotestate.Provider
	seen int
}

func (p *bindingProvider) Publish(context.Context, remotestate.Snapshot) (remotestate.PublishResult, error) {
	p.seen++
	return remotestate.PublishResult{Location: "private-report"}, nil
}
func TestOptionsLearnTheLoginThroughTheActualLazyPublisher(t *testing.T) {
	t.Parallel()
	config := cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop\n  publish:\n    interval: 5m\n")()
	deps := DefaultDependencies(config, testError)
	opened, logins := 0, 0
	provider := &bindingProvider{}
	deps.Publish.Login = func() (string, error) { logins++; return "alice", nil }
	deps.Publish.Open = func(remotestate.Config, string) (remotestate.Provider, error) { opened++; return provider, nil }
	deps.Publish.Now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	options := Options(Request{ProjectsRoot: t.TempDir(), Home: t.TempDir(), ConfigPath: config, Config: wbconfig.DefaultCockpitConfig(), Logs: io.Discard}, deps)
	if options.Publisher == nil || options.LoginSource() == "alice" || opened != 0 || logins != 0 {
		t.Fatalf("construction eagerly published/opened=%d/logins=%d", opened, logins)
	}
	options.Publisher.Publish(t.Context(), nil)
	if options.LoginSource() != "alice" || provider.seen != 1 || opened != 1 || logins != 1 {
		t.Fatalf("learned=%q writes=%d opened=%d logins=%d", options.LoginSource(), provider.seen, opened, logins)
	}
}
