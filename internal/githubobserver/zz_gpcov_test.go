package githubobserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/testenv"
)

// This file adds targeted coverage for the githubobserver package. Every test
// asserts an observable outcome: a returned value, an error, a recorded call,
// progress reported to a sink, or a file the package wrote.

const gpCovHead = 40

func gpCovRequest(head, endpoint string) GetRequest {
	return GetRequest{
		Repository: "acme/app",
		Target:     "main",
		Head:       strings.Repeat(head, gpCovHead),
		Endpoint:   endpoint,
	}
}

func gpCovEntry(request GetRequest, body, etag, lastModified string, observedAt time.Time) *cacheEntry {
	return &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Repository:    request.Repository,
		Target:        request.Target,
		Head:          request.Head,
		Endpoint:      request.Endpoint,
		RequestHash:   requestHash(request.Endpoint, request.Query, request.Accept),
		StatusCode:    200,
		Body:          []byte(body),
		BodySHA256:    digest([]byte(body)),
		ETag:          etag,
		LastModified:  lastModified,
		ObservedAt:    observedAt.UTC(),
	}
}

func gpCovWriteEntry(t *testing.T, stateDir string, request GetRequest, entry *cacheEntry) string {
	t.Helper()
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, request.Query, request.Accept)
	path := filepath.Join(stateDir, "cache", key+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCacheEntry(path, entry); err != nil {
		t.Fatal(err)
	}
	return path
}

// WithRetryTelemetry documents that a nil accumulator is a no-op rather than a
// panic: the caller keeps its own context.
func TestGpCovWithRetryTelemetryNilReturnsParentContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := WithRetryTelemetry(ctx, nil); got != ctx {
		t.Fatalf("WithRetryTelemetry(ctx, nil) = %v, want the parent context unchanged", got)
	}
	telemetry := &RetryTelemetry{}
	if got := WithRetryTelemetry(ctx, telemetry); got == ctx {
		t.Fatal("WithRetryTelemetry(ctx, telemetry) must derive a context carrying the accumulator")
	}
}

// WithProgress documents that a nil reporter is a no-op rather than a panic.
func TestGpCovWithProgressNilReturnsParentContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := WithProgress(ctx, nil); got != ctx {
		t.Fatalf("WithProgress(ctx, nil) = %v, want the parent context unchanged", got)
	}
	reporter := func(progress.Event) {}
	if got := WithProgress(ctx, reporter); got == ctx {
		t.Fatal("WithProgress(ctx, reporter) must derive a context carrying the reporter")
	}
}

