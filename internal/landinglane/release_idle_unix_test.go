//go:build unix

package landinglane

import (
	"errors"
	"strings"
	"testing"
)

func TestReleaseIdleReportsLockFailure(t *testing.T) {
	tailCovStubFlock(t, errors.New("flock failed"))
	if _, err := ReleaseIdle(t.TempDir(), "acme/app", "main", "owner"); err == nil || !strings.Contains(err.Error(), "lock landing lane") {
		t.Fatal(err)
	}
}
