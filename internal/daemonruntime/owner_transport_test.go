package daemonruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type ownerContextKey struct{}

type ownerRoundTrip func(*http.Request) (*http.Response, error)

func (f ownerRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOwnerTokenTransportPreservesExactTokenAndClonesRequest(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "owner-token", "exact raw '☃' token"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			ctx := context.WithValue(context.Background(), ownerContextKey{}, token)
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wb.local/owner", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "original header")
			sentinel := errors.New("base refusal")
			response := &http.Response{StatusCode: http.StatusForbidden}
			called := false
			base := ownerRoundTrip(func(got *http.Request) (*http.Response, error) {
				called = true
				if got == request || got.Context() != ctx || got.Body != request.Body || got.URL.String() != request.URL.String() || got.Header.Get("Authorization") != "Bearer "+token {
					t.Fatalf("cloned request = %#v", got)
				}
				return response, sentinel
			})
			got, err := WithOwnerToken(token, base).RoundTrip(request)
			if !called || got != response || err != sentinel || request.Header.Get("Authorization") != "original header" {
				t.Fatalf("response/identity/original = %#v,%v,%#v", got, err, request.Header)
			}
		})
	}
}