// The package-level Get/Read/Execute/GetPages helpers are the API other
// packages call. They must all route through one shared Default observer, and
// its fields must be the injection seam those helpers honour.
func TestGpCovDefaultObserverServesPackageLevelHelpers(t *testing.T) {
	first := Default()
	if first == nil {
		t.Fatal("Default() returned nil")
	}
	if second := Default(); second != first {
		t.Fatalf("Default() = %p, want the shared %p", second, first)
	}
	saved := *first
	t.Cleanup(func() { *first = saved })

	first.StateDir = t.TempDir()
	first.Now = func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
	first.Sleep = func(context.Context, time.Duration) error { return nil }
	first.RandomIntn = func(int64) int64 { return 0 }
	first.Run = func(_ context.Context, _ string, args ...string) commandResult {
		if len(args) > 0 && args[0] == "api" {
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		}
		if len(args) > 0 && args[0] == "version" {
			return commandResult{Stdout: []byte("gh version 2.45.0")}
		}
		return commandResult{Stdout: []byte(`{"number":42}`)}
	}

	response, err := Get(context.Background(), GetRequest{Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(response.Body) != `{"ok":true}` {
		t.Fatalf("package Get response = %+v", response)
	}

	output, err := Read(context.Background(), "", "pr", "view", "42", "--repo", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != `{"number":42}` {
		t.Fatalf("package Read output = %q", output)
	}

	executed := Execute(context.Background(), "", "version")
	if executed.Err != nil || executed.ExitCode != 0 || string(executed.Stdout) != "gh version 2.45.0" {
		t.Fatalf("package Execute response = %+v", executed)
	}

	missing := Execute(context.Background(), "")
	if missing.Err == nil || missing.Err.Error() != "GitHub command is required" || missing.ExitCode != 2 {
		t.Fatalf("package Execute without arguments = %+v, want a usage error and exit code 2", missing)
	}

	pages, err := GetPages(context.Background(), GetRequest{Endpoint: "user"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || string(pages[0].Body) != `{"ok":true}` {
		t.Fatalf("package GetPages = %#v", pages)
	}
}

// An empty endpoint must fail before any state directory or lock is touched.
func TestGpCovGetRequiresAnEndpoint(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"", "   "} {
		observer := &Observer{StateDir: t.TempDir()}
		_, err := observer.Get(context.Background(), GetRequest{Endpoint: endpoint})
		if err == nil || err.Error() != "GitHub endpoint is required" {
			t.Fatalf("endpoint %q: err = %v, want the endpoint requirement", endpoint, err)
		}
	}
}

// pathsForKey must surface both directory-creation failures with a message
// naming which directory failed.
func TestGpCovPathsForKeyReportsDirectoryCreationFailures(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := (&Observer{StateDir: blocker}).pathsForKey("key")
	if err == nil || !strings.Contains(err.Error(), "create GitHub observer cache directory") {
		t.Fatalf("cache dir err = %v, want the cache-directory failure", err)
	}

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "locks"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = (&Observer{StateDir: stateDir}).pathsForKey("key")
	if err == nil || !strings.Contains(err.Error(), "create GitHub observer lock directory") {
		t.Fatalf("lock dir err = %v, want the lock-directory failure", err)
	}
}

// A state directory that cannot be prepared must fail the read instead of
// silently falling back to the user's real state directory.
func TestGpCovGetReportsUnpreparableStateDirectory(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	observer := &Observer{StateDir: blocker}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !strings.Contains(err.Error(), "create GitHub observer cache directory") {
		t.Fatalf("err = %v, want the cache-directory failure", err)
	}
}

// A lock that cannot even be opened must fail the whole read, before any gh
// invocation, rather than running without cross-process exclusion.
func TestGpCovGetReportsUnopenableLock(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	request := GetRequest{Endpoint: "user"}
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, request.Query, request.Accept)
	lockPath := filepath.Join(stateDir, "locks", key+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the lock file belongs cannot be opened for locking.
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}

	observer := &Observer{
		StateDir: stateDir,
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			t.Fatal("gh must not run when the observer lock cannot be acquired")
			return commandResult{}
		},
	}
	_, err := observer.Get(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "open GitHub observer lock") {
		t.Fatalf("err = %v, want the lock-open failure", err)
	}
}

// A 403 whose remaining rate-limit budget is zero is retryable even when the
// response carries no Retry-After and the body never says "rate limit".
func TestGpCovGetRetriesExhaustedRateLimitForbidden(t *testing.T) {
	t.Parallel()
	var calls int
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{
					Stdout:   []byte("HTTP/2 403 Forbidden\nX-RateLimit-Remaining: 0\n\n{\"message\":\"Forbidden\"}"),
					Stderr:   []byte("gh: HTTP 403"),
					ExitCode: 1,
					Err:      errors.New("exit status 1"),
				}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	response, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(response.Body) != `{"ok":true}` {
		t.Fatalf("calls = %d body = %q, want one retry after the exhausted rate limit", calls, response.Body)
	}
}

// A 304 with no cached body means the conditional request was sent without a
// validator; the caller gets an error rather than an empty success.
func TestGpCovGetRejectsNotModifiedWithoutCachedBody(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 304 Not Modified\n\n"),
				Stderr:   []byte("gh: HTTP 304"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), gpCovRequest("4", "repos/acme/app/branches/main"))
	if err == nil || !strings.Contains(err.Error(), "without a cached body") {
		t.Fatalf("err = %v, want the no-cached-body refusal", err)
	}
}

// Revalidating a 304 refreshes the cache entry's timestamp. If that write
// cannot happen the read must fail rather than report a cached body it could
// not persist.
func TestGpCovGetReportsUnwritableCacheWhenRevalidating(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	request := gpCovRequest("5", "repos/acme/app/branches/main")
	request.FreshWindow = time.Millisecond
	entry := gpCovEntry(request, `{"name":"main"}`, `"etag-cov"`, "", now.Add(-time.Hour))
	cachePath := gpCovWriteEntry(t, stateDir, request, entry)

	cacheDir := filepath.Dir(cachePath)
	if err := os.Chmod(cacheDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cacheDir, 0o700) })

	observer := &Observer{
		StateDir: stateDir,
		Now:      func() time.Time { return now },
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			if !strings.Contains(strings.Join(args, " "), "If-None-Match") {
				t.Fatalf("expected a conditional request, got %v", args)
			}
			return commandResult{
				Stdout:   []byte("HTTP/2 304 Not Modified\nETag: \"etag-cov\"\n\n"),
				Stderr:   []byte("gh: HTTP 304"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "create GitHub observer cache temp file") {
		t.Fatalf("err = %v, want the cache-write failure", err)
	}
}

// A fresh 200 that cannot be cached must surface the write failure rather than
// pretend the response was observed durably.
func TestGpCovGetReportsUnwritableCacheForFreshResponse(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	request := gpCovRequest("6", "repos/acme/app/branches/main")
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, request.Query, request.Accept)
	cachePath := filepath.Join(stateDir, "cache", key+".json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the cache file belongs makes the atomic rename fail.
	if err := os.Mkdir(cachePath, 0o700); err != nil {
		t.Fatal(err)
	}

	observer := &Observer{
		StateDir: stateDir,
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	_, err := observer.Get(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "activate GitHub observer cache") {
		t.Fatalf("err = %v, want the cache activation failure", err)
	}
}

// Any status other than 200/304 is a hard error; the caller must not treat an
// unparsed 404 as a successful observation.
func TestGpCovGetRejectsUnexpectedHTTPStatus(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{Stdout: []byte("HTTP/2 404 Not Found\n\n{\"message\":\"Not Found\"}")}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !strings.Contains(err.Error(), "unexpected HTTP 404") {
		t.Fatalf("err = %v, want the unexpected-status refusal", err)
	}
}

// A request Accept header must reach the gh command line verbatim.
func TestGpCovGetForwardsAcceptHeaderToGh(t *testing.T) {
	t.Parallel()
	var observed []string
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			observed = append([]string(nil), args...)
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	request := GetRequest{Endpoint: "user", Accept: "application/vnd.github.raw+json"}
	if _, err := observer.Get(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(observed, "\n")
	if !strings.Contains(joined, "Accept: application/vnd.github.raw+json") {
		t.Fatalf("gh args = %v, want the Accept header forwarded", observed)
	}
}

// A parsed-but-failed command whose output cannot be parsed as an HTTP
// document is salvaged as a 200 carrying the raw output, so callers that only
// need JSON (gh api without --include formatting) still work.
func TestGpCovGetSalvagesUnparsableSuccessfulOutput(t *testing.T) {
	t.Parallel()
	raw := []byte("plain text without an included response")
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{Stdout: raw}
		},
	}
	response, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Cached || string(response.Body) != string(raw) {
		t.Fatalf("response = %+v, want a salvaged 200 with the raw body", response)
	}
}

// The Read retry loop must stop before its first attempt once MaxRetryElapsed
// is already spent, naming zero attempts instead of running anyway.
func TestGpCovReadStopsBeforeFirstAttemptWhenBudgetIsSpent(t *testing.T) {
	t.Parallel()
	calls := 0
	observer := &Observer{
		MaxRetryElapsed: time.Nanosecond,
		Now: func() time.Time {
			calls++
			return time.Unix(1_788_080_400, 0).UTC().Add(time.Duration(calls) * time.Second)
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			t.Fatal("no attempt may start once the retry budget is spent")
			return commandResult{}
		},
	}
	_, err := observer.Read(context.Background(), "", "api", "user")
	if err == nil {
		t.Fatal("want an error once the retry budget is already spent")
	}
	if !errors.Is(err, ErrTransientRetriesExhausted) {
		t.Fatalf("err = %v, want ErrTransientRetriesExhausted", err)
	}
	if !strings.Contains(err.Error(), "after 0 attempts") {
		t.Fatalf("err = %v, want it to name zero attempts", err)
	}
}

// A retry wait that cannot be honoured must abort the read with the sleep
// failure rather than continue into another attempt.
func TestGpCovReadReportsSleepFailure(t *testing.T) {
	t.Parallel()
	sleepErr := errors.New("clock refused to wait")
	observer := &Observer{
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return sleepErr },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{ExitCode: -1, Err: errors.New("signal: killed")}
		},
	}
	_, err := observer.Read(context.Background(), "", "api", "user")
	if err == nil || !errors.Is(err, sleepErr) {
		t.Fatalf("err = %v, want it to wrap the sleep failure", err)
	}
	if !strings.Contains(err.Error(), "retry after signal: killed") {
		t.Fatalf("err = %v, want it to name the retry cause", err)
	}
}

