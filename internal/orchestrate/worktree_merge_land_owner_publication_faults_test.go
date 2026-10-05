//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func landOwnerPrivateProviderRefusal(t *testing.T, pattern string) string {
	t.Helper()
	path := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "provider-refusal")
	stage := "case \"$*\" in\n " + pattern + ") printf observed > " + strconv.Quote(marker) + "; echo 'controlled late provider refusal' >&2; exit 1 ;;\nesac\n"
	modified := strings.Replace(string(body), "set -eu\n", "set -eu\n"+stage, 1)
	if modified == string(body) {
		t.Fatal("owned scripted provider has no exact insertion point")
	}
	if err := testenv.WriteExecutableFile(path, []byte(modified), 0755); err != nil {
		t.Fatal(err)
	}
	return marker
}
