package githubobserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

const (
	cacheSchemaVersion   = 1
	defaultMaxAttempts   = 4
	defaultBaseBackoff   = 250 * time.Millisecond
	defaultMaxBackoff    = 5 * time.Second
	defaultCommandTimout = 15 * time.Second
)

type GetRequest struct {
	Dir         string
	Repository  string
	Target      string
	Head        string
	Endpoint    string
	Query       map[string]string
	Accept      string
	FreshWindow time.Duration
	Progress    progress.Reporter
}

type Response struct {
	Body        []byte
	StatusCode  int
	Headers     map[string]string
	Cached      bool
	ObservedAt  time.Time
	CacheKey    string
	RequestHash string
}

type CommandResponse struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

type Observer struct {
	StateDir    string
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
	RandomIntn  func(int64) int64
	Run         func(context.Context, string, ...string) commandResult
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

type cacheEntry struct {
	SchemaVersion int       `json:"schema_version"`
	Repository    string    `json:"repository,omitempty"`
	Target        string    `json:"target,omitempty"`
	Head          string    `json:"head,omitempty"`
	Endpoint      string    `json:"endpoint"`
	RequestHash   string    `json:"request_hash"`
	StatusCode    int       `json:"status_code"`
	Body          []byte    `json:"body"`
	BodySHA256    string    `json:"body_sha256"`
	ETag          string    `json:"etag,omitempty"`
	LastModified  string    `json:"last_modified,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
}

type httpResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

var (
	defaultOnce     sync.Once
	defaultObserver *Observer
)

type progressContextKey struct{}

// WithProgress attaches one transport-neutral progress sink to GitHub reads
// made below ctx. An explicit GetRequest.Progress takes precedence.
func WithProgress(ctx context.Context, reporter progress.Reporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

func Default() *Observer {
	defaultOnce.Do(func() {
		defaultObserver = &Observer{}
	})
	return defaultObserver
}

func Get(ctx context.Context, request GetRequest) (Response, error) {
	return Default().Get(ctx, request)
}

func Read(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return Default().Read(ctx, dir, args...)
}

func Execute(ctx context.Context, dir string, args ...string) CommandResponse {
	return Default().Execute(ctx, dir, args...)
}

func (o *Observer) Get(ctx context.Context, request GetRequest) (response Response, err error) {
	if request.Progress == nil {
		request.Progress, _ = ctx.Value(progressContextKey{}).(progress.Reporter)
	}
	request.Endpoint = strings.TrimSpace(request.Endpoint)
	if request.Endpoint == "" {
		return Response{}, errors.New("GitHub endpoint is required")
	}
	requestStartedAt := o.now()
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, request.Query, request.Accept)
	cachePath, lockPath, err := o.pathsForKey(key)
	if err != nil {
		return Response{}, err
	}
	unlock, err := acquireLock(lockPath)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release GitHub observer lock %s: %w", lockPath, unlockErr))
		}
	}()

	checkedAt := o.now()
	freshWindow := request.FreshWindow
	entry, cacheErr := readCacheEntry(cachePath)
	if cacheErr != nil {
		entry = nil
	}
	if entry != nil && len(entry.Body) > 0 {
		reuse := false
		if freshWindow > 0 && checkedAt.Sub(entry.ObservedAt) <= freshWindow {
			reuse = true
		}
		if freshWindow <= 0 && !entry.ObservedAt.Before(requestStartedAt) {
			reuse = true
		}
		if reuse {
			return Response{
				Body:       append([]byte(nil), entry.Body...),
				StatusCode: entry.StatusCode,
				Headers: map[string]string{
					"etag":          entry.ETag,
					"last-modified": entry.LastModified,
				},
				Cached:      true,
				ObservedAt:  entry.ObservedAt,
				CacheKey:    key,
				RequestHash: entry.RequestHash,
			}, nil
		}
	}

	headers := map[string]string{}
	if entry != nil {
		if entry.ETag != "" {
			headers["If-None-Match"] = entry.ETag
		}
		if entry.LastModified != "" {
			headers["If-Modified-Since"] = entry.LastModified
		}
	}
	httpResult, err := o.apiGet(ctx, request, headers)
	if err != nil {
		return Response{}, err
	}
	observedAt := o.now()
	switch httpResult.StatusCode {
	case 304:
		if entry == nil || len(entry.Body) == 0 {
			return Response{}, fmt.Errorf("GitHub returned 304 for %s without a cached body", request.Endpoint)
		}
		entry.ObservedAt = observedAt
		if writeErr := writeCacheEntry(cachePath, entry); writeErr != nil {
			return Response{}, writeErr
		}
		return Response{
			Body:        append([]byte(nil), entry.Body...),
			StatusCode:  200,
			Headers:     normalizeHeaders(httpResult.Headers, entry),
			Cached:      true,
			ObservedAt:  entry.ObservedAt,
			CacheKey:    key,
			RequestHash: entry.RequestHash,
		}, nil
	case 200:
		updated := &cacheEntry{
			SchemaVersion: cacheSchemaVersion,
			Repository:    request.Repository,
			Target:        request.Target,
			Head:          request.Head,
			Endpoint:      request.Endpoint,
			RequestHash:   requestHash(request.Endpoint, request.Query, request.Accept),
			StatusCode:    httpResult.StatusCode,
			Body:          append([]byte(nil), httpResult.Body...),
			BodySHA256:    digest(httpResult.Body),
			ETag:          strings.TrimSpace(httpResult.Headers["etag"]),
			LastModified:  strings.TrimSpace(httpResult.Headers["last-modified"]),
			ObservedAt:    observedAt,
		}
		if updated.ETag != "" || updated.LastModified != "" || len(updated.Body) > 0 {
			if writeErr := writeCacheEntry(cachePath, updated); writeErr != nil {
				return Response{}, writeErr
			}
		}
		return Response{
			Body:        append([]byte(nil), httpResult.Body...),
			StatusCode:  httpResult.StatusCode,
			Headers:     normalizeHeaders(httpResult.Headers, updated),
			ObservedAt:  updated.ObservedAt,
			CacheKey:    key,
			RequestHash: updated.RequestHash,
		}, nil
	default:
		return Response{}, fmt.Errorf("GitHub returned unexpected HTTP %d for %s", httpResult.StatusCode, request.Endpoint)
	}
}

func (o *Observer) Read(ctx context.Context, dir string, args ...string) ([]byte, error) {
	requestCtx, cancelRequest := withCommandTimeout(ctx)
	defer cancelRequest()
	reporter, _ := ctx.Value(progressContextKey{}).(progress.Reporter)
	maxAttempts := o.maxAttempts()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result := o.Execute(requestCtx, dir, args...)
		if result.Err == nil {
			return append([]byte(nil), result.Stdout...), nil
		}
		message := commandFailureMessage(result)
		cause, retryable := retryableReadFailure(requestCtx, result, message)
		if !retryable || attempt+1 >= maxAttempts {
			return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), result.Err, message)
		}
		delay, reason := o.retryDelay(attempt, nil)
		reportRetryProgress(reporter, repositoryArgument(args), "github_read", attempt+1, maxAttempts, cause, delay, reason)
		if sleepErr := o.sleep(requestCtx, delay); sleepErr != nil {
			return nil, fmt.Errorf("gh %s retry after %s: %w", strings.Join(args, " "), cause, sleepErr)
		}
	}
	return nil, fmt.Errorf("gh %s did not return a response", strings.Join(args, " "))
}

func (o *Observer) Execute(ctx context.Context, dir string, args ...string) CommandResponse {
	if len(args) == 0 {
		return CommandResponse{Err: errors.New("GitHub command is required"), ExitCode: 2}
	}
	result := o.runner()(ctx, dir, args...)
	return CommandResponse{Stdout: append([]byte(nil), result.Stdout...), Stderr: append([]byte(nil), result.Stderr...), ExitCode: result.ExitCode, Err: result.Err}
}

func (o *Observer) apiGet(ctx context.Context, request GetRequest, conditional map[string]string) (httpResponse, error) {
	requestCtx, cancelRequest := withCommandTimeout(ctx)
	defer cancelRequest()
	dir := request.Dir
	endpoint := request.Endpoint
	query := request.Query
	accept := request.Accept
	args := []string{"api", endpoint, "--include"}
	if len(query) > 0 {
		args = append(args, "--method", "GET")
	}
	if accept != "" {
		args = append(args, "-H", "Accept: "+accept)
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "-f", key+"="+query[key])
	}
	conditionalKeys := make([]string, 0, len(conditional))
	for key := range conditional {
		conditionalKeys = append(conditionalKeys, key)
	}
	sort.Strings(conditionalKeys)
	for _, key := range conditionalKeys {
		if conditional[key] != "" {
			args = append(args, "-H", key+": "+conditional[key])
		}
	}
	var lastErr error
	maxAttempts := o.maxAttempts()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result := o.runner()(requestCtx, dir, args...)
		response, parseErr := parseIncludedResponse(result.Stdout)
		commandOK := result.Err == nil && result.ExitCode == 0
		if parseErr != nil && commandOK {
			return httpResponse{StatusCode: 200, Headers: map[string]string{}, Body: append([]byte(nil), result.Stdout...)}, nil
		}
		// gh returns a non-zero exit status for conditional requests whose
		// response is HTTP 304. The included response is authoritative in this
		// case; retain command failures for every other parsed status.
		if parseErr == nil && (commandOK || response.StatusCode == 304) {
			return response, nil
		}
		if parseErr == nil && isRetryableHTTPResponse(response) {
			delay, reason := o.retryDelay(attempt, response.Headers)
			cause := fmt.Sprintf("HTTP %d", response.StatusCode)
			if attempt+1 >= maxAttempts {
				return httpResponse{}, fmt.Errorf("GitHub %s returned %s after %d attempts", endpoint, cause, attempt+1)
			}
			reportRetry(request, attempt+1, maxAttempts, cause, delay, reason)
			if sleepErr := o.sleep(requestCtx, delay); sleepErr != nil {
				return httpResponse{}, fmt.Errorf("GitHub %s retry after %s: %w", endpoint, cause, sleepErr)
			}
			lastErr = fmt.Errorf("GitHub %s returned %s", endpoint, cause)
			continue
		}
		if parseErr == nil {
			if commandOK {
				return response, nil
			}
			message := strings.TrimSpace(string(result.Stderr))
			if message == "" {
				message = strings.TrimSpace(string(response.Body))
			}
			return httpResponse{}, fmt.Errorf("GitHub %s returned HTTP %d: %s", endpoint, response.StatusCode, message)
		}
		if result.Err != nil {
			message := strings.TrimSpace(string(result.Stderr))
			if message == "" {
				message = strings.TrimSpace(string(result.Stdout))
			}
			if isTemporaryCommandFailure(requestCtx, result.Err, message) {
				delay, reason := o.retryDelay(attempt, nil)
				cause := temporaryFailureCause(message, result.Err)
				if attempt+1 >= maxAttempts {
					return httpResponse{}, fmt.Errorf("gh api %s failed temporarily after %d attempts: %s", endpoint, attempt+1, cause)
				}
				reportRetry(request, attempt+1, maxAttempts, cause, delay, reason)
				if sleepErr := o.sleep(requestCtx, delay); sleepErr != nil {
					return httpResponse{}, fmt.Errorf("GitHub %s retry after %s: %w", endpoint, cause, sleepErr)
				}
				lastErr = fmt.Errorf("gh api %s failed temporarily: %s", endpoint, cause)
				continue
			}
			return httpResponse{}, fmt.Errorf("gh api %s: %w: %s", endpoint, result.Err, message)
		}
		return httpResponse{}, parseErr
	}
	if lastErr != nil {
		return httpResponse{}, lastErr
	}
	return httpResponse{}, fmt.Errorf("GitHub did not return a response for %s", endpoint)
}

func reportRetry(request GetRequest, attempt, maxAttempts int, cause string, delay time.Duration, delayReason string) {
	reportRetryProgress(request.Progress, request.Repository, "github_api", attempt, maxAttempts, cause, delay, delayReason)
}

func reportRetryProgress(reporter progress.Reporter, repository, operation string, attempt, maxAttempts int, cause string, delay time.Duration, delayReason string) {
	detail := fmt.Sprintf("attempt %d/%d failed: %s; retrying in %s", attempt, maxAttempts, cause, delay)
	if delayReason != "" {
		detail += " (" + delayReason + ")"
	}
	progress.Report(reporter, progress.Event{
		Operation: operation, Phase: "retry", Repository: repository,
		Detail: detail, State: progress.Waiting,
	})
}

func commandFailureMessage(result CommandResponse) string {
	message := strings.TrimSpace(string(result.Stderr))
	if stdout := strings.TrimSpace(string(result.Stdout)); stdout != "" {
		if message != "" {
			message += ": "
		}
		message += stdout
	}
	if message == "" && result.Err != nil {
		message = result.Err.Error()
	}
	return message
}

func retryableReadFailure(ctx context.Context, result CommandResponse, message string) (string, bool) {
	lower := strings.ToLower(message)
	for _, status := range []int{429, 502, 503, 504} {
		for _, marker := range []string{fmt.Sprintf("http %d", status), fmt.Sprintf("status code %d", status)} {
			if strings.Contains(lower, marker) {
				return fmt.Sprintf("HTTP %d", status), true
			}
		}
	}
	if strings.Contains(lower, "secondary rate limit") || strings.Contains(lower, "rate limit exceeded") {
		return "GitHub rate limit", true
	}
	if isTemporaryCommandFailure(ctx, result.Err, message) {
		return temporaryFailureCause(message, result.Err), true
	}
	return "", false
}

func repositoryArgument(args []string) string {
	for index, arg := range args {
		if arg == "--repo" && index+1 < len(args) {
			return strings.TrimSpace(args[index+1])
		}
		if strings.HasPrefix(arg, "--repo=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
		}
	}
	return ""
}

func isRetryableHTTPResponse(response httpResponse) bool {
	switch response.StatusCode {
	case 429, 502, 503, 504:
		return true
	case 403:
		if strings.TrimSpace(response.Headers["retry-after"]) != "" {
			return true
		}
		if strings.TrimSpace(response.Headers["x-ratelimit-remaining"]) == "0" {
			return true
		}
		body := strings.ToLower(string(response.Body))
		return strings.Contains(body, "secondary rate limit") || strings.Contains(body, "rate limit exceeded")
	default:
		return false
	}
}

func isTemporaryCommandFailure(ctx context.Context, err error, message string) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	message = strings.ToLower(strings.TrimSpace(message + " " + err.Error()))
	for _, fragment := range []string{
		"error connecting to api.github.com",
		"connection reset",
		"connection refused",
		"connection closed",
		"server closed idle connection",
		"temporary failure",
		"temporarily unavailable",
		"tls handshake timeout",
		"i/o timeout",
		"unexpected eof",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func temporaryFailureCause(message string, err error) string {
	normalized := strings.ToLower(strings.TrimSpace(message + " " + err.Error()))
	for _, cause := range []string{
		"error connecting to api.github.com",
		"connection reset",
		"connection refused",
		"connection closed",
		"server closed idle connection",
		"temporary failure",
		"temporarily unavailable",
		"tls handshake timeout",
		"i/o timeout",
		"unexpected eof",
	} {
		if strings.Contains(normalized, cause) {
			return cause
		}
	}
	return "temporary network failure"
}

func (o *Observer) pathsForKey(key string) (cachePath, lockPath string, err error) {
	stateDir := o.stateDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "cache"), 0o700); err != nil {
		return "", "", fmt.Errorf("create GitHub observer cache directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "locks"), 0o700); err != nil {
		return "", "", fmt.Errorf("create GitHub observer lock directory: %w", err)
	}
	return filepath.Join(stateDir, "cache", key+".json"), filepath.Join(stateDir, "locks", key+".lock"), nil
}

func (o *Observer) runner() func(context.Context, string, ...string) commandResult {
	if o.Run != nil {
		return o.Run
	}
	return runGH
}

func (o *Observer) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o *Observer) sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if o.Sleep != nil {
		return o.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *Observer) randomIntn(max int64) int64 {
	if max <= 0 {
		return 0
	}
	if o.RandomIntn != nil {
		return o.RandomIntn(max)
	}
	return time.Now().UnixNano() % max
}

func (o *Observer) maxAttempts() int {
	if o.MaxAttempts > 0 {
		return o.MaxAttempts
	}
	return defaultMaxAttempts
}

func (o *Observer) baseBackoff() time.Duration {
	if o.BaseBackoff > 0 {
		return o.BaseBackoff
	}
	return defaultBaseBackoff
}

func (o *Observer) maxBackoff() time.Duration {
	if o.MaxBackoff > 0 {
		return o.MaxBackoff
	}
	return defaultMaxBackoff
}

func (o *Observer) stateDir() string {
	if strings.TrimSpace(o.StateDir) != "" {
		return o.StateDir
	}
	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
		return filepath.Join(stateHome, "wb", "github-observer")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".wb", "github-observer")
	}
	return filepath.Join(home, ".local", "state", "wb", "github-observer")
}

func (o *Observer) retryDelay(attempt int, headers map[string]string) (time.Duration, string) {
	capDelay := o.baseBackoff()
	for i := 0; i < attempt; i++ {
		capDelay *= 2
		if capDelay >= o.maxBackoff() {
			capDelay = o.maxBackoff()
			break
		}
	}
	jitter := time.Duration(o.randomIntn(int64(capDelay) + 1))
	delay := jitter
	reason := "exponential full-jitter backoff"
	if retryAfter := parseRetryAfter(headers["retry-after"], o.now()); retryAfter > delay {
		delay = retryAfter
		reason = "Retry-After"
	}
	if reset := parseRateLimitReset(headers["x-ratelimit-reset"], o.now()); reset > delay {
		delay = reset
		reason = "X-RateLimit-Reset"
	}
	if delay <= 0 {
		delay = o.baseBackoff()
	}
	return delay, reason
}

func runGH(ctx context.Context, dir string, args ...string) commandResult {
	command := exec.CommandContext(ctx, "gh", args...)
	command.Dir = dir
	command.Env = console.Env()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Err: err}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = 1
	return result
}

func withCommandTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultCommandTimout)
}

func readCacheEntry(path string) (*cacheEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read GitHub observer cache %s: %w", path, err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("decode GitHub observer cache %s: %w", path, err)
	}
	if entry.SchemaVersion != cacheSchemaVersion {
		return nil, nil
	}
	if entry.BodySHA256 != digest(entry.Body) {
		return nil, fmt.Errorf("GitHub observer cache %s body digest mismatch", path)
	}
	return &entry, nil
}

func writeCacheEntry(path string, entry *cacheEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode GitHub observer cache %s: %w", path, err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create GitHub observer cache temp file for %s: %w", path, err)
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("set GitHub observer cache temp permissions for %s: %w", path, err)
	}
	if _, err := tmpFile.Write(raw); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write GitHub observer cache %s: %w", path, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("write GitHub observer cache %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("activate GitHub observer cache %s: %w", path, err)
	}
	return nil
}

func cacheKey(repository, target, head, endpoint string, query map[string]string, accept string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(repository))
	builder.WriteByte('\n')
	builder.WriteString(strings.TrimSpace(target))
	builder.WriteByte('\n')
	builder.WriteString(strings.TrimSpace(head))
	builder.WriteByte('\n')
	builder.WriteString(requestHash(endpoint, query, accept))
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func requestHash(endpoint string, query map[string]string, accept string) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(endpoint))
	builder.WriteByte('\n')
	builder.WriteString(strings.TrimSpace(accept))
	for _, key := range keys {
		builder.WriteByte('\n')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(query[key])
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func normalizeHeaders(headers map[string]string, entry *cacheEntry) map[string]string {
	normalized := map[string]string{}
	for key, value := range headers {
		normalized[strings.ToLower(strings.TrimSpace(key))] = value
	}
	if entry != nil {
		if entry.ETag != "" && normalized["etag"] == "" {
			normalized["etag"] = entry.ETag
		}
		if entry.LastModified != "" && normalized["last-modified"] == "" {
			normalized["last-modified"] = entry.LastModified
		}
	}
	return normalized
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func parseIncludedResponse(raw []byte) (httpResponse, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return httpResponse{}, errors.New("empty GitHub response")
	}
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	separator := []byte("\n\n")
	index := bytes.Index(normalized, separator)
	if index < 0 {
		return httpResponse{}, errors.New("GitHub response omitted headers")
	}
	headerBlock := string(normalized[:index])
	body := append([]byte(nil), normalized[index+len(separator):]...)
	lines := strings.Split(headerBlock, "\n")
	if len(lines) == 0 {
		return httpResponse{}, errors.New("GitHub response omitted status line")
	}
	statusFields := strings.Fields(lines[0])
	if len(statusFields) < 2 {
		return httpResponse{}, fmt.Errorf("GitHub response status line malformed: %q", lines[0])
	}
	statusCode, err := strconv.Atoi(statusFields[1])
	if err != nil {
		return httpResponse{}, fmt.Errorf("GitHub response status line malformed: %q", lines[0])
	}
	headers := map[string]string{}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(line[:colon]))] = strings.TrimSpace(line[colon+1:])
	}
	return httpResponse{StatusCode: statusCode, Headers: headers, Body: body}, nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := httpTime(value); err == nil {
		delay := at.Sub(now.UTC())
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func parseRateLimitReset(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	delay := time.Unix(seconds, 0).UTC().Sub(now.UTC())
	if delay > 0 {
		return delay
	}
	return 0
}

func httpTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, time.RFC850, time.ANSIC} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid HTTP time %q", value)
}

func acquireLock(path string) (func() error, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open GitHub observer lock %s: %w", path, err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("lock GitHub observer lock %s: %w", path, err)
	}
	return func() error {
		if unlockErr := unix.Flock(fd, unix.LOCK_UN); unlockErr != nil {
			_ = unix.Close(fd)
			return unlockErr
		}
		return unix.Close(fd)
	}, nil
}