// A rate-limit message with no HTTP status is still a transient read failure
// worth retrying, and the recorded cause names the rate limit.
func TestGpCovReadRetriesRateLimitMessages(t *testing.T) {
	t.Parallel()
	var calls int
	telemetry := &RetryTelemetry{}
	observer := &Observer{
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{
					Stderr:   []byte("gh: API rate limit exceeded for user ID 1"),
					ExitCode: 1,
					Err:      errors.New("exit status 1"),
				}
			}
			return commandResult{Stdout: []byte(`{"number":7}`)}
		},
	}
	output, err := observer.Read(WithRetryTelemetry(context.Background(), telemetry), "", "api", "user")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(output) != `{"number":7}` {
		t.Fatalf("calls=%d output=%q", calls, output)
	}
	if telemetry.Count != 1 || telemetry.LastReason != "GitHub rate limit" {
		t.Fatalf("telemetry = %+v, want one retry caused by the rate limit", telemetry)
	}
}

// A rate-limit failure with no HTTP status must not be retried when the
// caller's own context is already done: that cancellation is authoritative.
func TestGpCovRetryableReadFailureRefusesDoneCallerContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := CommandResponse{Stderr: []byte("gh: HTTP 503"), ExitCode: 1, Err: errors.New("exit status 1")}
	cause, retryable := retryableReadFailure(ctx, false, result, "gh: HTTP 503")
	if retryable || cause != "" {
		t.Fatalf("cause=%q retryable=%v, want a done caller context to be authoritative", cause, retryable)
	}
}

// repositoryArgument must read both --repo forms so retry progress names the
// repository a caller asked about.
func TestGpCovReadReportsRepositoryFromRepoEqualsFlag(t *testing.T) {
	t.Parallel()
	var events []progress.Event
	observer := &Observer{
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			if strings.Contains(strings.Join(args, " "), "--repo=acme/app") {
				if len(events) == 0 {
					return commandResult{Stderr: []byte("gh: HTTP 503"), ExitCode: 1, Err: errors.New("exit status 1")}
				}
				return commandResult{Stdout: []byte(`{"number":42}`)}
			}
			t.Fatalf("unexpected args %v", args)
			return commandResult{}
		},
	}
	ctx := WithProgress(context.Background(), func(event progress.Event) { events = append(events, event) })
	if _, err := observer.Read(ctx, "", "pr", "view", "42", "--repo=acme/app"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Repository != "acme/app" {
		t.Fatalf("progress = %+v, want the repository reported from --repo=", events)
	}
}

// A failure message must combine stderr and stdout when both are present, so
// an operator sees everything the failed command printed.
func TestGpCovReadCombinesStderrAndStdoutInFailureMessage(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("an authoritative failure must not be retried")
			return nil
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("extra detail"),
				Stderr:   []byte("gh: boom"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Read(context.Background(), "", "api", "user")
	if err == nil || !strings.Contains(err.Error(), "gh: boom: extra detail") {
		t.Fatalf("err = %v, want stderr and stdout combined", err)
	}
}

// A net timeout surfaces as a retryable read failure even without any HTTP
// status in the output, and the recorded cause names the timeout.
func TestGpCovReadRetriesNetworkTimeouts(t *testing.T) {
	t.Parallel()
	var calls int
	telemetry := &RetryTelemetry{}
	observer := &Observer{
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{ExitCode: -1, Err: os.ErrDeadlineExceeded}
			}
			return commandResult{Stdout: []byte(`{"number":9}`)}
		},
	}
	output, err := observer.Read(WithRetryTelemetry(context.Background(), telemetry), "", "api", "user")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(output) != `{"number":9}` {
		t.Fatalf("calls=%d output=%q", calls, output)
	}
	if telemetry.Count != 1 || !strings.Contains(telemetry.LastReason, "i/o timeout") {
		t.Fatalf("telemetry = %+v, want the network timeout recorded", telemetry)
	}
}

