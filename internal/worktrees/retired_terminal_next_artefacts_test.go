package worktrees

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestRetiredTerminalNextPurgePreservesNativeFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before_seek", "before_read", "before_stat", "before_lock_unlink"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root, task, path := newTerminalArtefactTask(t)
			lock := filepath.Join(path, testRetiredLock)
			if err := os.WriteFile(lock, []byte("terminal lock bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			observed := false
			var native error
			got := purgeTerminalArtefactsObserved(root, task, func(at string, held *os.File, candidate PurgedArtefact) {
				if at != phase {
					return
				}
				observed = true
				switch phase {
				case "before_seek":
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
					_, native = held.Seek(0, 0)
				case "before_read":
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
					_, native = held.ReadDir(-1)
				case "before_stat":
					if err := os.Remove(candidate.Path); err != nil {
						t.Fatal(err)
					}
					native = unix.Fstatat(int(held.Fd()), filepath.Base(candidate.Path), &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW)
				case "before_lock_unlink":
					if err := os.Rename(candidate.Path, candidate.Path+".retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(candidate.Path, 0700); err != nil {
						t.Fatal(err)
					}
					native = unix.Unlinkat(int(held.Fd()), filepath.Base(candidate.Path), 0)
				}
				if native == nil {
					t.Fatal("actual owned operation did not refuse mutated evidence")
				}
			})
			if !observed || native == nil || len(got) != 0 {
				t.Fatalf("purge=%+v observed=%v native=%v", got, observed, native)
			}
			switch phase {
			case "before_stat":
				if _, err := os.Lstat(lock); !os.IsNotExist(err) {
					t.Fatalf("vanished lock recreated: %v", err)
				}
			case "before_lock_unlink":
				if info, err := os.Stat(lock); err != nil || !info.IsDir() {
					t.Fatalf("replacement removed: %v %v", info, err)
				}
				if raw, err := os.ReadFile(lock + ".retained"); err != nil || string(raw) != "terminal lock bytes" {
					t.Fatalf("old terminal bytes lost: %q %v", raw, err)
				}
			default:
				if raw, err := os.ReadFile(lock); err != nil || string(raw) != "terminal lock bytes" {
					t.Fatalf("closed authority removed lock: %q %v", raw, err)
				}
			}
		})
	}
}
