package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestLegacyPaginationChecksEveryTransportAndCancellation(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	path := "/repos/acme/app/commits/" + sha + "/comments"
	page := func(n int) string { return fmt.Sprintf("https://api.github.com%s?page=%d&per_page=100", path, n) }
	for _, kind := range []string{"complete", "foreign-host", "foreign-repo", "foreign-commit", "duplicate-next", "skipped-page", "missing-next", "malformed-next", "cancel-before", "cancel-between", "transport-error", "page-limit"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			failure := errors.New("synthetic provider unavailable")
			ctx = githubobserver.WithReader(ctx, githubobserver.Reader{Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				if r.Endpoint != page(calls) || r.Repository != "acme/app" {
					t.Fatalf("forbidden transport: %+v", r)
				}
				if kind == "transport-error" {
					return githubobserver.Response{}, failure
				}
				response := githubobserver.Response{Body: []byte("[]")}
				target := page(calls + 1)
				if calls == 1 {
					switch kind {
					case "foreign-host":
						target = strings.Replace(target, "api.github.com", "evil.invalid", 1)
					case "foreign-repo":
						target = strings.Replace(target, "acme/app", "other/app", 1)
					case "foreign-commit":
						target = strings.Replace(target, sha, strings.Repeat("b", 40), 1)
					case "skipped-page":
						target = page(3)
					case "cancel-between":
						cancel()
					}
					response.Headers = map[string]string{"Link": "<" + target + ">; rel=\"next\", <" + page(2) + ">; rel=\"last\""}
				}
				if kind == "duplicate-next" && calls == 1 {
					response.Headers["Link"] = "<" + page(2) + ">; rel=\"next\", <" + page(2) + ">; rel=\"next\""
				}
				if kind == "missing-next" && calls == 1 {
					response.Headers["Link"] = "<" + page(2) + ">; rel=\"last\""
				}
				if kind == "malformed-next" && calls == 1 {
					response.Headers["Link"] = "<" + page(2) + "; rel=\"next\""
				}
				if kind == "page-limit" {
					response.Headers = map[string]string{"Link": "<" + target + ">; rel=\"next\""}
				}
				return response, nil
			}})
			if kind == "cancel-before" {
				cancel()
			}
			pages, err := legacyCommentPages(ctx, "acme/app", sha)
			if kind == "complete" {
				if err != nil || len(pages) != 2 || calls != 2 {
					t.Fatalf("complete pages=%d calls=%d err=%v", len(pages), calls, err)
				}
				return
			}
			if err == nil || pages != nil {
				t.Fatalf("incomplete discovery accepted: %s pages=%d calls=%d err=%v", kind, len(pages), calls, err)
			}
			switch kind {
			case "cancel-before":
				if calls != 0 || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel before transport calls=%d err=%v", calls, err)
				}
			case "cancel-between":
				if calls != 1 || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel between calls=%d err=%v", calls, err)
				}
			case "transport-error":
				if calls != 1 || !errors.Is(err, failure) {
					t.Fatalf("transport evidence lost: %v", err)
				}
			case "page-limit":
				if calls != githubobserver.DefaultMaxPages || !strings.Contains(err.Error(), "exceeded") {
					t.Fatalf("unbounded pagination calls=%d err=%v", calls, err)
				}
			default:
				if calls != 1 {
					t.Fatalf("forbidden next transport occurred: %d", calls)
				}
			}
		})
	}
}

func TestLegacyPageTransportRejectsForgedRequestScopeBeforeDelegation(t *testing.T) {
	t.Parallel()
	path := "/repos/acme/app/commits/" + strings.Repeat("a", 40) + "/comments"
	for _, kind := range []string{"malformed-url", "foreign-repository", "query-override", "page-sequence"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				return githubobserver.Response{Body: []byte("[]")}, nil
			}})
			reader := legacyCommentPageReader(ctx, "acme/app", path)
			request := githubobserver.GetRequest{Repository: "acme/app", Endpoint: "https://api.github.com" + path + "?page=1&per_page=100"}
			switch kind {
			case "malformed-url":
				request.Endpoint = "https://%zz"
			case "foreign-repository":
				request.Repository = "other/app"
			case "query-override":
				request.Query = map[string]string{"page": "2"}
			case "page-sequence":
				request.Endpoint = strings.Replace(request.Endpoint, "page=1&", "page=2&", 1)
			}
			if _, err := reader.Get(ctx, request); err == nil || calls != 0 {
				t.Fatalf("forged scope reached transport: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestLegacyPageURLAndLinkPolicyRejectsIncompleteOrForeignInventory(t *testing.T) {
	t.Parallel()
	path := "/repos/acme/app/commits/" + strings.Repeat("a", 40) + "/comments"
	base := "https://api.github.com" + path
	for _, suffix := range []string{"?page=1;per_page=100", "?page=1&per_page=99", "?page=0&per_page=100", "?page=01&per_page=100", "?%70age=1&per_page=100", "?page=1&per_page=100&x=1", "#secret"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			if _, _, err := legacyPageURL(base+suffix, path); err == nil {
				t.Fatalf("unsafe page accepted: %s", suffix)
			}
		})
	}
	link := func(n int, rel string) string {
		return fmt.Sprintf("<%s?page=%d&per_page=100>; rel=\"%s\"", base, n, rel)
	}
	for _, header := range []string{"not a link", link(2, "next") + ", " + link(2, "last") + ", " + link(2, "last"), link(2, "prev"), link(2, "unknown"), "<" + base + "?page=2&per_page=100>; rel=next", "<" + base + "?page=2&per_page=100>; x=\"next\"", link(2, "next") + ", " + link(1, "last")} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()
			if err := legacyPageLinks(map[string]string{" Link ": header}, path, 1); err == nil {
				t.Fatalf("incomplete inventory accepted: %s", header)
			}
		})
	}
	if err := legacyPageLinks(map[string]string{"X-Unrelated": "value", "Link": " "}, path, 1); err != nil {
		t.Fatalf("complete empty inventory: %v", err)
	}
	if err := legacyPageLinks(map[string]string{"Link": link(1, "first") + ", " + link(1, "prev")}, path, 2); err != nil {
		t.Fatalf("terminal backward links: %v", err)
	}
}
