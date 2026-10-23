package peersrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/credentialfile"
)

func readPeersJoinToken(in io.Reader, tokenFile string, tokenStdin bool) (string, error) {
	if tokenStdin {
		return credentialfile.ReadToken(in)
	}
	if !filepath.IsAbs(tokenFile) {
		return "", errors.New("--token-file must be an absolute path")
	}
	raw, err := os.ReadFile(tokenFile) //nolint:gosec // operator-supplied absolute path, read once at join time.
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token file must contain one non-empty token")
	}
	return token, nil
}
func hostForFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || strings.TrimSpace(parsed.Hostname()) == "" {
		return "hub"
	}
	return strings.ReplaceAll(parsed.Host, ":", "-")
}
func verifyPeerConnectProbe(ctx context.Context, hubURL, token string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(hubURL, "/")+hub.PeersConnectPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Timeout: 10 * time.Second,
		// The probe carries the peer's bearer token in a header a redirect
		// target would also receive: refuse every redirect outright rather
		// than silently following one to a host the operator never typed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("peer connect probe refuses to follow a redirect")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("connect probe returned %s", response.Status)
	}
	var body struct {
		SchemaVersion int    `json:"schema_version"`
		PeerID        string `json:"peer_id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&body); err != nil {
		return fmt.Errorf("decode connect probe response: %w", err)
	}
	if body.SchemaVersion != 1 || strings.TrimSpace(body.PeerID) == "" {
		return errors.New("connect probe response is missing schema_version or peer_id")
	}
	return nil
}
func Verify(ctx context.Context, hubURL, token string) error {
	return verifyPeerConnectProbe(ctx, hubURL, token)
}
