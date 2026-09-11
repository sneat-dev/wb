package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/hub/poller"
	"github.com/sneat-dev/wb/hub/web"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// localIdentityID is the one identity a loopback hub knows. There is no
// sign-in because there is nothing to sign in to: only this machine can reach
// the listener, and requireLoopbackAddress refuses to bind anywhere else.
const localIdentityID = "local"

const hubPepperBytes = 32

// hubMount is what a self-hosted hub adds to the daemon's loopback listener.
// A zero value means "no hub section"; every field is then empty and the
// daemon serves exactly the routes it served before.
type hubMount struct {
	Engine       string
	Store        string
	Machine      string
	DashboardURL string
	Mounts       map[string]http.Handler
	// Poller is nil when hub.github.token_file is unset: without a token
	// there is nothing to ask GitHub with, so the hub serves the dashboard
	// and waits for webhooks instead.
	Poller *poller.Poller
	// Interval is the configured poll interval, reported by
	// `wb daemon status` whether or not polling is active.
	Interval time.Duration
	// Webhook is nil unless hub.github.app is configured.
	Webhook *webhookMode
	closer  io.Closer
	status  *hub.StatusService
	viewer  hub.Viewer
}

// hubTuning overrides the two values a whole-journey end-to-end test cannot
// live with: GitHub's origin, which a test replaces with a fake server, and
// the poll interval, whose 30s configuration floor is far longer than a test
// may run. Nothing outside a test sets it; production passes nil.
type hubTuning struct {
	APIBaseURL   string
	PollInterval time.Duration
}

// Close releases the store engine. Safe on a nil mount so callers can defer
// it unconditionally.
func (mount *hubMount) Close() error {
	if mount == nil || mount.closer == nil {
		return nil
	}
	return mount.closer.Close()
}

// handlers returns the extra mounts, or nil when there is no hub. A nil
// receiver is the no-hub case, so callers need no branch.
func (mount *hubMount) handlers() map[string]http.Handler {
	if mount == nil {
		return nil
	}
	return mount.Mounts
}

// health is what /api/v1/health reports about the hub, and therefore what
// `wb daemon status` in another process can see. A status read that fails is
// reported as "no delivery yet" rather than failing the health endpoint: an
// operator needs health most when something is already wrong.
func (mount *hubMount) health(ctx context.Context) dashboard.HubHealth {
	value := dashboard.HubHealth{Mounted: true, Polling: mount.Poller != nil, PollIntervalSeconds: mount.Interval.Seconds()}
	if mount.Poller != nil {
		value.RepositoriesPolled = mount.Poller.Repositories()
	}
	if mount.status == nil {
		return value
	}
	response, err := mount.status.Read(ctx, mount.viewer)
	if err != nil || response.Delivery == nil {
		return value
	}
	value.LastEventReceived = hubDeliveryMarker(response.Delivery.LastReceived)
	value.LastEventAcknowledged = hubDeliveryMarker(response.Delivery.LastAcknowledged)
	return value
}

// hubHealth returns the health hook the dashboard mounts, or nil when there
// is no hub, so the health payload keeps its exact previous shape.
func (mount *hubMount) hubHealth() func(context.Context) dashboard.HubHealth {
	if mount == nil {
		return nil
	}
	return mount.health
}

func hubDeliveryMarker(marker *hub.StatusDeliveryMarker) *dashboard.HubDeliveryMarker {
	if marker == nil {
		return nil
	}
	return &dashboard.HubDeliveryMarker{ID: marker.DeliveryID, Event: marker.Event, OccurredAt: marker.OccurredAt}
}

// startPolling runs the poller until ctx ends. It is a no-op without a token
// file, so callers need no branch.
func (mount *hubMount) startPolling(ctx context.Context) {
	if mount == nil || mount.Poller == nil {
		return
	}
	go func() { _ = mount.Poller.Run(ctx) }()
}

// StartLine is the single line the daemon prints on stderr when a hub is
// mounted, so an operator running `wb daemon serve` in a terminal sees where
// the dashboard is without opening the configuration.
func (mount *hubMount) StartLine() string {
	if mount == nil {
		return ""
	}
	return fmt.Sprintf("WB hub: engine=%s store=%s dashboard=%s%s", mount.Engine, mount.Store, mount.DashboardURL, mount.Webhook.StartSuffix())
}

type fixedViewerResolver struct{ viewer hub.Viewer }

func (resolver fixedViewerResolver) Viewer(*http.Request) (hub.Viewer, error) {
	return resolver.viewer, nil
}

// mountHub builds the hub and the embedded dashboard when wb.yaml has a hub
// section. It returns (nil, nil) when the section is absent, which is the
// path every operator who does not self-host takes.
func mountHub(ctx context.Context, configPath, listenAddress string, writer narrate.Writer, tuning *hubTuning) (*hubMount, error) {
	cfg, found, err := hubconfig.Load(configPath)
	if err != nil || !found {
		return nil, err
	}
	store, closer, err := hubstore.Open(ctx, cfg.Store)
	if err != nil {
		return nil, err
	}
	mount, err := buildHubMount(ctx, cfg, store, configPath, listenAddress, writer, tuning)
	if err != nil {
		_ = closer.Close()
		return nil, err
	}
	mount.closer = closer
	return mount, nil
}

