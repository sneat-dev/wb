package sessionmove

import (
	"os"
	"strings"
	"testing"
)

func openTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

// TestPublishImmutableAtRejectsNameThatCannotBeLinked drives the Linkat
// failure branch that is not EEXIST (store.go publishImmutableAt): a
// destination name over the filesystem's NAME_MAX makes the temporary
// file's publish Linkat fail with ENAMETOOLONG, distinct from the
// already-exercised EEXIST/idempotent-republish path.
func TestPublishImmutableAtRejectsNameThatCannotBeLinked(t *testing.T) {
	t.Parallel()
	directory := openTestDirectory(t, t.TempDir())
	tooLong := strings.Repeat("a", 300)
	if _, err := publishImmutableAt(directory, tooLong, []byte("payload"), 0o600); err == nil ||
		!strings.Contains(err.Error(), "publish immutable file") {
		t.Fatalf("publishImmutableAt(name over NAME_MAX) = %v, want a \"publish immutable file\" link error", err)
	}
}
