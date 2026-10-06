package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// localHubBearer is the Authorization value of the machine credential the
// daemon-hosted hub enrolled for its owner.
func localHubBearer(t *testing.T, configPath string) string {
	t.Helper()
	remote, err := remotestate.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(remote.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + strings.TrimSpace(string(raw))
}

// postToServedHub posts body to a hub route of a served daemon, with the
// Authorization value when there is one.
func postToServedHub(t *testing.T, address, path, body, authorization string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(answer)
}

// TestDaemonHostedHubWritesNeedTheOwnersCredential proves
// self-hosted-bench#ac:hub-writes-need-a-credential on a real daemon-hosted hub
// mount with the credential the hub enrolled for its owner: an anonymous local
// caller is refused on every write route and the store stays empty, both
// before and after the daemon binds Cockpit's owner check; the owner's machine
// bearer writes; so does a request the owner check accepts; and the anonymous
// metadata reads stay open throughout.
