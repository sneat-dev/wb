package githubobserver

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestGetBoundsRetryWaitsAndUsesResetOnlyForExhaustedQuota(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		status     int
		remaining  string
		resetAfter time.Duration
		retryAfter string
		message    string
		backoff    time.Duration
		wantSleep  time.Duration
		exhausted  bool
	}{
		{name: "service failure with available quota", status: 503, remaining: "4900", resetAfter: time.Hour, wantSleep: time.Second},
		{name: "service failure without quota evidence", status: 502, resetAfter: time.Hour, wantSleep: time.Second},
		{name: "quota reset within remaining budget", status: 403, remaining: "0", resetAfter: 3 * time.Second, wantSleep: 2 * time.Second},
		{name: "quota reset beyond remaining budget", status: 403, remaining: "0", resetAfter: time.Hour, wantSleep: 4 * time.Second, exhausted: true},
		{name: "secondary limit honors Retry-After with available quota", status: 403, remaining: "4900", resetAfter: time.Hour, retryAfter: "2", message: "secondary rate limit", wantSleep: 2 * time.Second},
		{name: "service failure honors Retry-After", status: 503, remaining: "4900", resetAfter: time.Hour, retryAfter: "2", wantSleep: 2 * time.Second},
		{name: "Retry-After beyond remaining budget", status: 429, retryAfter: "3600", wantSleep: 4 * time.Second, exhausted: true},
		{name: "transport backoff beyond remaining budget", backoff: time.Hour, wantSleep: 4 * time.Second, exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			startedAt := time.Unix(1_788_080_400, 0).UTC()
			now := startedAt
			backoff := tc.backoff
			if backoff == 0 {
				backoff = time.Second
			}
			calls, sleeps := 0, 0
			observer := &Observer{
				StateDir:        t.TempDir(),
				MaxRetryElapsed: 5 * time.Second,
				BaseBackoff:     backoff,
				MaxBackoff:      backoff,
				RandomIntn:      func(max int64) int64 { return max - 1 },
				Now:             func() time.Time { return now },
				Sleep: func(_ context.Context, delay time.Duration) error {
					sleeps++
					if delay != tc.wantSleep {
						t.Fatalf("retry wait = %v, want %v", delay, tc.wantSleep)
					}
					now = now.Add(delay)
					return nil
				},
				Run: func(context.Context, string, ...string) commandResult {
					calls++
					now = now.Add(time.Second) // The response consumes part of the budget.
					if calls > 1 {
						if tc.exhausted {
							t.Fatal("a request ran after the retry budget expired")
						}
						return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
					}
					if tc.status == 0 {
						return commandResult{ExitCode: -1, Err: errors.New("signal: killed")}
					}
					headers := ""
					if tc.remaining != "" {
						headers += "X-RateLimit-Remaining: " + tc.remaining + "\n"
					}
					if tc.resetAfter > 0 {
						headers += fmt.Sprintf("X-RateLimit-Reset: %d\n", startedAt.Add(tc.resetAfter).Unix())
					}
					if tc.retryAfter != "" {
						headers += "Retry-After: " + tc.retryAfter + "\n"
					}
					return commandResult{
						Stdout:   []byte(fmt.Sprintf("HTTP/2 %d Unavailable\n%s\n{\"message\":%q}", tc.status, headers, tc.message)),
						ExitCode: 1,
						Err:      errors.New("exit status 1"),
					}
				},
			}
			response, err := observer.Get(context.Background(), GetRequest{Repository: "acme/app", Endpoint: "repos/acme/app/pulls/1"})
			if tc.exhausted {
				if !errors.Is(err, ErrTransientRetriesExhausted) || calls != 1 || now.Sub(startedAt) != 5*time.Second {
					t.Fatalf("err=%v calls=%d elapsed=%v, want exhausted after one request within 5s", err, calls, now.Sub(startedAt))
				}
			} else if err != nil || calls != 2 || string(response.Body) != `{"ok":true}` {
				t.Fatalf("err=%v calls=%d response=%s, want recovery on the second request", err, calls, response.Body)
			}
			if sleeps != 1 {
				t.Fatalf("sleeps=%d, want exactly one retry wait", sleeps)
			}
		})
	}
}

func TestGetCancellationInterruptsBudgetedProviderWaitAndReleasesLock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Unix(1_788_080_400, 0).UTC()
	calls, sleeps := 0, 0
	observer := &Observer{
		StateDir:        t.TempDir(),
		MaxRetryElapsed: 5 * time.Second,
		Now:             func() time.Time { return now },
		Sleep: func(ctx context.Context, delay time.Duration) error {
			sleeps++
			if delay != 4*time.Second {
				t.Fatalf("retry wait=%v, want the remaining 4s budget", delay)
			}
			cancel()
			// Exercise the real timer's cancellation path without a real wait.
			return (&Observer{}).sleep(ctx, delay)
		},
		Run: func(context.Context, string, ...string) commandResult {
			calls++
			now = now.Add(time.Second)
			if calls == 1 {
				return commandResult{Stdout: []byte("HTTP/2 429 Too Many Requests\nRetry-After: 3600\n\n{}"), ExitCode: 1, Err: errors.New("exit status 1")}
			}
			return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{\"ok\":true}")}
		},
	}
	request := GetRequest{Repository: "acme/app", Endpoint: "repos/acme/app/pulls/1"}
	_, err := observer.Get(ctx, request)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTransientRetriesExhausted) || calls != 1 || sleeps != 1 {
		t.Fatalf("err=%v calls=%d sleeps=%d, want immediate caller cancellation without retry", err, calls, sleeps)
	}
	// The cancelled observation must not leave the per-request lock held.
	response, err := observer.Get(context.Background(), request)
	if err != nil || calls != 2 || string(response.Body) != `{"ok":true}` {
		t.Fatalf("err=%v calls=%d response=%s, want a fresh observation after cancellation", err, calls, response.Body)
	}
}

func TestReadBoundsRetryBackoffByRemainingBudget(t *testing.T) {
	t.Parallel()
	startedAt := time.Unix(1_788_080_400, 0).UTC()
	now := startedAt
	calls, sleeps := 0, 0
	observer := &Observer{
		MaxRetryElapsed: 5 * time.Second,
		BaseBackoff:     time.Hour,
		MaxBackoff:      time.Hour,
		RandomIntn:      func(max int64) int64 { return max - 1 },
		Now:             func() time.Time { return now },
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps++
			if delay != 4*time.Second {
				t.Fatalf("retry wait=%v, want the remaining 4s budget", delay)
			}
			now = now.Add(delay)
			return nil
		},
		Run: func(context.Context, string, ...string) commandResult {
			calls++
			now = now.Add(time.Second)
			return commandResult{Stderr: []byte("gh: HTTP 503"), ExitCode: 1, Err: errors.New("exit status 1")}
		},
	}
	_, err := observer.Read(context.Background(), "", "api", "repos/acme/app/pulls/1")
	if !errors.Is(err, ErrTransientRetriesExhausted) || calls != 1 || sleeps != 1 || now.Sub(startedAt) != 5*time.Second {
		t.Fatalf("err=%v calls=%d sleeps=%d elapsed=%v, want exhaustion at the 5s budget", err, calls, sleeps, now.Sub(startedAt))
	}
}
