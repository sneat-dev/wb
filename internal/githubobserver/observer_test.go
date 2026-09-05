package githubobserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestGetRevalidatesStaleCacheWithConditionalHeaders(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	request := GetRequest{
		Repository: "acme/app",
		Target:     "main",
		Head:       strings.Repeat("a", 40),
		Endpoint:   "repos/acme/app/branches/main",
	}
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, nil, "")
	cachePath := filepath.Join(stateDir, "cache", key+".json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Repository:    request.Repository,
		Target:        request.Target,
		Head:          request.Head,
		Endpoint:      request.Endpoint,
		RequestHash:   requestHash(request.Endpoint, nil, ""),
		StatusCode:    200,
		Body:          []byte(`{"name":"main","protected":true}`),
		BodySHA256:    digest([]byte(`{"name":"main","protected":true}`)),
		ETag:          `"etag-1"`,
		LastModified:  "Sun, 30 Aug 2026 09:59:00 GMT",
		ObservedAt:    now.Add(-10 * time.Second),
	}
	if err := writeCacheEntry(cachePath, entry); err != nil {
		t.Fatal(err)
	}
	observer := &Observer{
		StateDir: stateDir,
		Now:      func() time.Time { return now },
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, `If-None-Match: "etag-1"`) {
				t.Fatalf("conditional request missing ETag header: %v", args)
			}
			return commandResult{
				Stdout:   []byte("HTTP/2 304 Not Modified\nETag: \"etag-1\"\nLast-Modified: Sun, 30 Aug 2026 09:59:00 GMT\n\n"),
				Stderr:   []byte("gh: HTTP 304"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}

	response, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"name":"main","protected":true}` || !response.Cached || response.StatusCode != 200 {
		t.Fatalf("response = %+v", response)
	}
	updated, err := readCacheEntry(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ObservedAt != now {
		t.Fatalf("ObservedAt = %s, want %s", updated.ObservedAt, now)
	}
}

func TestGetDoesNotAcceptFailedCommandForFreshResponse(t *testing.T) {
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 200 OK\n\n{\"ok\":true}"),
				Stderr:   []byte("gh: transport failure"),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}

	_, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("h", 40),
		Endpoint:    "user",
		FreshWindow: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("err=%v, want failed command for parsed HTTP 200", err)
	}
}

func TestGetDoesNotAcceptNonzeroExitCodeWithoutCommandError(t *testing.T) {
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return commandResult{
				Stdout:   []byte("HTTP/2 200 OK\n\n{\"ok\":true}"),
				ExitCode: 1,
			}
		},
	}

	_, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("i", 40),
		Endpoint:    "user",
		FreshWindow: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("err=%v, want failed command for nonzero exit code", err)
	}
}

func TestGetHonoursRateLimitResetAndJitterBackoff(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  int
		sleeps []time.Duration
	)
	now := time.Unix(1_788_080_400, 0).UTC()
	observer := &Observer{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		Sleep: func(_ context.Context, delay time.Duration) error {
			mu.Lock()
			sleeps = append(sleeps, delay)
			mu.Unlock()
			return nil
		},
		RandomIntn: func(max int64) int64 {
			if max <= 0 {
				return 0
			}
			return max / 4
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				reset := now.Add(3 * time.Second).Unix()
				return commandResult{Stdout: []byte(fmt.Sprintf("HTTP/2 403 Forbidden\nRetry-After: 1\nX-RateLimit-Reset: %d\n\n{\"message\":\"secondary rate limit\"}", reset)), Err: errors.New("exit status 1"), ExitCode: 1}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\nETag: \"etag-2\"\n\n{\"ok\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("b", 40),
		Endpoint:    "repos/acme/app/compare/base...head",
		FreshWindow: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` || calls != 2 {
		t.Fatalf("calls=%d response=%s", calls, response.Body)
	}
	if len(sleeps) != 1 || sleeps[0] < 3*time.Second {
		t.Fatalf("sleeps=%v, want one wait of at least the rate-limit reset", sleeps)
	}
}

