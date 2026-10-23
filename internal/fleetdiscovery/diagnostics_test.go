package fleetdiscovery

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestRemoteFailureKeepsLocalDataAndDiagnosticPrecedence(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	resolver := New(&output)
	resolver.ScanLocal = func(string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "local", Local: true}}, nil
	}
	resolver.ListRemote = func(string) ([]discover.Repo, error) { return nil, errors.New("denied") }
	repos, err := resolver.Discover("projects", "", func() []string { return []string{"acme"} })
	if err != nil || len(repos) != 1 || repos[0].Slug() != "acme/local" || output.String() != "wb: could not list repos for acme, using local data only: denied\n" {
		t.Fatal(repos, err, output.String())
	}
	resolver.Diagnostics = diagnosticFailWriter{}
	if repos, err := resolver.Discover("projects", "", func() []string { return []string{"acme"} }); err != nil || len(repos) != 1 {
		t.Fatal(repos, err)
	}
	_ = io.Discard
	_ = strings.TrimSpace
}

type diagnosticFailWriter struct{}

func (diagnosticFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("diagnostic unavailable")
}