func buildHubMount(ctx context.Context, cfg hubconfig.Config, store githubapp.DocumentStore, configPath, listenAddress string, writer narrate.Writer, tuning *hubTuning) (*hubMount, error) {
	machine, err := localMachineName(configPath)
	if err != nil {
		return nil, err
	}
	pepper, err := hubPepper(hubStateDirectory(cfg))
	if err != nil {
		return nil, err
	}
	credentials, resolver, snapshots := hub.NewMachineStores(store)
	states, bindings, _, lifecycle := hub.NewInstallationStores(store)
	events, eventStatus := hub.NewRepositoryEventStore(store)
	webhook, err := newWebhookMode(cfg, states, bindings, pepper)
	if err != nil {
		return nil, err
	}

	viewer := hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: machine}
	enrollment := &hub.MachineEnrollmentService{Store: credentials, Pepper: pepper}
	snapshotService := &hub.MachineSnapshotService{Store: snapshots}
	status := &hub.StatusService{Bindings: bindings, Snapshots: snapshots, Events: eventStatus, AppName: localIdentityID}

	handler := hub.NewHandler(hub.HandlerOptions{
		ViewerResolver: fixedViewerResolver{viewer: viewer},
		MachineBearer:  hub.NewMachineBearerResolver(resolver, pepper),
		Enrollment:     enrollment,
		Snapshots:      snapshotService,
		RepositoryEvents: &hub.RepositoryEventService{
			Snapshots: snapshots, Entitlements: localRepositoryEntitlements{identityID: localIdentityID}, Lifecycle: lifecycle, Store: events,
			Narrate: writer.Write,
		},
		Status:        status,
		Installations: webhook.Installations(),
		WebhookSecret: webhook.WebhookSecret(),
		// Projection stays unset: the hosted instance projects deliveries into
		// its own dashboard read model, which a loopback hub reads from the
		// same store the events are written to.
		AllowedOrigin: "http://" + listenAddress,
		Narrate:       writer.Write,
	})

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, machine, pepper, listenAddress); err != nil {
		return nil, err
	}
	return &hubMount{
		Engine:       cfg.Store.Engine,
		Store:        cfg.Location(),
		Machine:      machine,
		DashboardURL: "http://" + listenAddress + web.MountPath + "dashboard/",
		Poller:       newHubPoller(cfg, store, snapshots, events, machine, writer, tuning),
		Interval:     pollInterval(cfg, tuning),
		Webhook:      webhook,
		status:       status,
		viewer:       viewer,
		Mounts: map[string]http.Handler{
			hub.APIPrefix + "/": handler,
			web.MountPath:       web.Handler(),
		},
	}, nil
}

// newHubPoller builds the polling ingester, or returns nil when no token file
// is configured. Polling is the only ingestion a self-hoster gets by default,
// and a token is the only thing it needs.
func newHubPoller(cfg hubconfig.Config, store githubapp.DocumentStore, snapshots hub.MachineSnapshotStore, events hub.RepositoryEventStore, machine string, writer narrate.Writer, tuning *hubTuning) *poller.Poller {
	// Webhook mode replaces polling outright: the operator installed the App
	// on the repositories they care about, so GitHub pushes every default-
	// branch update and rename, and only the repository named by an event is
	// pulled. Polling alongside it would spend the shared per-user API budget
	// on repositories that already report themselves (founder, 2026-09-11).
	if cfg.GitHub.App != nil || strings.TrimSpace(cfg.GitHub.TokenFile) == "" {
		return nil
	}
	baseURL := ""
	if tuning != nil {
		baseURL = tuning.APIBaseURL
	}
	return poller.New(poller.Options{
		Client:       &http.Client{Timeout: 30 * time.Second},
		APIBaseURL:   baseURL,
		Token:        hubGitHubToken(cfg.GitHub.TokenFile),
		Snapshots:    snapshots,
		Events:       events,
		Observations: hub.NewPollObservationStore(store),
		Machine:      hub.Machine{ID: hub.MachineID(localIdentityID, machine), Name: machine, IdentityID: localIdentityID},
		Interval:     pollInterval(cfg, tuning),
		Narrate:      writer.Write,
	})
}

// pollInterval is the configured interval, or the test override when one is
// injected. The configuration floor is deliberately not applied to the
// override: it exists to protect GitHub's rate limit, and a test's fake
// GitHub has none.
func pollInterval(cfg hubconfig.Config, tuning *hubTuning) time.Duration {
	if tuning != nil && tuning.PollInterval > 0 {
		return tuning.PollInterval
	}
	return cfg.GitHub.PollInterval
}