func TestGetHonoursRetryAfterHTTPDateWithInjectedClock(t *testing.T) {
	var sleeps []time.Duration
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	observer := &Observer{
		StateDir: t.TempDir(),
		Now:      func() time.Time { return now },
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		RandomIntn: func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			if len(sleeps) == 0 {
				return commandResult{
					Stdout:   []byte("HTTP/2 403 Forbidden\nRetry-After: Sun, 30 Aug 2026 10:00:04 GMT\n\n{\"message\":\"secondary rate limit\"}"),
					Err:      errors.New("exit status 1"),
					ExitCode: 1,
				}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("e", 40),
		Endpoint:    "repos/acme/app/branches/main",
		FreshWindow: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` {
		t.Fatalf("response=%s", response.Body)
	}
	if len(sleeps) != 1 || sleeps[0] != 4*time.Second {
		t.Fatalf("sleeps=%v, want one injected-clock Retry-After wait of 4s", sleeps)
	}
}

func TestGetRetriesTransientGitHubServiceFailuresWithProgress(t *testing.T) {
	statuses := []int{502, 503, 504, 200}
	var sleeps []time.Duration
	var events []progress.Event
	observer := &Observer{
		StateDir: t.TempDir(),
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		RandomIntn: func(max int64) int64 { return max - 1 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			status := statuses[0]
			statuses = statuses[1:]
			if status == 200 {
				return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
			}
			return commandResult{
				Stdout:   []byte(fmt.Sprintf("HTTP/2 %d Service Unavailable\n\n{\"message\":\"temporary\"}", status)),
				Stderr:   []byte(fmt.Sprintf("gh: HTTP %d", status)),
				ExitCode: 1,
				Err:      errors.New("exit status 1"),
			}
		},
	}

	response, err := observer.Get(context.Background(), GetRequest{
		Repository: "acme/app",
		Target:     "main",
		Head:       strings.Repeat("c", 40),
		Endpoint:   "repos/acme/app/compare/base...head",
		Progress:   func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` || len(statuses) != 0 {
		t.Fatalf("response=%s remaining statuses=%v", response.Body, statuses)
	}
	wantSleeps := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}
	if !slices.Equal(sleeps, wantSleeps) {
		t.Fatalf("sleeps=%v, want %v", sleeps, wantSleeps)
	}
	if len(events) != 3 {
		t.Fatalf("progress events=%d, want 3: %+v", len(events), events)
	}
	for index, event := range events {
		wantStatus := 502 + index
		if event.Operation != "github_api" || event.Phase != "retry" || event.Repository != "acme/app" || event.State != progress.Waiting {
			t.Fatalf("event %d metadata=%+v", index, event)
		}
		for _, fragment := range []string{fmt.Sprintf("attempt %d/4", index+1), fmt.Sprintf("HTTP %d", wantStatus), wantSleeps[index].String()} {
			if !strings.Contains(event.Detail, fragment) {
				t.Fatalf("event %d detail=%q, missing %q", index, event.Detail, fragment)
			}
		}
	}
}

func TestGetRetriesTemporaryNetworkFailure(t *testing.T) {
	var calls int
	var sleeps []time.Duration
	observer := &Observer{
		StateDir: t.TempDir(),
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		RandomIntn: func(int64) int64 { return 0 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{Stderr: []byte("error connecting to api.github.com"), ExitCode: 1, Err: errors.New("exit status 1")}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(response.Body) != `{"ok":true}` || len(sleeps) != 1 {
		t.Fatalf("calls=%d sleeps=%v response=%s", calls, sleeps, response.Body)
	}
}

func TestGetDoesNotRetryAuthenticationOrOrdinaryForbiddenFailures(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("HTTP_%d", status), func(t *testing.T) {
			calls := 0
			observer := &Observer{
				StateDir: t.TempDir(),
				Sleep: func(_ context.Context, _ time.Duration) error {
					t.Fatal("non-retryable failure slept")
					return nil
				},
				Run: func(_ context.Context, _ string, _ ...string) commandResult {
					calls++
					return commandResult{
						Stdout: []byte(fmt.Sprintf("HTTP/2 %d Forbidden\n\n{\"message\":\"authentication required\"}", status)),
						Stderr: []byte("gh: authentication required"), ExitCode: 1, Err: errors.New("exit status 1"),
					}
				},
			}
			_, err := observer.Get(context.Background(), GetRequest{Endpoint: "user"})
			if err == nil || calls != 1 {
				t.Fatalf("err=%v calls=%d, want one terminal attempt", err, calls)
			}
		})
	}
}

func TestReadRetriesTransientFailureAndReportsProgress(t *testing.T) {
	var calls int
	var sleeps []time.Duration
	var events []progress.Event
	observer := &Observer{
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		RandomIntn: func(max int64) int64 { return max - 1 },
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			if calls == 1 {
				return commandResult{Stderr: []byte("gh: HTTP 502"), ExitCode: 1, Err: errors.New("exit status 1")}
			}
			return commandResult{Stdout: []byte(`{"number":42}`)}
		},
	}
	ctx := WithProgress(context.Background(), func(event progress.Event) { events = append(events, event) })

	output, err := observer.Read(ctx, "", "pr", "view", "42", "--repo", "acme/app", "--json", "number")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || string(output) != `{"number":42}` || !slices.Equal(sleeps, []time.Duration{250 * time.Millisecond}) {
		t.Fatalf("calls=%d sleeps=%v output=%s", calls, sleeps, output)
	}
	if len(events) != 1 || events[0].Operation != "github_read" || events[0].Repository != "acme/app" || !strings.Contains(events[0].Detail, "HTTP 502") {
		t.Fatalf("progress=%+v", events)
	}
}

