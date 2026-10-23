//go:build e2e

package gitrepo

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// storageProvider uses repository-local identity so parallel fixtures never
// mutate the process environment shared by other tests.
func storageProvider(t *testing.T) *Provider {
	t.Helper()
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, t.TempDir(), "clone", "-q", origin, seed)
	gitIn(t, seed, "config", "user.name", "test")
	gitIn(t, seed, "config", "user.email", "test@example.invalid")
	testenv.ConfigureGitAutoMaintenanceOff(t, seed)
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, seed, "push", "-q", "origin", "main")
	p := machine(t, origin)
	if err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	gitIn(t, p.opts.ClonePath, "config", "user.name", "test")
	gitIn(t, p.opts.ClonePath, "config", "user.email", "test@example.invalid")
	testenv.ConfigureGitAutoMaintenanceOff(t, p.opts.ClonePath)
	return p
}

func TestE2ESnapshotAndClaimEncodingFailuresPreserveStore(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"publish", "stamp", "claim"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			p := storageProvider(t)
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			value := snap("alex", "mac", at)
			if _, err := p.Publish(context.Background(), value); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(p.opts.ClonePath, filepath.FromSlash(SnapshotPath("alex", "mac")))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			head := gitIn(t, p.opts.ClonePath, "rev-parse", "HEAD")
			failure := errors.New("storage codec refused value")
			p.encodeSnapshot = func(remotestate.Snapshot) ([]byte, error) { return nil, failure }
			p.encodeClaim = func(remotestate.Claim) ([]byte, error) { return nil, failure }
			switch operation {
			case "publish":
				if _, err := p.Publish(context.Background(), value); !errors.Is(err, failure) {
					t.Fatalf("publish=%v", err)
				}
			case "stamp":
				if got := p.stampOwnLastSeen("alex", "mac", at.Add(time.Hour)); got != "" {
					t.Fatalf("stamp=%q", got)
				}
			case "claim":
				if _, err := p.Claim(context.Background(), mkClaim("alex", "mac", "task", at), remotestate.ClaimForce, ""); !errors.Is(err, failure) {
					t.Fatalf("claim=%v", err)
				}
				if _, err := os.Stat(filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task")))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed claim published=%v", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("failed codec modified snapshot: %v", err)
			}
			if got := gitIn(t, p.opts.ClonePath, "rev-parse", "HEAD"); got != head {
				t.Fatalf("failed codec committed %s", got)
			}
		})
	}
}

func TestE2EReleaseToleratesClaimDisappearingBeforeRemoval(t *testing.T) {
	t.Parallel()
	p := storageProvider(t)
	if _, err := p.Claim(context.Background(), mkClaim("alex", "mac", "task", time.Now().UTC()), remotestate.ClaimForce, ""); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.removeClaim = func(path string) error {
		calls++
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.ErrNotExist
	}
	result, err := p.Release(context.Background(), "task", "alex", "mac", false)
	if err != nil || result.Kind != remotestate.Released || calls != 1 {
		t.Fatalf("release=%+v,%v calls=%d", result, err, calls)
	}
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task")))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim remains=%v", err)
	}
}

func TestE2ECloneLockReportsUnexpectedFlockFailure(t *testing.T) {
	t.Parallel()
	failure := syscall.EBADF
	calls := 0
	lock, err := acquireCloneLockWithFlock(filepath.Join(t.TempDir(), "clone"), time.Now, func(time.Duration) { t.Fatal("unexpected retry") }, func(_ int, flags int) error {
		calls++
		if flags != unix.LOCK_EX|unix.LOCK_NB {
			t.Fatalf("flock flags=%d", flags)
		}
		return failure
	})
	if lock != nil || !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("lock=%v,%v calls=%d", lock, err, calls)
	}
}
