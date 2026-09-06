package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/internal/remotestate"
)

const (
	maxResponseBytes = 1 << 20
	maxTokenBytes    = 16 << 10
)

var (
	defaultRetryDelays   = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}
	ErrClaimsUnsupported = errors.New("the HTTP hub provider does not yet support remote claims")
)

// Options configures the authenticated HTTPS provider. Exactly one of Token
// and TokenFile must be set; the constructor never invents a credential.
type Options struct {
	BaseURL     string
	Machine     string
	Token       string
	TokenFile   string
	Client      *http.Client
	Sleep       func(context.Context, time.Duration) error
	RetryDelays []time.Duration
}

// Provider publishes and reads privacy-safe remote snapshots through the hub.
type Provider struct {
	baseURL     string
	machine     string
	token       string
	tokenFile   string
	client      *http.Client
	sleep       func(context.Context, time.Duration) error
	retryDelays []time.Duration
}

// New validates the endpoint and credential source without making a request.
func New(options Options) (*Provider, error) {
	parsed, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("remote.url must be an HTTPS origin without credentials, query, or fragment")
	}
	if err := machinesnapshot.ValidateIdentity(options.Machine); err != nil {
		return nil, errors.New("remote.machine is invalid for the HTTP hub provider")
	}
	if (strings.TrimSpace(options.Token) == "") == (strings.TrimSpace(options.TokenFile) == "") {
		return nil, errors.New("exactly one hub credential source is required")
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	retryDelays := options.RetryDelays
	if retryDelays == nil {
		retryDelays = append([]time.Duration(nil), defaultRetryDelays...)
	}
	for _, delay := range retryDelays {
		if delay < 0 {
			return nil, errors.New("hub retry delay must not be negative")
		}
	}
	token := strings.TrimSpace(options.Token)
	if token != "" && !validToken(token) {
		return nil, errors.New("injected hub credential must contain one non-empty token")
	}
	return &Provider{
		baseURL: strings.TrimRight(parsed.String(), "/"), machine: options.Machine,
		token: token, tokenFile: strings.TrimSpace(options.TokenFile),
		client: client, sleep: sleep, retryDelays: append([]time.Duration(nil), retryDelays...),
	}, nil
}

// Publish strips local-only state, binds the configured machine, and sends an
// idempotent request. Retrying transient failures is safe because the hub
// deduplicates the validated payload digest for each login/machine key.
func (provider *Provider) Publish(ctx context.Context, source remotestate.Snapshot) (remotestate.PublishResult, error) {
	if source.Machine != provider.machine {
		return remotestate.PublishResult{}, fmt.Errorf("snapshot machine %q does not match configured machine %q", source.Machine, provider.machine)
	}
	snapshot := FromRemoteSnapshot(source)
	if err := snapshot.Validate(); err != nil {
		return remotestate.PublishResult{}, err
	}
	var receipt machinesnapshot.Receipt
	if err := provider.doJSON(ctx, http.MethodPost, snapshot, &receipt); err != nil {
		return remotestate.PublishResult{}, fmt.Errorf("publish hosted snapshot: %w", err)
	}
	if receipt.Login != snapshot.Login || receipt.Machine != snapshot.Machine ||
		receipt.PublishedAt != snapshot.PublishedAt || receipt.ReceivedAt.IsZero() {
		return remotestate.PublishResult{}, errors.New("hub returned a receipt for a different publisher")
	}
	return remotestate.PublishResult{Location: provider.baseURL + machinesnapshot.SnapshotPath}, nil
}

// List returns the authenticated login's privacy-safe machine snapshots.
func (provider *Provider) List(ctx context.Context) ([]remotestate.Entry, error) {
	var response machinesnapshot.ListResponse
	if err := provider.doJSON(ctx, http.MethodGet, nil, &response); err != nil {
		return nil, fmt.Errorf("list hosted snapshots: %w", err)
	}
	machinesnapshot.SortPublished(response.Snapshots)
	entries := make([]remotestate.Entry, 0, len(response.Snapshots))
	for _, published := range response.Snapshots {
		if err := published.Snapshot.Validate(); err != nil || published.ReceivedAt.IsZero() {
			entries = append(entries, remotestate.Entry{
				Snapshot: remotestate.Snapshot{Login: published.Snapshot.Login, Machine: published.Snapshot.Machine},
				Error:    "invalid hosted snapshot response",
			})
			continue
		}
		entries = append(entries, Entry(machinesnapshot.StoredSnapshot{Snapshot: published.Snapshot, ReceivedAt: published.ReceivedAt}))
	}
	return entries, nil
}

func (provider *Provider) Claim(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, ErrClaimsUnsupported
}

func (provider *Provider) Release(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, ErrClaimsUnsupported
}

func (provider *Provider) Claims(context.Context) ([]remotestate.ClaimEntry, error) {
	return nil, ErrClaimsUnsupported
}

func (provider *Provider) doJSON(ctx context.Context, method string, input, output any) error {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	token, err := provider.credential()
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, provider.baseURL+machinesnapshot.SnapshotPath, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		if input != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, requestErr := provider.client.Do(request)
		if requestErr == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
			decodeErr := decoder.Decode(output)
			closeErr := response.Body.Close()
			if decodeErr != nil {
				return fmt.Errorf("decode successful hub response: %w", decodeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close successful hub response: %w", closeErr)
			}
			return nil
		}
		transient := requestErr != nil
		var responseCode int
		if response != nil {
			responseCode = response.StatusCode
			transient = retryableStatus(responseCode)
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
			_ = response.Body.Close()
		}
		if !transient || attempt >= len(provider.retryDelays) {
			if requestErr != nil {
				return fmt.Errorf("hub request failed: %w", requestErr)
			}
			return fmt.Errorf("hub returned HTTP %d", responseCode)
		}
		if err := provider.sleep(ctx, provider.retryDelays[attempt]); err != nil {
			return err
		}
	}
}

func (provider *Provider) credential() (string, error) {
	if provider.token != "" {
		return provider.token, nil
	}
	file, err := os.Open(provider.tokenFile)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", errors.New("hub credential file does not exist")
		case errors.Is(err, os.ErrPermission):
			return "", errors.New("hub credential file is not readable")
		default:
			return "", errors.New("open hub credential file")
		}
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", errors.New("read hub credential file")
	}
	if closeErr != nil {
		return "", errors.New("close hub credential file")
	}
	if len(raw) > maxTokenBytes {
		return "", errors.New("hub credential file is too large")
	}
	token := strings.TrimSpace(string(raw))
	if !validToken(token) {
		return "", errors.New("hub credential file must contain one non-empty token")
	}
	return token, nil
}

func validToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, " \t\r\n") && len(token) <= maxTokenBytes
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