func TestReadDoesNotRetryOrdinaryForbiddenFailure(t *testing.T) {
	var calls int
	observer := &Observer{
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("ordinary forbidden failure slept")
			return nil
		},
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{Stderr: []byte("gh: HTTP 403: resource not accessible"), ExitCode: 1, Err: errors.New("exit status 1")}
		},
	}
	_, err := observer.Read(context.Background(), "", "repo", "view", "acme/app")
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one terminal attempt", err, calls)
	}
}

func TestGetForcesMethodGETForQueryRequests(t *testing.T) {
	var observed []string
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			observed = append([]string(nil), args...)
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("d", 40),
		Endpoint:    "repos/acme/app/pulls",
		Query:       map[string]string{"head": "acme:branch", "state": "open"},
		FreshWindow: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"ok":true}` {
		t.Fatalf("response=%s", response.Body)
	}
	methodIndex := slices.Index(observed, "--method")
	if methodIndex < 0 || methodIndex+1 >= len(observed) || observed[methodIndex+1] != "GET" {
		t.Fatalf("query request did not force GET: %v", observed)
	}
}

func TestGetSeparatesExactHeadCacheKeys(t *testing.T) {
	stateDir := t.TempDir()
	var calls int
	observer := &Observer{
		StateDir: stateDir,
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{Stdout: []byte(fmt.Sprintf("HTTP/2 200 OK\nETag: \"etag-%d\"\n\n{\"call\":%d}", calls, calls))}
		},
	}

	first, err := observer.Get(context.Background(), GetRequest{
		Repository: "acme/app", Target: "main", Head: strings.Repeat("1", 40), Endpoint: "repos/acme/app/git/ref/heads/main", FreshWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := observer.Get(context.Background(), GetRequest{
		Repository: "acme/app", Target: "main", Head: strings.Repeat("2", 40), Endpoint: "repos/acme/app/git/ref/heads/main", FreshWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheKey == second.CacheKey {
		t.Fatalf("cache key reused across exact heads: %s", first.CacheKey)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestGetCoalescesAcrossProcesses(t *testing.T) {
	stateDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	ghPath := filepath.Join(binDir, "gh")
	script := "#!/bin/sh\n" +
		"echo call >> " + shellQuote(logPath) + "\n" +
		"sleep 0.2\n" +
		"printf 'HTTP/2 200 OK\\nETag: \"etag-process\"\\n\\n{\"ok\":true}'\n"
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runHelper := func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=TestHelperProcessObserve", "--")
		command.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS_OBSERVE=1",
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GITHUB_OBSERVER_STATE_DIR="+stateDir,
		)
		return command
	}

	first := runHelper()
	second := runHelper()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first helper: %v", err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second helper: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(data)), "call"); got != 1 {
		t.Fatalf("GitHub helper invocations = %d, want 1", got)
	}
}

func TestGetCoalescesZeroFreshWindowAcrossProcesses(t *testing.T) {
	stateDir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	ghPath := filepath.Join(binDir, "gh")
	script := "#!/bin/sh\n" +
		"echo call >> " + shellQuote(logPath) + "\n" +
		"sleep 0.2\n" +
		"printf 'HTTP/2 200 OK\\nETag: \"etag-zero\"\\n\\n{\"ok\":true}'\n"
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	runHelper := func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=TestHelperProcessObserve", "--")
		command.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS_OBSERVE=1",
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GITHUB_OBSERVER_STATE_DIR="+stateDir,
			"GITHUB_OBSERVER_FRESH_WINDOW=0",
		)
		return command
	}

	first := runHelper()
	second := runHelper()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first helper: %v", err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second helper: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(data)), "call"); got != 1 {
		t.Fatalf("GitHub helper invocations = %d, want 1", got)
	}
}

func TestWriteCacheEntryUsesPrivatePermissions(t *testing.T) {
	stateDir := t.TempDir()
	observer := &Observer{StateDir: stateDir}
	cacheTarget, lockTarget, err := observer.pathsForKey("permissions")
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Dir(cacheTarget)
	lockDir := filepath.Dir(lockTarget)
	cachePath := filepath.Join(stateDir, "cache.json")
	entry := &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Endpoint:      "repos/acme/app/branches/main",
		RequestHash:   requestHash("repos/acme/app/branches/main", nil, ""),
		StatusCode:    200,
		Body:          []byte(`{"ok":true}`),
		BodySHA256:    digest([]byte(`{"ok":true}`)),
		ObservedAt:    time.Now().UTC(),
	}
	if err := writeCacheEntry(cachePath, entry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("cache mode=%#o, want 0600", got)
	}
	for _, dir := range []string{cacheDir, lockDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory %s mode=%#o, want 0700", dir, got)
		}
	}
}

func TestGetRefetchesWhenCachedBodyDigestIsCorrupt(t *testing.T) {
	stateDir := t.TempDir()
	request := GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("f", 40),
		Endpoint:    "repos/acme/app/branches/main",
		FreshWindow: time.Hour,
	}
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, nil, "")
	cachePath := filepath.Join(stateDir, "cache", key+".json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Repository:    request.Repository,
		Target:        request.Target,
		Head:          request.Head,
		Endpoint:      request.Endpoint,
		RequestHash:   requestHash(request.Endpoint, nil, ""),
		StatusCode:    200,
		Body:          []byte(`{"cached":true}`),
		BodySHA256:    digest([]byte(`{"different":true}`)),
		ObservedAt:    time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	}
	if err := writeCacheEntry(cachePath, entry); err != nil {
		t.Fatal(err)
	}
	var calls int
	observer := &Observer{
		StateDir: stateDir,
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{Stdout: []byte("HTTP/2 200 OK\nETag: \"etag-refetch\"\n\n{\"fresh\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1 refetch after corrupt cache", calls)
	}
	if string(response.Body) != `{"fresh":true}` || response.Cached {
		t.Fatalf("response=%+v", response)
	}
	updated, err := readCacheEntry(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.BodySHA256 != digest(updated.Body) {
		t.Fatalf("updated cache digest mismatch: %+v", updated)
	}
}

func TestGetRefetchesWhenCachedBodyDigestIsMissing(t *testing.T) {
	stateDir := t.TempDir()
	request := GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("g", 40),
		Endpoint:    "repos/acme/app/branches/main",
		FreshWindow: time.Hour,
	}
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, nil, "")
	cachePath := filepath.Join(stateDir, "cache", key+".json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := &cacheEntry{
		SchemaVersion: cacheSchemaVersion,
		Repository:    request.Repository,
		Target:        request.Target,
		Head:          request.Head,
		Endpoint:      request.Endpoint,
		RequestHash:   requestHash(request.Endpoint, nil, ""),
		StatusCode:    200,
		Body:          []byte(`{"cached":true}`),
		ObservedAt:    time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	}
	if err := writeCacheEntry(cachePath, entry); err != nil {
		t.Fatal(err)
	}
	var calls int
	observer := &Observer{
		StateDir: stateDir,
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			calls++
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"fresh\":true}")}
		},
	}

	response, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1 refetch after missing digest", calls)
	}
	if string(response.Body) != `{"fresh":true}` || response.Cached {
		t.Fatalf("response=%+v", response)
	}
}

func TestHelperProcessObserve(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_OBSERVE") != "1" {
		return
	}
	observer := &Observer{StateDir: os.Getenv("GITHUB_OBSERVER_STATE_DIR")}
	freshWindow := 2 * time.Second
	if os.Getenv("GITHUB_OBSERVER_FRESH_WINDOW") == "0" {
		freshWindow = 0
	}
	_, err := observer.Get(context.Background(), GetRequest{
		Repository:  "acme/app",
		Target:      "main",
		Head:        strings.Repeat("c", 40),
		Endpoint:    "repos/acme/app/branches/main",
		FreshWindow: freshWindow,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
