package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// peersJoinDeps are the seams tests replace: config location, verification,
// and the daemon restart.
type peersJoinDeps struct {
	configPath func() string
	verify     func(ctx context.Context, hubURL, token string) error
	restart    func(ctx context.Context, projectsRoot string) error
}

func defaultPeersJoinDeps(inv *invocation) peersJoinDeps {
	return peersJoinDeps{
		configPath: wbconfig.DefaultPath,
		verify:     verifyPeerConnectProbe,
		restart: func(ctx context.Context, projectsRoot string) error {
			return restartDaemonAfterRemoteEnroll(ctx, inv.commandRunner(), projectsRoot)
		},
	}
}

func newPeersJoinCmd(inv *invocation) *cobra.Command {
	var tokenFile string
	var tokenStdin, restartDaemon, jsonOut bool
	command := &cobra.Command{
		Use:   "join <hub-url>",
		Short: "Join a hub as its peer, verifying the invite token first",
		Long: `wb peers join reads the one-time token an operator minted with
"wb peers invite", verifies it against the hub, stores it as a private
credential, writes peers.upstream in wb.yaml (never touching remote:), and
restarts a running daemon so the peer session starts immediately.

  wb peers invite laptop --token-file token.txt   # on the hub
  cat token.txt | wb peers join https://vm1.sneat.dev --token-stdin   # on the laptop`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runPeersJoin(command.Context(), defaultPeersJoinDeps(inv), inv.projectsRoot, args[0], tokenFile, tokenStdin, restartDaemon, jsonOut, command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr())
		},
	}
	command.Flags().StringVar(&tokenFile, "token-file", "", "read the one-time token from this absolute path")
	command.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the one-time token from stdin")
	command.Flags().BoolVar(&restartDaemon, "restart-daemon", true, "restart WB daemon if it is running")
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "peers join laptop connect hub upstream token dial vm block")
	return command
}

type peersJoinResult struct {
	HubURL        string `json:"hub_url"`
	ConfigPath    string `json:"config_path"`
	TokenFile     string `json:"token_file"`
	Verified      bool   `json:"verified"`
	DaemonRestart bool   `json:"daemon_restart"`
}

func runPeersJoin(ctx context.Context, deps peersJoinDeps, projectsRoot, hubURL, tokenFile string, tokenStdin, restartDaemon, jsonOut bool, in io.Reader, out, errOut io.Writer) error {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if err := remotestate.ValidateHubURL(hubURL); err != nil {
		return &exitError{code: exitUsage, message: err.Error()}
	}
	switch {
	case tokenStdin && strings.TrimSpace(tokenFile) != "":
		return &exitError{code: exitUsage, message: "pass either --token-stdin or --token-file, not both"}
	case !tokenStdin && strings.TrimSpace(tokenFile) == "":
		return &exitError{code: exitUsage, message: "--token-stdin or --token-file is required"}
	}
	token, err := readPeersJoinToken(in, tokenFile, tokenStdin)
	if err != nil {
		return &exitError{code: exitUsage, message: err.Error()}
	}

	configPath := deps.configPath()
	var unconfigured *remotestate.UnconfiguredError
	switch cfg, loadErr := remotestate.LoadConfig(configPath); {
	case loadErr == nil && cfg.Provider == "hub" && sameOrigin(cfg.URL, hubURL):
		return &exitError{code: exitUsage, message: fmt.Sprintf("remote.provider is already hub at %s; two receivers cannot consume one queue", cfg.URL)}
	case loadErr != nil && !errors.As(loadErr, &unconfigured):
		// "No remote configured yet" (a fresh install's common case) is not
		// refused: there is nothing to conflict with. Any other load failure
		// — wb.yaml exists but fails to parse — is refused rather than
		// silently skipping the same-origin check it would otherwise have
		// made: joining on top of an unreadable config risks exactly the
		// two-receivers-one-queue conflict that check exists to catch.
		return fmt.Errorf("load %s: %w", configPath, loadErr)
	}

	if err := deps.verify(ctx, hubURL, token); err != nil {
		return &exitError{code: exitFindings, message: "verify peer token: " + err.Error()}
	}
	// peer-connectivity#req:node-identity: the node ID is created on first
	// daemon start or on first `wb peers join`, whichever happens first on
	// this machine. A failure here is reported (to stderr: it is a
	// diagnostic, not part of join's own result) but does not block the
	// join: the daemon creates it too, on its next start.
	if _, err := nodeidentity.Load(projectsRoot, nil); err != nil {
		_, _ = fmt.Fprintln(errOut, "wb: node identity unavailable:", err)
	}

	digest := sha256.Sum256([]byte(token))
	tokenFileName := fmt.Sprintf("peer-%s-%s.token", hostForFilename(hubURL), hex.EncodeToString(digest[:3]))
	absoluteTokenFile := filepath.Join(filepath.Dir(configPath), "credentials", tokenFileName)
	created, err := writePrivateCredential(absoluteTokenFile, token)
	if err != nil {
		return err
	}
	if err := wbconfig.SetPeersUpstream(configPath, hubURL, absoluteTokenFile); err != nil {
		if created {
			_ = os.Remove(absoluteTokenFile)
		}
		return err
	}
	restarted := false
	if restartDaemon {
		if err := deps.restart(ctx, projectsRoot); err != nil {
			return fmt.Errorf("peer join saved, but daemon restart failed: %w", err)
		}
		restarted = true
	}
	result := peersJoinResult{HubURL: hubURL, ConfigPath: configPath, TokenFile: absoluteTokenFile, Verified: true, DaemonRestart: restarted}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err = fmt.Fprintf(out, "Joined %s\n  config      %s\n  credential  %s\n  verified    yes\n  daemon      %s\n",
		hubURL, configPath, absoluteTokenFile, map[bool]string{true: "restarted if running", false: "restart skipped"}[restarted])
	return err
}

