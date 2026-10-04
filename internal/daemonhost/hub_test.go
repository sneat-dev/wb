package daemonhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func TestMountHubEnrolsTheLocalMachineExactlyOnce(t *testing.T) {
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	address := "127.0.0.1:8799"

	first, err := mountHub(ctx, configPath, address, narrate.Writer{}, nil)
	if err != nil || first == nil {
		t.Fatalf("mountHub = %v, %v", first, err)
	}
	defer func() { _ = first.Close() }()

	cfg, err := remotestate.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("remote section after enrolment: %v", err)
	}
	if cfg.Provider != "hub" || cfg.URL != "http://"+address || cfg.Machine != first.Machine {
		t.Fatalf("remote section = %+v", cfg)
	}
	wantToken := filepath.Join(filepath.Dir(configPath), "credentials", "hub-local-"+first.Machine+".token")
	if cfg.TokenFile != wantToken {
		t.Fatalf("token file = %q, want %q", cfg.TokenFile, wantToken)
	}
	info, err := os.Stat(wantToken)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file = %v, %v; want mode 0600", info, err)
	}
	token, err := os.ReadFile(wantToken)
	if err != nil {
		t.Fatal(err)
	}

	// The pepper is the thing that must survive: regenerating it would make
	// every stored digest unresolvable, so the second mount would silently
	// re-enrol.
	pepper, err := os.ReadFile(filepath.Join(hubStoreDirectoryFromConfig(t, configPath), "pepper"))
	if err != nil {
		t.Fatal(err)
	}

	// A second mount over the SAME store (memory is per-process, so the store
	// is reopened empty) must re-enrol; over a store that still holds the
	// credential it must not. Re-running mountHub here proves the detection
	// itself: the token file and remote section are unchanged, and the only
	// reason a new credential is minted is that the memory store lost it.
	second, err := mountHub(ctx, configPath, address, narrate.Writer{}, nil)
	if err != nil || second == nil {
		t.Fatalf("second mountHub = %v, %v", second, err)
	}
	defer func() { _ = second.Close() }()
	if second.Machine != first.Machine {
		t.Fatalf("machine name changed across restarts: %q then %q", first.Machine, second.Machine)
	}
	repeatedPepper, err := os.ReadFile(filepath.Join(hubStoreDirectoryFromConfig(t, configPath), "pepper"))
	if err != nil || !bytes.Equal(pepper, repeatedPepper) {
		t.Fatalf("pepper was regenerated across restarts")
	}
	newToken, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(token, newToken) {
		t.Fatal("the credential was reused even though the memory store lost it; enrolment is not actually verified")
	}
}

func TestEnsureLocalEnrollmentSkipsAnAlreadyResolvableCredential(t *testing.T) {
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	address := "127.0.0.1:8798"
	pepper := []byte(strings.Repeat("p", 32))

	backend, closer, err := hubstore.Open(ctx, hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	credentials, resolver, _ := hub.NewMachineStores(backend)
	enrollment := &hub.MachineEnrollmentService{Store: credentials, Pepper: pepper}
	viewer := hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: "laptop"}

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", pepper, address); err != nil {
		t.Fatalf("first enrolment: %v", err)
	}
	cfg, loadErr := remotestate.LoadConfig(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", pepper, address); err != nil {
		t.Fatalf("second enrolment: %v", err)
	}
	repeated, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(token, repeated) {
		t.Fatal("a resolvable credential was rotated on restart")
	}

	// A pepper change is the one thing that must force a fresh credential:
	// the stored digest can no longer be reproduced from the token on disk.
	if err := ensureLocalEnrollment(ctx, enrollment, resolver, viewer, configPath, "laptop", []byte(strings.Repeat("q", 32)), address); err != nil {
		t.Fatalf("enrolment after a pepper change: %v", err)
	}
	rotated, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(token, rotated) {
		t.Fatal("an unresolvable credential was kept")
	}
}

func TestMountHubDoesNothingWithoutAHubSection(t *testing.T) {
	mount, err := mountHub(context.Background(), filepath.Join(t.TempDir(), "absent.yaml"), "127.0.0.1:8797", narrate.Writer{}, nil)
	if err != nil || mount != nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	if mount.handlers() != nil || mount.StartLine() != "" || mount.Close() != nil {
		t.Fatal("a nil mount is not inert")
	}
}

func TestMountHubSurfacesAnInvalidSection(t *testing.T) {
	if _, err := mountHub(context.Background(), hubTestConfig(t, "hub:\n  store:\n    engine: postgres\n"), "127.0.0.1:8796", narrate.Writer{}, nil); err == nil {
		t.Fatal("an invalid hub section was mounted")
	}
	// An engine that cannot open is a mount failure, not a silent no-op.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mountHub(context.Background(), hubTestConfig(t, "hub:\n  store:\n    engine: ingitdb\n    path: "+blocked+"\n"), "127.0.0.1:8796", narrate.Writer{}, nil); err == nil {
		t.Fatal("an unopenable store was mounted")
	}
}

func TestHubPepperRefusesATruncatedFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "pepper"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hubPepper(directory); err == nil {
		t.Fatal("a truncated pepper was accepted")
	}
	if _, err := hubPepper(""); err == nil {
		t.Fatal("an unresolvable hub state directory was accepted")
	}
	// A regular file where the directory should be is the portable way to make
	// both the read and the MkdirAll fail.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hubPepper(filepath.Join(file, "nested")); err == nil {
		t.Fatal("an unusable hub state directory was accepted")
	}
}

func TestLocalMachineNamePrefersTheConfiguredName(t *testing.T) {
	configured := hubTestConfig(t, "remote:\n  provider: git\n  repo: sneat-dev/wb\n  machine: workhorse\n")
	if name, err := localMachineName(configured, os.Hostname); err != nil || name != "workhorse" {
		t.Fatalf("localMachineName = %q, %v", name, err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this machine")
	}
	host, _, _ = strings.Cut(host, ".")
	if name, err := localMachineName(filepath.Join(t.TempDir(), "absent.yaml"), os.Hostname); err != nil || name != host {
		t.Fatalf("localMachineName = %q, %v; want the hostname %q", name, err, host)
	}
}

func TestHubStateDirectoryFollowsTheConfiguredPath(t *testing.T) {
	if got := hubStateDirectory(hubConfigWithPath("/var/bench")); got != "/var/bench" {
		t.Fatalf("hubStateDirectory = %q", got)
	}
}

func TestFixedViewerResolverAlwaysReturnsTheLocalIdentity(t *testing.T) {
	resolver := fixedViewerResolver{viewer: hub.Viewer{Authenticated: true, IdentityID: localIdentityID, DisplayName: "laptop"}}
	viewer, err := resolver.Viewer(nil)
	if err != nil || !viewer.Authenticated || viewer.IdentityID != localIdentityID {
		t.Fatalf("viewer = %+v, %v", viewer, err)
	}
}

func TestEnsureLocalEnrollmentSurfacesAnEnrolmentFailure(t *testing.T) {
	// A service without a pepper cannot mint a credential, and the failure
	// must name the machine rather than leaving remote: half written.
	err := ensureLocalEnrollment(context.Background(), &hub.MachineEnrollmentService{}, unresolvableCredentials{},
		hub.Viewer{Authenticated: true, IdentityID: localIdentityID}, hubTestConfig(t, ""), "laptop", []byte(strings.Repeat("p", 32)), "127.0.0.1:8795")
	if err == nil || !strings.Contains(err.Error(), "laptop") {
		t.Fatalf("ensureLocalEnrollment = %v", err)
	}
}
