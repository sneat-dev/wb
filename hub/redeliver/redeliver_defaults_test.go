package redeliver

// Pack unit p02 coverage: New, Sweeper.transientDelay, nextPageURL,
// sleepContext. Unit tier only: no real network calls.

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	sweeper := New(Options{})
	if sweeper.options.APIBaseURL != DefaultAPIBaseURL {
		t.Fatalf("expected default API base URL, got %q", sweeper.options.APIBaseURL)
	}
	if sweeper.options.Client != http.DefaultClient {
		t.Fatalf("expected http.DefaultClient default")
	}
}

func TestTransientDelayFallback(t *testing.T) {
	t.Parallel()
	sweeper := &Sweeper{options: Options{Interval: 5 * time.Second}}
	if delay := sweeper.transientDelay(); delay != 5*time.Second {
		t.Fatalf("expected fallback to Interval, got %v", delay)
	}
}

func TestNextPageURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"malformed-no-semicolon", "justtoken", ""},
		{"no-next-relation", `<https://api.github.com/x?page=1>; rel="prev"`, ""},
		{"next-present", `<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=1>; rel="prev"`, "https://api.github.com/x?page=2"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := nextPageURL(testCase.header); got != testCase.want {
				t.Fatalf("nextPageURL(%q) = %q, want %q", testCase.header, got, testCase.want)
			}
		})
	}
}

func TestSleepContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, 200*time.Millisecond); err == nil {
		t.Fatalf("expected context error when context is already cancelled")
	}
}