func readPeersJoinToken(in io.Reader, tokenFile string, tokenStdin bool) (string, error) {
	if tokenStdin {
		return readRemoteEnrollmentToken(in)
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

// sameOrigin compares scheme and host only, so "https://vm1.sneat.dev/" and
// "https://vm1.sneat.dev" (or a differing path/query rejected earlier by
// ValidateHubURL) are recognised as the same receiver. Both sides are
// normalised the same way before comparing: lowercase, a trailing dot
// stripped from the hostname (DNS treats "vm1.sneat.dev." and
// "vm1.sneat.dev" as the same name), and the scheme's default port treated
// as equivalent to no port at all, so "https://vm1.sneat.dev:443" and
// "https://vm1.sneat.dev" are recognised as the same origin too.
func sameOrigin(a, b string) bool {
	parsedA, errA := url.Parse(a)
	parsedB, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	schemeA, schemeB := strings.ToLower(parsedA.Scheme), strings.ToLower(parsedB.Scheme)
	return schemeA == schemeB && normalizeOriginHost(parsedA, schemeA) == normalizeOriginHost(parsedB, schemeB)
}

// normalizeOriginHost lowercases the hostname, strips a trailing DNS root
// dot, and drops an explicit port that already matches the scheme's default
// (443 for https, 80 for http), so it compares equal to a URL that omitted
// the port entirely.
func normalizeOriginHost(parsed *url.URL, scheme string) string {
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimSuffix(host, ".")
	port := parsed.Port()
	defaultPort := map[string]string{"https": "443", "http": "80"}[scheme]
	if port == "" || port == defaultPort {
		return host
	}
	return host + ":" + port
}

// hostForFilename names the private credential file after the hub host, so
// several joined hubs never collide on one filename.
func hostForFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || strings.TrimSpace(parsed.Hostname()) == "" {
		return "hub"
	}
	return strings.ReplaceAll(parsed.Host, ":", "-")
}

// verifyPeerConnectProbe is Task 1's stand-in for the real hello/welcome
// handshake (Task 2): an authenticated GET on the same connect route,
// answered by hub.apiHandler.peersConnect. It proves the token is valid, is
// a peer credential, and is not blocked, before anything is written to
// disk.
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
