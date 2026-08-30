package githubobserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"golang.org/x/sys/unix"
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

func (o *Observer) Get(ctx context.Context, request GetRequest) (Response, error) {
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
	defer unlock()

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
	httpResult, err := o.apiGet(ctx, request.Dir, request.Endpoint, request.Query, request.Accept, headers)
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
	result := o.Execute(ctx, dir, args...)
	if result.Err != nil {
		message := strings.TrimSpace(string(result.Stderr))
		if stdout := strings.TrimSpace(string(result.Stdout)); stdout != "" {
			if message != "" {
				message += ": "
			}
			message += stdout
		}
		if message == "" {
			message = result.Err.Error()
		}
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), result.Err, message)
	}
	return append([]byte(nil), result.Stdout...), nil
}

func (o *Observer) Execute(ctx context.Context, dir string, args ...string) CommandResponse {
	if len(args) == 0 {
		return CommandResponse{Err: errors.New("GitHub command is required"), ExitCode: 2}
	}
	result := o.runner()(ctx, dir, args...)
	return CommandResponse{Stdout: append([]byte(nil), result.Stdout...), Stderr: append([]byte(nil), result.Stderr...), ExitCode: result.ExitCode, Err: result.Err}
}

func (o *Observer) apiGet(ctx context.Context, dir, endpoint string, query map[string]string, accept string, conditional map[string]string) (httpResponse, error) {
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
	for attempt := 0; attempt < o.maxAttempts(); attempt++ {
		commandCtx, cancel := withCommandTimeout(ctx)
		result := o.runner()(commandCtx, dir, args...)
		cancel()
		response, parseErr := parseIncludedResponse(result.Stdout)
		if parseErr != nil && result.Err == nil {
			return httpResponse{StatusCode: 200, Headers: map[string]string{}, Body: append([]byte(nil), result.Stdout...)}, nil
		}
		if parseErr == nil && result.Err == nil {
			return response, nil
		}
		if parseErr == nil && isThrottleStatus(response.StatusCode) {
			delay, reason := o.retryDelay(attempt, response.Headers)
			if attempt+1 >= o.maxAttempts() {
				return httpResponse{}, fmt.Errorf("GitHub throttled %s after %d attempts: %s", endpoint, attempt+1, reason)
			}
			if sleepErr := o.sleep(ctx, delay); sleepErr != nil {
				return httpResponse{}, sleepErr
			}
			lastErr = fmt.Errorf("GitHub throttled %s: %s", endpoint, reason)
			continue
		}
		if parseErr == nil {
			if result.Err == nil {
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
			return httpResponse{}, fmt.Errorf("gh api %s: %w: %s", endpoint, result.Err, message)
		}
		return httpResponse{}, parseErr
	}
	if lastErr != nil {
		return httpResponse{}, lastErr
	}
	return httpResponse{}, fmt.Errorf("GitHub did not return a response for %s", endpoint)
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

func isThrottleStatus(status int) bool {
	return status == 403 || status == 429
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