// A killed attempt whose output says nothing recognisable still gets the
// generic temporary-network cause, and the exhausted error names it.
func TestGpCovReadNamesGenericTemporaryNetworkCause(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		MaxAttempts:          2,
		MinAPIAttemptTimeout: 20 * time.Millisecond,
		Sleep:                func(context.Context, time.Duration) error { return nil },
		RandomIntn:           func(int64) int64 { return 0 },
		Run: func(ctx context.Context, _ string, _ ...string) commandResult {
			<-ctx.Done()
			return commandResult{ExitCode: -1, Err: errors.New("exit status 1")}
		},
	}
	_, err := observer.Read(context.Background(), "", "api", "repos/acme/app/pulls/1")
	if err == nil || !errors.Is(err, ErrTransientRetriesExhausted) {
		t.Fatalf("err = %v, want ErrTransientRetriesExhausted", err)
	}
	if !strings.Contains(err.Error(), "temporary network failure") {
		t.Fatalf("err = %v, want the generic temporary-network cause", err)
	}
}

// isTemporaryCommandFailure's allow/deny decisions must agree with the
// documented rules: no error is never retryable, a done caller context is
// authoritative, and a net timeout is retryable.
func TestGpCovIsTemporaryCommandFailureClassifiesFailures(t *testing.T) {
	t.Parallel()
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, testCase := range []struct {
		name     string
		ctx      context.Context
		timedOut bool
		err      error
		message  string
		want     bool
	}{
		{"no error", live, false, nil, "", false},
		{"caller context done", cancelled, false, errors.New("dial tcp: connection refused"), "connection refused", false},
		{"net timeout", live, false, os.ErrDeadlineExceeded, "", true},
		{"timed out attempt", live, true, errors.New("exit status 1"), "", true},
		{"signal killed", live, false, errors.New("signal: killed"), "", true},
		{"connection refused text", live, false, errors.New("exit status 1"), "dial tcp: connection refused", true},
		{"ordinary failure", live, false, errors.New("exit status 1"), "gh: HTTP 404: Not Found", false},
	} {
		if got := isTemporaryCommandFailure(testCase.ctx, testCase.timedOut, testCase.err, testCase.message); got != testCase.want {
			t.Errorf("%s: isTemporaryCommandFailure = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// The apiGet retry loop must stop before its first attempt once the budget is
// spent, reporting that GitHub never answered.
func TestGpCovApiGetStopsBeforeFirstAttemptWhenBudgetIsSpent(t *testing.T) {
	t.Parallel()
	calls := 0
	observer := &Observer{
		StateDir:        t.TempDir(),
		MaxRetryElapsed: time.Nanosecond,
		Now: func() time.Time {
			calls++
			return time.Unix(1_788_080_400, 0).UTC().Add(time.Duration(calls) * time.Second)
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			t.Fatal("no gh invocation may start once the retry budget is spent")
			return commandResult{}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !strings.Contains(err.Error(), "did not return a response for user") {
		t.Fatalf("err = %v, want the no-response failure without running gh", err)
	}
}

// The per-attempt timeout must be clamped to the little budget that remains
// rather than handed the full 30s floor.
func TestGpCovGetClampsAttemptTimeoutToRemainingBudget(t *testing.T) {
	t.Parallel()
	calls := 0
	var attemptTimeout time.Duration
	observer := &Observer{
		StateDir:        t.TempDir(),
		MaxRetryElapsed: 100 * time.Millisecond,
		Now: func() time.Time {
			calls++
			return time.Unix(1_788_080_400, 0).UTC().Add(time.Duration(calls) * 90 * time.Millisecond)
		},
		Run: func(ctx context.Context, _ string, _ ...string) commandResult {
			if deadline, ok := ctx.Deadline(); ok {
				attemptTimeout = time.Until(deadline)
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	response, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` {
		t.Fatalf("response = %q", response.Body)
	}
	if attemptTimeout <= 0 || attemptTimeout >= 30*time.Second {
		t.Fatalf("attempt timeout = %v, want it clamped below the 30s floor to the remaining budget", attemptTimeout)
	}
}

// A retryable HTTP status must stop retrying once MaxAttempts is reached and
// report the exhausted budget as transient.
func TestGpCovGetFailsAfterRetryableStatusExhaustsAttempts(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 1,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("the single allowed attempt must not sleep")
			return nil
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 503 Service Unavailable\n\n{\"message\":\"temporary\"}"),
				Stderr:   []byte("gh: HTTP 503"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !errors.Is(err, ErrTransientRetriesExhausted) {
		t.Fatalf("err = %v, want ErrTransientRetriesExhausted", err)
	}
	if !strings.Contains(err.Error(), "returned HTTP 503 after 1 attempts") {
		t.Fatalf("err = %v, want the attempt count reported", err)
	}
}

// A retry wait that fails while recovering from a retryable HTTP status must
// abort with the sleep failure.
func TestGpCovGetReportsSleepFailureForRetryableStatus(t *testing.T) {
	t.Parallel()
	sleepErr := errors.New("no wait possible")
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return sleepErr },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 503 Service Unavailable\n\n{\"message\":\"temporary\"}"),
				Stderr:   []byte("gh: HTTP 503"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !errors.Is(err, sleepErr) {
		t.Fatalf("err = %v, want it to wrap the sleep failure", err)
	}
	if !strings.Contains(err.Error(), "retry after HTTP 503") {
		t.Fatalf("err = %v, want it to name the retry cause", err)
	}
}

// When a retryable status is recovered on a later attempt and the budget runs
// out meanwhile, the last retry error is what the caller sees.
func TestGpCovApiGetReturnsLastRetryErrorWhenBudgetRunsOut(t *testing.T) {
	t.Parallel()
	calls := 0
	clock := 0
	observer := &Observer{
		StateDir:        t.TempDir(),
		MaxRetryElapsed: 3 * time.Second,
		Sleep:           func(context.Context, time.Duration) error { return nil },
		RandomIntn:      func(int64) int64 { return 0 },
		Now: func() time.Time {
			clock++
			return time.Unix(1_788_080_400, 0).UTC().Add(time.Duration(clock) * time.Second)
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{
				Stdout:   []byte("HTTP/2 503 Service Unavailable\n\n{\"message\":\"temporary\"}"),
				Stderr:   []byte("gh: HTTP 503"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !errors.Is(err, ErrTransientRetriesExhausted) {
		t.Fatalf("err = %v, want ErrTransientRetriesExhausted", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err = %v, want the last retry cause", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly one attempt before the budget ran out", calls)
	}
}

// An unparsable gh failure whose stderr is empty must fall back to stdout so
// the operator still sees the diagnostic.
func TestGpCovApiGetFallsBackToStdoutWhenStderrIsEmpty(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("gh: unexpected server failure"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !strings.Contains(err.Error(), "gh: unexpected server failure") {
		t.Fatalf("err = %v, want the stdout diagnostic in the failure", err)
	}
}

// A temporary command failure must exhaust attempts as transient, naming the
// cause.
func TestGpCovGetFailsAfterTemporaryCommandFailureExhaustsAttempts(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 1,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("the single allowed attempt must not sleep")
			return nil
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{ExitCode: -1, Err: errors.New("signal: killed")}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !errors.Is(err, ErrTransientRetriesExhausted) {
		t.Fatalf("err = %v, want ErrTransientRetriesExhausted", err)
	}
	if !strings.Contains(err.Error(), "failed temporarily") || !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("err = %v, want the temporary cause named", err)
	}
}

// A retry wait that fails while recovering from a temporary command failure
// must abort with the sleep failure.
func TestGpCovGetReportsSleepFailureForTemporaryCommandFailure(t *testing.T) {
	t.Parallel()
	sleepErr := errors.New("no wait possible")
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return sleepErr },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{ExitCode: -1, Err: errors.New("signal: killed")}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !errors.Is(err, sleepErr) {
		t.Fatalf("err = %v, want it to wrap the sleep failure", err)
	}
	if !strings.Contains(err.Error(), "retry after signal: killed") {
		t.Fatalf("err = %v, want it to name the retry cause", err)
	}
}

// A command that reports no error but an empty body and a nonzero exit status
// leaves only the parse failure to report.
func TestGpCovApiGetReturnsParseErrorWhenCommandReportsNoError(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{ExitCode: 1}
		},
	}
	_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err == nil || !strings.Contains(err.Error(), "empty GitHub response") {
		t.Fatalf("err = %v, want the parse failure", err)
	}
}

// readCacheEntry must distinguish an unreadable path, a corrupt document, and
// a stale schema version, and must treat a missing file as an ordinary miss.
func TestGpCovReadCacheEntryClassifiesFailures(t *testing.T) {
	t.Parallel()
	if entry, err := readCacheEntry(filepath.Join(t.TempDir(), "missing.json")); err != nil || entry != nil {
		t.Fatalf("missing cache = (%+v, %v), want an ordinary miss", entry, err)
	}

	if _, err := readCacheEntry(t.TempDir()); err == nil || !strings.Contains(err.Error(), "read GitHub observer cache") {
		t.Fatalf("directory cache err = %v, want a read failure", err)
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCacheEntry(corrupt); err == nil || !strings.Contains(err.Error(), "decode GitHub observer cache") {
		t.Fatalf("corrupt cache err = %v, want a decode failure", err)
	}

	stale := filepath.Join(t.TempDir(), "stale.json")
	raw, err := json.Marshal(&cacheEntry{SchemaVersion: cacheSchemaVersion + 1, Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if entry, err := readCacheEntry(stale); err != nil || entry != nil {
		t.Fatalf("stale-schema cache = (%+v, %v), want an ordinary miss", entry, err)
	}
}

// writeCacheEntry must report an unusable parent directory and an
// unactivatable destination instead of silently losing the cache write.
func TestGpCovWriteCacheEntryReportsFailures(t *testing.T) {
	t.Parallel()
	entry := &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Endpoint:      "user",
		Body:          []byte(`{"ok":true}`),
		BodySHA256:    digest([]byte(`{"ok":true}`)),
		ObservedAt:    time.Now().UTC(),
	}

	missingParent := filepath.Join(t.TempDir(), "absent", "cache.json")
	if err := writeCacheEntry(missingParent, entry); err == nil ||
		!strings.Contains(err.Error(), "create GitHub observer cache temp file") {
		t.Fatalf("missing-parent err = %v, want a temp-file failure", err)
	}

	stateDir := t.TempDir()
	directoryTarget := filepath.Join(stateDir, "cache.json")
	if err := os.Mkdir(directoryTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCacheEntry(directoryTarget, entry); err == nil ||
		!strings.Contains(err.Error(), "activate GitHub observer cache") {
		t.Fatalf("directory-target err = %v, want an activation failure", err)
	}
}

// normalizeHeaders must lower-case and trim header names, keep values intact,
// and fall back to the cached validators only when the response omitted them.
func TestGpCovNormalizeHeadersPrefersResponseThenCache(t *testing.T) {
	t.Parallel()
	entry := &cacheEntry{ETag: `"cached"`, LastModified: "cached-lm"}

	got := normalizeHeaders(map[string]string{}, entry)
	if got["etag"] != `"cached"` || got["last-modified"] != "cached-lm" {
		t.Fatalf("empty response headers = %#v, want the cached validators", got)
	}

	got = normalizeHeaders(map[string]string{"ETag": `"fresh"`, "LAST-MODIFIED": "fresh-lm"}, entry)
	if got["etag"] != `"fresh"` || got["last-modified"] != "fresh-lm" {
		t.Fatalf("response headers = %#v, want them preferred over the cache", got)
	}

	got = normalizeHeaders(map[string]string{"X-Other": " value "}, nil)
	if got["x-other"] != " value " {
		t.Fatalf("nil entry headers = %#v, want the name normalised and the value kept", got)
	}
}

// parseIncludedResponse must reject empty, headerless, and malformed status
// lines, skip blank and colonless header lines, and normalise CRLF.
func TestGpCovParseIncludedResponseRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "   ", "empty GitHub response"},
		{"no separator", "HTTP/2 200 OK", "omitted headers"},
		{"short status line", "HTTP/2\n\nbody", "status line malformed"},
		{"non-numeric status", "HTTP/2 OK\n\nbody", "status line malformed"},
	} {
		if _, err := parseIncludedResponse([]byte(testCase.raw)); err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: err = %v, want %q", testCase.name, err, testCase.want)
		}
	}

	raw := "HTTP/2 200 OK\r\n   \r\nnovalue\r\n:emptyname\r\nX-Test: yes\r\n\r\npayload"
	response, err := parseIncludedResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(response.Body) != "payload" {
		t.Fatalf("response = %+v, want a 200 with the payload body", response)
	}
	if len(response.Headers) != 1 || response.Headers["x-test"] != "yes" {
		t.Fatalf("headers = %#v, want only the well-formed header", response.Headers)
	}
}

// parseRetryAfter accepts positive seconds and HTTP dates in the future, and
// returns zero for anything else.
func TestGpCovParseRetryAfterTable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"   ", 0},
		{"not-a-delay", 0},
		{"-5", 0},
		{"0", 0},
		{"4", 4 * time.Second},
		{"Sun, 30 Aug 2026 10:00:04 GMT", 4 * time.Second},
		{"Sun, 30 Aug 2026 09:59:00 GMT", 0},
	} {
		if got := parseRetryAfter(testCase.value, now); got != testCase.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
}

// parseRateLimitReset accepts a future unix timestamp and returns zero for
// malformed, non-positive, and already-elapsed resets.
func TestGpCovParseRateLimitResetTable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"abc", 0},
		{"-1", 0},
		{"0", 0},
		{"1788084005", 5 * time.Second},
		{"1788083990", 0},
	} {
		if got := parseRateLimitReset(testCase.value, now); got != testCase.want {
			t.Errorf("parseRateLimitReset(%q) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
}

// httpTime accepts every documented HTTP date layout and rejects garbage.
func TestGpCovHTTPTimeParsesSupportedLayouts(t *testing.T) {
	t.Parallel()
	want := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)
	for _, value := range []string{
		"Mon, 02 Jan 2006 15:04:05 UTC",
		"Mon, 02 Jan 2006 15:04:05 +0000",
		"Monday, 02-Jan-06 15:04:05 UTC",
		"Mon Jan  2 15:04:05 2006",
	} {
		got, err := httpTime(value)
		if err != nil {
			t.Errorf("httpTime(%q) err = %v", value, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("httpTime(%q) = %s, want %s", value, got, want)
		}
	}
	if _, err := httpTime("garbage"); err == nil || !strings.Contains(err.Error(), "invalid HTTP time") {
		t.Fatalf("httpTime(garbage) err = %v, want an invalid-time failure", err)
	}
}

// retryDelay must stop doubling once the configured maximum backoff is
// reached instead of growing without bound.
func TestGpCovRetryDelayCapsAtMaxBackoff(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		BaseBackoff: time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
		RandomIntn:  func(max int64) int64 { return max - 1 },
	}
	delay, reason := observer.retryDelay(10, nil)
	if delay != 2*time.Millisecond {
		t.Fatalf("delay = %v, want it capped at MaxBackoff", delay)
	}
	if reason != "exponential full-jitter backoff" {
		t.Fatalf("reason = %q, want the jitter backoff", reason)
	}
}

// A non-positive delay is not a wait at all, and an already-cancelled caller
// must be able to interrupt a real wait.
func TestGpCovSleepSkipsNonPositiveDelaysAndHonoursCancellation(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("a non-positive delay must not reach the sleep function")
			return nil
		},
	}
	if err := observer.sleep(context.Background(), 0); err != nil {
		t.Fatalf("sleep(0) = %v, want nil", err)
	}
	if err := observer.sleep(context.Background(), -time.Second); err != nil {
		t.Fatalf("sleep(-1s) = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Observer{}).sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep on a cancelled context = %v, want context.Canceled", err)
	}
}

// randomIntn must not consult its source for non-positive bounds.
func TestGpCovRandomIntnDefaultsForNonPositiveBounds(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		RandomIntn: func(int64) int64 {
			t.Fatal("a non-positive bound must not reach the random source")
			return 0
		},
	}
	if got := observer.randomIntn(0); got != 0 {
		t.Fatalf("randomIntn(0) = %d, want 0", got)
	}
	if got := observer.randomIntn(-3); got != 0 {
		t.Fatalf("randomIntn(-3) = %d, want 0", got)
	}
}

// maxBackoff must prefer the configured cap over the default.
func TestGpCovMaxBackoffPrefersConfiguredValue(t *testing.T) {
	t.Parallel()
	if got := (&Observer{MaxBackoff: 7 * time.Second}).maxBackoff(); got != 7*time.Second {
		t.Fatalf("maxBackoff() = %v, want the configured 7s", got)
	}
	if got := (&Observer{}).maxBackoff(); got != defaultMaxBackoff {
		t.Fatalf("maxBackoff() = %v, want the default", got)
	}
}

// Only `gh api` and `gh pr view` earn the longer per-attempt floor.
func TestGpCovIsAPIOrPRViewCommandClassifiesArguments(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"api", "user"}, true},
		{[]string{"pr", "view", "42"}, true},
		{[]string{"pr", "list"}, false},
		{[]string{"pr"}, false},
		{[]string{"repo", "view", "acme/app"}, false},
	} {
		if got := isAPIOrPRViewCommand(testCase.args); got != testCase.want {
			t.Errorf("isAPIOrPRViewCommand(%v) = %v, want %v", testCase.args, got, testCase.want)
		}
	}
}

// stateDir must resolve, in order, an explicit setting, XDG_STATE_HOME, the
// user's home directory, and finally a relative fallback.
func TestGpCovStateDirResolutionOrder(t *testing.T) {
	if got := (&Observer{StateDir: "/tmp/explicit"}).stateDir(); got != "/tmp/explicit" {
		t.Fatalf("stateDir() = %q, want the explicit setting", got)
	}

	t.Run("XDG_STATE_HOME", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_STATE_HOME", xdg)
		want := filepath.Join(xdg, "wb", "github-observer")
		if got := (&Observer{StateDir: "   "}).stateDir(); got != want {
			t.Fatalf("stateDir() = %q, want %q", got, want)
		}
	})

	t.Run("home directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		want := filepath.Join(home, ".local", "state", "wb", "github-observer")
		if got := (&Observer{}).stateDir(); got != want {
			t.Fatalf("stateDir() = %q, want %q", got, want)
		}
	})

	t.Run("relative fallback", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
		if got := (&Observer{}).stateDir(); got != filepath.Join(".wb", "github-observer") {
			t.Fatalf("stateDir() = %q, want the relative fallback", got)
		}
	})
}

// runGH must report a real gh failure's exit status, its captured streams, and
// fall back to exit code 1 when the process could not be started at all.
func TestGpCovRunGHReportsExitStatusAndStartFailure(t *testing.T) {
	binDir := t.TempDir()
	ghPath := filepath.Join(binDir, "gh")
	script := "#!/bin/sh\nprintf 'stdout-line'\nprintf 'stderr-line' >&2\nexit 3\n"
	if err := testenv.WriteExecutableFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	result := runGH(context.Background(), "", "api", "user")
	if result.Err == nil {
		t.Fatal("a nonzero gh exit must be reported as an error")
	}
	if result.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want the gh exit status 3", result.ExitCode)
	}
	if string(result.Stdout) != "stdout-line" || string(result.Stderr) != "stderr-line" {
		t.Fatalf("stdout = %q stderr = %q, want both streams captured", result.Stdout, result.Stderr)
	}

	if err := testenv.WriteExecutableFile(ghPath, []byte("#!/bin/sh\nprintf 'ok'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result = runGH(context.Background(), "", "version")
	if result.Err != nil || result.ExitCode != 0 || string(result.Stdout) != "ok" {
		t.Fatalf("successful run = %+v", result)
	}

	t.Setenv("PATH", t.TempDir())
	result = runGH(context.Background(), "", "version")
	if result.Err == nil {
		t.Fatal("a missing gh binary must be reported as an error")
	}
	if result.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want the generic 1 for a process that never started", result.ExitCode)
	}
}

// attemptContext must not shorten a caller's own deadline; only its
// cancellation propagates.
func TestGpCovAttemptContextPreservesCallerDeadline(t *testing.T) {
	t.Parallel()
	parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
	t.Cleanup(func() { cancelParent() })
	parentDeadline, _ := parent.Deadline()

	ctx, cancel := (&Observer{}).attemptContext(parent, time.Millisecond)
	t.Cleanup(func() { cancel() })
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(parentDeadline) {
		t.Fatalf("attempt deadline = %v (ok=%v), want the caller's %v", deadline, ok, parentDeadline)
	}
}

// A lock path that cannot be opened must fail with a message naming the lock.
func TestGpCovAcquireLockRejectsAnUnopenablePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := acquireLock(dir); err == nil || !strings.Contains(err.Error(), "open GitHub observer lock") {
		t.Fatalf("err = %v, want the lock-open failure", err)
	}
}

// A lock that is acquired then released must leave a real, re-acquirable lock
// file behind.
func TestGpCovAcquireLockRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "round-trip.lock")
	unlock, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock = %v", err)
	}
	second, err := acquireLock(path)
	if err != nil {
		t.Fatalf("re-acquire after unlock = %v", err)
	}
	if err := second(); err != nil {
		t.Fatalf("second unlock = %v", err)
	}
}

// GetPages must propagate a page read failure instead of returning a partial
// walk as success.
func TestGpCovGetPagesPropagatesPageReadFailure(t *testing.T) {
	t.Parallel()
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 401 Unauthorized\n\n{\"message\":\"Bad credentials\"}"),
				Stderr:   []byte("gh: Bad credentials"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}
	pages, err := observer.GetPages(context.Background(), GetRequest{Endpoint: "repos/acme/app/rules"}, 0)
	if err == nil || pages != nil {
		t.Fatalf("pages = %#v err = %v, want no pages and the read failure", pages, err)
	}
}

// A link section without a bracketed target is not a next link.
func TestGpCovNextPageEndpointSkipsSectionsWithoutABracketedTarget(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"unbracketed target", map[string]string{"link": `bare-target; rel="next"`}, ""},
		{"partial bracket", map[string]string{"link": `<half; rel="next"`}, ""},
		{"unbracketed then bracketed", map[string]string{"link": `bare; rel="next", <https://api.github.com/a?page=2>; rel="next"`}, "https://api.github.com/a?page=2"},
	} {
		if got := NextPageEndpoint(testCase.headers); got != testCase.want {
			t.Errorf("%s: NextPageEndpoint = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// A progress detail must name the delay reason when the server supplied one.
func TestGpCovRetryProgressNamesTheDelayReason(t *testing.T) {
	t.Parallel()
	var events []progress.Event
	reportRetryProgress(func(event progress.Event) { events = append(events, event) }, "acme/app", "github_api", 1, 4, "HTTP 503", time.Second, "Retry-After")
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one report", events)
	}
	if !strings.Contains(events[0].Detail, "(Retry-After)") {
		t.Fatalf("detail = %q, want the delay reason named", events[0].Detail)
	}
}

// A reporter attached to the context must receive retry progress for a
// GetRequest with no explicit Progress, so the ctx seam is honoured.
func TestGpCovGetUsesProgressFromContext(t *testing.T) {
	t.Parallel()
	var events []progress.Event
	calls := 0
	observer := &Observer{
		StateDir:    t.TempDir(),
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		RandomIntn:  func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{
					Stdout:   []byte("HTTP/2 503 Service Unavailable\n\n{\"message\":\"temporary\"}"),
					Stderr:   []byte("gh: HTTP 503"),
					ExitCode: 1,
					Err:      errors.New("exit status 1"),
				}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	ctx := WithProgress(context.Background(), func(event progress.Event) { events = append(events, event) })
	response, err := observer.Get(ctx, gpCovRequest("7", "repos/acme/app/branches/main"))
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` {
		t.Fatalf("response = %q", response.Body)
	}
	if len(events) != 1 || events[0].Operation != "github_api" || events[0].Repository != "acme/app" {
		t.Fatalf("progress = %+v, want the api retry reported through the context sink", events)
	}
}

// A 200 response must be cached with the validators it arrived with, and a
// subsequent read served from that cache before the fresh window elapses.
func TestGpCovGetCachesFreshResponseWithValidators(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	calls := 0
	observer := &Observer{
		StateDir: stateDir,
		Now:      func() time.Time { return now },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{
				Stdout: []byte("HTTP/2 200 OK\nETag: \"etag-cache\"\nLast-Modified: Wed, 03 Sep 2026 08:59:00 GMT\n\n{\"ok\":true}"),
			}
		},
	}
	request := gpCovRequest("8", "repos/acme/app/branches/main")
	request.FreshWindow = time.Hour
	first, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached {
		t.Fatal("first read must not report a cache hit")
	}
	second, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached || string(second.Body) != `{"ok":true}` || second.Headers["etag"] != `"etag-cache"` {
		t.Fatalf("second response = %+v, want a cache hit carrying the cached validators", second)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want the fresh window to avoid a second gh invocation", calls)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "cache")); statErr != nil {
		t.Fatalf("cache directory missing: %v", statErr)
	}
}

// A malformed link header with no rel=next parameter must end the walk.
func TestGpCovNextPageEndpointIgnoresOtherRelationsInAMixedHeader(t *testing.T) {
	t.Parallel()
	headers := map[string]string{
		"Link": `<https://api.github.com/a?page=1>; rel="first", <https://api.github.com/a?page=9>; rel="last"`,
	}
	if got := NextPageEndpoint(headers); got != "" {
		t.Fatalf("NextPageEndpoint = %q, want no next page", got)
	}
}

// A failed retryable read must record its cause through the telemetry seam
// even when the retry eventually succeeds, and an unrelated failure must not.
func TestGpCovReadTelemetryIgnoresAuthoritativeFailures(t *testing.T) {
	t.Parallel()
	telemetry := &RetryTelemetry{}
	observer := &Observer{
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("an authoritative failure must not be retried")
			return nil
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{Stderr: []byte("gh: HTTP 404: Not Found"), ExitCode: 1, Err: errors.New("exit status 1")}
		},
	}
	ctx := WithRetryTelemetry(context.Background(), telemetry)
	if _, err := observer.Read(ctx, "", "api", "user"); err == nil {
		t.Fatal("want the 404 to be reported")
	}
	if telemetry.Count != 0 || telemetry.LastReason != "" {
		t.Fatalf("telemetry = %+v, want no retry recorded for an authoritative failure", telemetry)
	}
}
