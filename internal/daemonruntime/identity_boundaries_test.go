//go:build !windows

package daemonruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestIdentityBoundariesKeepForeignHomeSeparateFromLiveness(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }
	deps.ProcessStartTime = func(int) (time.Time, bool) {
		t.Fatal("foreign-home identity must precede process-generation observation")
		return time.Time{}, false
	}
	controller := NewController(deps, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "private-fixture", SHA256: "private"}, "owner", deps.Now())
	foreignHome, err := wbhome.Root(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state.WBHome = foreignHome
	state.MarkReadyWithProcess(os.Getpid(), time.Now(), deps.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(controller.store.Path)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := controller.store.Load()
	if err != nil || !found {
		t.Fatalf("record found %v, error %v", found, err)
	}
	identity, detail, alive := controller.assessIdentity(record, found)
	if identity != identityForeignHome || !alive || !strings.Contains(detail, foreignHome) {
		t.Fatalf("identity %s, detail %q, alive %v", identity, detail, alive)
	}
	localHome, err := wbhome.Root(root)
	if err != nil || !strings.Contains(detail, localHome) {
		t.Fatalf("detail %q does not name local home %q: %v", detail, localHome, err)
	}
	after, err := os.ReadFile(controller.store.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("identity assessment changed the native record: %v", err)
	}
}

func TestIdentityBoundariesObserveRecordedGenerationThroughInstanceEffect(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []bool{false, true} {
		name := map[bool]string{false: "current generation", true: "recycled PID"}[mismatch]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			recorded := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			observed := recorded
			if mismatch {
				observed = recorded.Add(time.Hour)
			}
			calls := 0
			deps.Alive = func(pid int) bool { return pid == os.Getpid() }
			// This is an explicit policy-boundary effect, not native proof of a
			// recycled process. The existing Linux tests retain that authority.
			deps.ProcessStartTime = func(pid int) (time.Time, bool) {
				calls++
				if pid != os.Getpid() {
					t.Fatalf("generation observation PID %d, want fixture PID", pid)
				}
				return observed, true
			}
			controller := NewController(deps, root)
			state := daemonTestState(t, root, DefaultListen, daemon.Provenance{}, "owner", deps.Now())
			state.MarkReadyWithProcess(os.Getpid(), recorded, deps.Now())
			if err := controller.store.Save(state); err != nil {
				t.Fatal(err)
			}
			state, found, err := controller.store.Load()
			if err != nil || !found {
				t.Fatalf("generation record found %v, error %v", found, err)
			}
			identity, detail, alive := controller.assessIdentity(state, true)
			if !alive || calls != 1 {
				t.Fatalf("alive %v, generation reads %d", alive, calls)
			}
			if mismatch {
				if identity != identityProcessRecycled || !strings.Contains(detail, observed.Format(time.RFC3339)) {
					t.Fatalf("mismatch identity %s, detail %q", identity, detail)
				}
			} else if identity != identityCurrent || detail != "" {
				t.Fatalf("current identity %s, detail %q", identity, detail)
			}
		})
	}
}

func TestIdentityBoundariesNilObservationUsesActualCurrentProcess(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.ProcessStartTime = nil
	deps.Alive = func(pid int) bool { return pid == os.Getpid() }
	controller := NewController(deps, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{}, "owner", deps.Now())
	started, observed := daemon.ProcessStartTime(os.Getpid())
	state.MarkReadyWithProcess(os.Getpid(), started, deps.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	state, found, err := controller.store.Load()
	if err != nil || !found {
		t.Fatalf("current native record found %v, error %v", found, err)
	}
	identity, detail, alive := controller.assessIdentity(state, found)
	if identity != identityCurrent || !alive {
		t.Fatalf("native current process identity %s, detail %q, alive %v", identity, detail, alive)
	}
	if observed && !started.IsZero() {
		if detail != "" {
			t.Fatalf("known actual generation detail = %q", detail)
		}
	} else if !strings.Contains(detail, "cannot observe a process start time") {
		t.Fatalf("unknown actual platform detail = %q", detail)
	}
}

func TestIdentityBoundariesUnresolvableHomePrecedesProcessEffects(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "private-file")
	if err := os.WriteFile(blocker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(blocker, "projects")
	deps := daemonTestDependencies(t, t.TempDir())
	deps.Alive = func(int) bool { t.Fatal("invalid home observed a process"); return false }
	identity, detail, alive := NewController(deps, root).assessIdentity(daemon.State{PID: os.Getpid()}, true)
	if identity != identityUnrecorded || alive || detail == "" {
		t.Fatalf("invalid home identity %s, detail %q, alive %v", identity, detail, alive)
	}
	if data, err := os.ReadFile(blocker); err != nil || string(data) != "preserve" {
		t.Fatalf("invalid-home blocker changed: %q, %v", data, err)
	}
}

func TestIdentityBoundariesLegacyDetailsAndAbsentEndpoints(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := (LegacyEndpoint{RuntimeDir: root}).describe(); got != "the runtime directory "+root+" exists" {
		t.Fatalf("empty legacy detail = %q", got)
	}
	if got := inspectLegacyRuntimeDir(filepath.Join(root, "missing"), func(int) bool {
		t.Fatal("absent endpoint observed a process")
		return true
	}); got != (LegacyEndpoint{}) {
		t.Fatalf("absent legacy endpoint = %+v", got)
	}
	if daemonSocketAnswers("") {
		t.Fatal("an empty endpoint answered")
	}
	if dirs := daemonLegacyRuntimeDirs(""); len(dirs) != 0 {
		t.Fatalf("empty fixed legacy root manufactured directories: %v", dirs)
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestIdentityBoundariesDeduplicateNativeLegacyHomeAlias(t *testing.T) {
	// Real HOME is deliberately changed only in this serial private fixture.
	legacy := daemonLegacyFixture(t)
	root := daemonTestRoot(t)
	if err := os.Symlink(filepath.Dir(legacy), filepath.Join(root, ".wb")); err != nil {
		t.Fatal(err)
	}
	before := daemonTestDirectorySnapshot(t, filepath.Dir(legacy))
	dirs := daemonLegacyRuntimeDirs(root)
	if len(dirs) != 1 || !daemonSamePath(dirs[0], legacy) {
		t.Fatalf("same native home alias was not deduplicated: %v, want %s", dirs, legacy)
	}
	if after := daemonTestDirectorySnapshot(t, filepath.Dir(legacy)); before != after {
		t.Fatalf("legacy discovery changed private home:\nbefore %s\nafter %s", before, after)
	}
}

func TestIdentityBoundariesMalformedHealthAddressNeverDials(t *testing.T) {
	t.Parallel()
	for name, call := range map[string]func() error{
		"plain health": func() error { return daemonHealthy(context.Background(), "bad\x00address") },
		"owned health": func() error { return daemonOwnedHealthy(context.Background(), "bad\x00address", os.Getpid(), 1) },
		"hub health":   func() error { _, err := daemonHubHealth(context.Background(), "bad\x00address"); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "invalid control character") {
			t.Fatalf("%s malformed-address refusal = %v", name, err)
		}
	}
}
