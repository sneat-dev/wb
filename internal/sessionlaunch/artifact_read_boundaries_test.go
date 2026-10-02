package sessionlaunch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestLaunchArtifactReadPreservesStatAndNativeReadFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"stat", "closed", "truncated"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			state, root := slCovOpenState(t)
			path := filepath.Join(slCovStateDir(root), "observed.json")
			slCovWrite(t, path, 0600, "exact bytes")
			stat := unix.Fstat
			if stage == "stat" {
				stat = func(fd int, value *unix.Stat_t) error { return syscall.EIO }
			}
			read := func(reader io.Reader) ([]byte, error) {
				if stage == "closed" {
					file := reader.(*io.LimitedReader).R.(*os.File)
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "truncated" {
					if err := os.Truncate(path, 0); err != nil {
						t.Fatal(err)
					}
				}
				return io.ReadAll(reader)
			}
			raw, err := readLaunchArtifactWithOperations(state.launch, "observed.json", stat, read)
			if err == nil || raw != nil {
				t.Fatalf("artifact = %q, %v", raw, err)
			}
			if stage == "stat" && !errors.Is(err, syscall.EIO) {
				t.Fatalf("stat cause = %v", err)
			}
			if stage == "closed" && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("read cause = %v", err)
			}
		})
	}
}

func TestPrivateArtifactReadRetainsSingleReadRefusal(t *testing.T) {
	t.Parallel()
	state, root := slCovOpenState(t)
	slCovWrite(t, filepath.Join(slCovStateDir(root), "private"), 0600, "exact bytes")
	var owned *os.File
	raw, err := readPrivateArtifactAtWithRead(state.launch, "private", 100, func(file *os.File, raw []byte) (int, error) {
		owned = file
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return file.Read(raw)
	})
	if err == nil || raw != nil {
		t.Fatalf("private read = %q, %v", raw, err)
	}
	if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("private reader retained: %v", err)
	}
}
