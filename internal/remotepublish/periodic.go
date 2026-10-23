package remotepublish

import (
	"context"
	"errors"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
)

// periodicScanWorkers bounds the repositories one periodic publish scans at
// once; the daemon shares the machine with the work it reports on, so it reads
// gently where `wb remote publish` reads eight at a time.
const periodicScanWorkers = 2

// newPeriodicPublisher is the daemon's periodic remote publisher for a machine
// whose remote section sets publish.interval (cockpit-views#req:periodic-
// remote-publish): the same collector, identity and provider as `wb remote
// publish`, wrapped in the interval, change and backoff policy of package
// periodic. The GitHub login keying this machine's entry is resolved on the
// first attempt that needs it and kept, and learned, when it is not nil, is told
// it then.
func (service *Service) Periodic(cfg remotestate.Config, projectsRoot string, logf func(string, ...any), learned func(login string)) *periodic.Publisher {
	deps := service.deps
	var login string
	// What a scan read of a clone is kept while the clone's fingerprint stays the
	// same, and no longer than the keepalive.
	scans := &repositoryScans{read: deps.ReadRepository, fingerprint: deps.Fingerprint, maxAge: max(periodic.DefaultKeepalive, cfg.Publish.PublishEvery())}
	if scans.read == nil {
		scans.read = readRepositoryWithGit
	}
	return periodic.New(periodic.Options{
		Every: cfg.Publish.PublishEvery(), Agents: cfg.Publish.Agents, Metrics: cfg.Publish.Metrics, Logf: logf, Now: deps.Now,
		Published:  func() { notePeriodicHardware(deps.ConfigPath, logf) },
		OldestRead: scans.oldest,
		Collect: func(ctx context.Context, now time.Time) (remotestate.Snapshot, error) {
			if login == "" {
				found, err := deps.Login()
				if err != nil || found == "" {
					return remotestate.Snapshot{}, errors.New("the GitHub login keying this machine's entry is unavailable (gh auth status)")
				}
				login = found
				if learned != nil {
					learned(login)
				}
			}
			return service.collectSnapshot(ctx, projectsRoot, "", periodicScanWorkers, service.publishIdentity(cfg, login, now), cfg.Publish.Unpushed, Progress{}, scans)
		},
		Open: func() (remotestate.Provider, error) { return deps.Open(cfg, projectsRoot) },
	})
}
