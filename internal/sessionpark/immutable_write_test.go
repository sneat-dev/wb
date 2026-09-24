package sessionpark

import (
	"os"
	"strings"
	"testing"
)

func openParkTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

// TestWriteImmutableAtReportsCreateFailure drives writeImmutableAt's
// create-time error branch that is not EEXIST: a name over NAME_MAX makes
// the O_CREAT|O_EXCL open fail with ENAMETOOLONG.
func TestWriteImmutableAtReportsCreateFailure(t *testing.T) {
	t.Parallel()
	directory := openParkTestDirectory(t, t.TempDir())
	tooLong := strings.Repeat("a", 300)
	if _, err := writeImmutableAt(directory, tooLong, []byte("payload"), 0o600); err == nil {
		t.Fatal("writeImmutableAt accepted a name over NAME_MAX, want a create failure")
	}
}