// hubGitHubToken reads the operator's token from its file on every tick, so
// rotating the file takes effect without restarting the daemon. The token
// itself never leaves this closure: an error names the path, never the
// contents.
func hubGitHubToken(path string) func() (string, error) {
	return func() (string, error) {
		raw, err := os.ReadFile(path) //nolint:gosec // operator-owned private token path from their own configuration.
		if err != nil {
			return "", fmt.Errorf("read hub GitHub token file: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("hub GitHub token file %s is empty", path)
		}
		return token, nil
	}
}

// hubStateDirectory is where the pepper lives. It follows the inGitDB project
// directory when one is configured, so everything a self-hoster must back up
// sits in one place, and falls back to ~/.wb/hub for the engines that keep
// their data elsewhere.
func hubStateDirectory(cfg hubconfig.Config) string {
	if strings.TrimSpace(cfg.Store.Path) != "" {
		return cfg.Store.Path
	}
	return hubconfig.DefaultStorePath()
}

// hubPepper reads, or creates once, the 32 secret bytes that turn a machine
// token into the digest the credential store is keyed by. It must survive
// restarts: regenerating it would invalidate every token already written to
// disk, so an enrolled machine would silently stop authenticating.
func hubPepper(directory string) ([]byte, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("hub state directory could not be resolved; set hub.store.path")
	}
	path := filepath.Join(directory, "pepper")
	existing, err := os.ReadFile(path) //nolint:gosec // operator-owned private state path.
	switch {
	case err == nil:
		if len(existing) < hubPepperBytes {
			return nil, fmt.Errorf("hub pepper %s is shorter than %d bytes; delete it to re-enrol this machine", path, hubPepperBytes)
		}
		return existing, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read hub pepper: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create hub state directory: %w", err)
	}
	pepper := make([]byte, hubPepperBytes)
	if _, err := rand.Read(pepper); err != nil {
		return nil, fmt.Errorf("generate hub pepper: %w", err)
	}
	if err := os.WriteFile(path, pepper, 0o600); err != nil {
		return nil, fmt.Errorf("write hub pepper: %w", err)
	}
	return pepper, nil
}

// localMachineName reuses remote.machine when the operator already named this
// machine, so a hub they later point at the hosted instance keeps one name.
// Otherwise the hostname is the name they would have typed anyway.
func localMachineName(configPath string) (string, error) {
	if cfg, err := remotestate.LoadConfig(configPath); err == nil && strings.TrimSpace(cfg.Machine) != "" {
		return cfg.Machine, nil
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("hub machine name could not be resolved; set remote.machine in wb.yaml")
	}
	// A hostname may carry a domain or characters the enrollment contract
	// rejects; the first label is the name an operator recognises.
	host, _, _ = strings.Cut(host, ".")
	return host, nil
}

// ensureLocalEnrollment makes the daemon's own machine credential exist
// exactly once. It is idempotent across restarts by construction: the
// credential on disk is resolved through the same bearer path a request
// takes, and only a credential that fails to resolve is replaced.
func ensureLocalEnrollment(ctx context.Context, enrollment *hub.MachineEnrollmentService, resolver hub.MachineCredentialResolver, viewer hub.Viewer, configPath, machine string, pepper []byte, listenAddress string) error {
	hubURL := "http://" + listenAddress
	tokenFile := filepath.Join(filepath.Dir(configPath), "credentials", fmt.Sprintf("hub-local-%s.token", machine))
	if localEnrollmentIsCurrent(ctx, resolver, configPath, hubURL, machine, tokenFile, pepper) {
		return nil
	}
	response, err := enrollment.Enroll(ctx, viewer, hub.MachineEnrollmentRequest{Name: machine})
	if err != nil {
		return fmt.Errorf("enrol %s against the local hub: %w", machine, err)
	}
	// A rotation writes a token the existing file does not hold, so the
	// stale file has to go before the create-exclusive write.
	if err := os.Remove(tokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace local hub credential: %w", err)
	}
	if _, err := writePrivateCredential(tokenFile, response.Token); err != nil {
		return err
	}
	return wbconfig.SetRemoteHub(configPath, hubURL, machine, tokenFile)
}

// localEnrollmentIsCurrent reports whether remote: already points at this
// loopback hub with a credential the store still recognises.
func localEnrollmentIsCurrent(ctx context.Context, resolver hub.MachineCredentialResolver, configPath, hubURL, machine, tokenFile string, pepper []byte) bool {
	cfg, err := remotestate.LoadConfig(configPath)
	if err != nil || cfg.Provider != "hub" || cfg.URL != hubURL || cfg.Machine != machine || cfg.TokenFile != tokenFile {
		return false
	}
	raw, err := os.ReadFile(tokenFile) //nolint:gosec // path is derived from the operator's own configuration directory.
	if err != nil {
		return false
	}
	digest, err := hub.DigestMachineToken(strings.TrimSpace(string(raw)), pepper)
	if err != nil {
		return false
	}
	binding, err := resolver.ResolveMachineCredential(ctx, digest)
	return err == nil && binding.MachineName == machine && binding.IdentityID == localIdentityID
}
