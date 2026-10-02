package redeliver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type boundaryTransport func(*http.Request) (*http.Response, error)

func (transport boundaryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestInvalidJWTStopsEveryDeliveryBoundaryBeforeHTTP(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"sweep", "listing", "delivery"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			calls := 0
			client := &http.Client{Transport: boundaryTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("HTTP must not start without JWT")
			})}
			sweeper := New(Options{AppID: 1, PrivateKeyPEM: []byte("invalid private key"), Client: client, Store: newFakeStore(), Now: func() time.Time { return now }})
			state := &tokenState{value: "expired token", mintedAt: now.Add(-jwtRefreshAfter - time.Second)}
			var err error
			switch boundary {
			case "sweep":
				sweeper.Sweep(context.Background())
				if got := sweeper.Status().LastFailureClass; got != "jwt" {
					t.Fatalf("failure class=%q", got)
				}
			case "listing":
				_, _, _, err = sweeper.listRecentDeliveries(context.Background(), state, now.Add(-MaxDeliveryAge))
			case "delivery":
				_, err = sweeper.redeliver(context.Background(), state, 1)
			}
			if boundary != "sweep" && (err == nil || classify(err) != "jwt") {
				t.Fatalf("JWT boundary failure=%v", err)
			}
			if calls != 0 {
				t.Fatalf("invalid JWT made %d HTTP calls", calls)
			}
			if state.value != "expired token" {
				t.Fatal("failed mint replaced retained token")
			}
		})
	}
}

func TestRunStopsWhenSleepCancelsWithoutReturningError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sleeps := 0
	sweeper := New(Options{Store: newFakeStore(), PrivateKeyPEM: []byte("invalid key"), Sleep: func(context.Context, time.Duration) error { sleeps++; cancel(); return nil }})
	if err := sweeper.Run(ctx); err != nil || sleeps != 1 {
		t.Fatalf("normal shutdown=%v sleeps=%d", err, sleeps)
	}
}

func TestMalformedRequestAndResponseKeepTransportDiagnosticsPrivate(t *testing.T) {
	t.Parallel()
	sweeper := New(Options{Client: &http.Client{Transport: boundaryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{invalid response")), Header: make(http.Header)}, nil
	})}})
	secretURL := "://private-token\n"
	if response, err := sweeper.doRequest(context.Background(), http.MethodGet, secretURL, "private-token"); response != nil || err == nil || classify(err) != "transport" || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("request failure leaked details: %v,%v", response, err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	broken := New(Options{APIBaseURL: secretURL, Client: sweeper.options.Client, Now: func() time.Time { return now }})
	if status, err := broken.redeliver(context.Background(), &tokenState{value: "private-token", mintedAt: now}, 1); status != 0 || err == nil || classify(err) != "transport" || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("redelivery request failure leaked details: %d,%v", status, err)
	}
	if _, err := sweeper.get(context.Background(), "https://example.invalid/private-token", "private-token", &[]deliveryItem{}); err == nil || classify(err) != "decode" || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("response decode failure=%v", err)
	}
}

func TestPositiveSleepCompletesOnLiveContext(t *testing.T) {
	t.Parallel()
	if err := sleepContext(context.Background(), time.Nanosecond); err != nil {
		t.Fatalf("live delay failed: %v", err)
	}
}
