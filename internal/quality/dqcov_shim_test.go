package quality

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// dqCovFakeGo installs a deterministic POSIX shell `go` shim on PATH for the
// calling test. It answers the three command shapes this package launches
// without touching a real Go toolchain or compiling anything:
//
//   - `go list -f {{.ImportPath}} <pattern>` prints $DQCOV_GO_LIST_MAIN when the
//     pattern is "./..." and $DQCOV_GO_LIST_OTHER otherwise;
//   - `go test <pkg> -list ...` prints $DQCOV_GO_TEST_LIST;
//   - `go test ... -coverprofile=<path>` writes a merged-profile fixture to the
//     requested profile path when $DQCOV_GO_WRITE_PROFILE is set, using
//     $DQCOV_GO_PROFILE_SHARD2 for any profile whose name contains "shard-2".
//
// Failure modes are selected by environment variable so each test asserts one
// branch: $DQCOV_GO_LIST_FAIL, $DQCOV_GO_DISCOVER_FAIL, $DQCOV_GO_TEST_FAIL
// (with $DQCOV_GO_TEST_FAIL_OUT) and $DQCOV_GO_SLEEP.
//
// It is skipped on Windows because it is a shell script; the package's other
// subprocess fakes use the same convention.
func dqCovFakeGo(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "go")
	script := `#!/bin/sh
mode=run
profile=""
last=""
for a in "$@"; do
  last="$a"
  case "$a" in
    -list) mode=list ;;
    -coverprofile=*) profile="${a#-coverprofile=}" ;;
  esac
done
case "$1" in
  list)
    if [ -n "$DQCOV_GO_LIST_STDERR" ]; then printf '%s\n' "$DQCOV_GO_LIST_STDERR" >&2; fi
    if [ -n "$DQCOV_GO_LIST_FAIL" ]; then exit 1; fi
    if [ "$last" = "./..." ]; then printf '%s\n' "$DQCOV_GO_LIST_MAIN"; else printf '%s\n' "$DQCOV_GO_LIST_OTHER"; fi
    exit 0
    ;;
  test)
    if [ "$mode" = list ]; then
      if [ -n "$DQCOV_GO_DISCOVER_FAIL" ]; then printf '%s\n' "$DQCOV_GO_DISCOVER_FAIL" >&2; exit 1; fi
      printf '%s\n' "$DQCOV_GO_TEST_LIST"
      exit 0
    fi
    if [ -n "$DQCOV_GO_SLEEP" ]; then sleep "$DQCOV_GO_SLEEP"; fi
    if [ -n "$DQCOV_GO_TEST_FAIL" ]; then printf '%s' "$DQCOV_GO_TEST_FAIL_OUT"; exit 1; fi
    if [ -n "$profile" ] && [ -n "$DQCOV_GO_WRITE_PROFILE" ]; then
      body="mode: set
pkg/a.go:1.1,2.2 2 1"
      case "$profile" in
        *shard-2*) if [ -n "$DQCOV_GO_PROFILE_SHARD2" ]; then body="$DQCOV_GO_PROFILE_SHARD2"; fi ;;
      esac
      printf '%s\n' "$body" > "$profile"
    fi
    printf '%s' "$DQCOV_GO_TEST_OUT"
    exit 0
    ;;
esac
exit 0
`
	if err := testenv.WriteExecutableFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return shim
}

// dqCovChmod is a helper that changes a path mode and restores it before the
// enclosing t.TempDir cleanup runs, so a deliberately unreadable fixture never
// breaks temporary-directory removal.
func dqCovChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}
