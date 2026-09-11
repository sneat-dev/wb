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

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/web"
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
	closer       io.Closer
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

// StartLine is the single line the daemon prints on stderr when a hub is
// mounted, so an operator running `wb daemon serve` in a terminal sees where
// the dashboard is without opening the configuration.
func (mount *hubMount) StartLine() string {
	if mount == nil {
		return ""
	}
	return fmt.Sprintf("WB hub: engine=%s store=%s dashboard=%s", mount.Engine, mount.Store, mount.DashboardURL)
}

type fixedViewerResolver struct{ viewer hub.Viewer }

func (resolver fixedViewerResolver) Viewer(*http.Request) (hub.Viewer, error) {
	return resolver.viewer, nil
}

// mountHub builds the hub and the embedded dashboard when wb.yaml has a hub
// section. It returns (nil, nil) when the section is absent, which is the
// path every operator who does not self-host takes.
func mountHub(ctx context.Context, configPath, listenAddress string) (*hubMount, error) {
	cfg, found, err := hubconfig.Load(configPath)
	if err != nil || !found {
		return nil, err
	}
	store, closer, err := hubstore.Open(ctx, cfg.Store)
	if err != nil {
		return nil, err
	}
	mount, err := buildHubMount(ctx, cfg, store, configPath, listenAddress)
	if err != nil {
		_ = closer.Close()
		return nil, err
	}
	mount.closer = closer
	return mount, nil
}

func buildHubMount(ctx context.Context, cfg hubconfig.Config, store githubapp.DocumentStore, configPath, listenAddress string) (*hubMount, error) {
	machine, err := localMachineName(configPath)
	if err != nil {
		return nil, err
	}
	pepper, err := hubPepper(hubStateDirectory(cfg))
	if err != nil {
		return nil, err
	}
	credentials, resolver, snapshots := hub.NewMachineStores(store)
	_, bindings, entitlements, lifecycle := hub.NewInstallationStores(store)
	events, eventStatus := hub.NewRepositoryEventStore(store)

	viewer := hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: machine}
	enrollment := &hub.MachineEnrollmentService{Store: credentials, Pepper: pepper}
	snapshotService := &hub.MachineSnapshotService{Store: snapshots}

	handler := hub.NewHandler(hub.HandlerOptions{
		ViewerResolver: fixedViewerResolver{viewer: viewer},
		MachineBearer:  hub.NewMachineBearerResolver(resolver, pepper),
		Enrollment:     enrollment,
		Snapshots:      snapshotService,
		RepositoryEvents: &hub.RepositoryEventService{
			Snapshots: snapshots, Entitlements: entitlements, Lifecycle: lifecycle, Store: events,
		},
		Status: &hub.StatusService{
			Bindings: bindings, Snapshots: snapshots, Events: eventStatus, AppName: localIdentityID,
		},
		// Installations, Projection and WebhookSecret stay unset: every route
		// family they gate answers 503 until Task 3 wires the GitHub App.
		AllowedOrigin: "http://" + listenAddress,
	})

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, machine, pepper, listenAddress); err != nil {
		return nil, err
	}
	return &hubMount{
		Engine:       cfg.Store.Engine,
		Store:        cfg.Location(),
		Machine:      machine,
		DashboardURL: "http://" + listenAddress + web.MountPath + "dashboard/",
		Mounts: map[string]http.Handler{
			hub.APIPrefix + "/": handler,
			web.MountPath:       web.Handler(),
		},
	}, nil
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
