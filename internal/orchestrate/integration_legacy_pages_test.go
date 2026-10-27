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
	if err := legacyPageLinks(map[string]string{"Link": link(1, "last")}, path, 2); err == nil {
		t.Fatal("contradictory last-page inventory accepted")
	}
	if err := legacyPageLinks(map[string]string{"Link": link(1, "first") + ", " + link(1, "prev")}, path, 2); err != nil {
		t.Fatalf("terminal backward links: %v", err)
	}
}

func TestLegacyCommentsNeverClaimsCompleteInventoryForAmbiguousLinkRepresentation(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	next := "<https://api.github.com/repos/acme/app/commits/" + sha + "/comments?page=2&per_page=100>"
	for _, kind := range []string{"multi-relation", "tab-relation", "duplicate-case-empty", "duplicate-case-prev"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				headers := map[string]string{"Link": next + "; rel=\"next\""}
				switch kind {
				case "multi-relation":
					headers["Link"] = next + "; rel=\"next last\""
				case "tab-relation":
					headers["Link"] = next + "; rel=\"\tnext\""
				case "duplicate-case-empty":
					headers["link"] = ""
				case "duplicate-case-prev":
					headers["link"] = "<https://api.github.com/repos/acme/app/commits/" + sha + "/comments?page=1&per_page=100>; rel=\"prev\""
				}
				return githubobserver.Response{Body: []byte("[]"), Headers: headers}, nil
			}})
			bindings, err := (githubIntegration{}).LegacyComments(ctx, "acme/app", sha)
			if err == nil || bindings != nil || calls != 1 {
				t.Fatalf("ambiguous representation claimed complete or followed transport: calls=%d bindings=%v err=%v", calls, bindings, err)
			}
		})
	}
}

func TestLegacyCommentsRetainsAnnouncedLastPageAcrossCompleteWalk(t *testing.T) {
	t.Parallel()
	record := legacyTestRecord()
	binding, body := legacyTestBinding(record)
	sha := record.CandidateSHA
	page := func(n int) string {
		return fmt.Sprintf("https://api.github.com/repos/acme/app/commits/%s/comments?page=%d&per_page=100", sha, n)
	}
	link := func(next, last int) map[string]string {
		return map[string]string{"Link": "<" + page(next) + ">; rel=\"next\", <" + page(last) + ">; rel=\"last\""}
	}
	for _, kind := range []string{"missing-next", "shrunk-last", "stable-three", "growing-four", "growing-without-last", "empty-terminal-two", "record-on-third"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				if request.Endpoint != page(calls) || request.Repository != "acme/app" {
					t.Fatalf("unexpected transport: %+v", request)
				}
				response := githubobserver.Response{Body: []byte("[]")}
				switch calls {
				case 1:
					last := 3
					if kind == "empty-terminal-two" {
						last = 2
					}
					response.Headers = link(2, last)
				case 2:
					switch kind {
					case "missing-next", "empty-terminal-two":
					case "shrunk-last":
						response.Headers = map[string]string{"Link": "<" + page(2) + ">; rel=\"last\""}
					case "growing-four":
						response.Headers = link(3, 4)
					default:
						response.Headers = link(3, 3)
					}
				case 3:
					switch kind {
					case "growing-four":
						response.Headers = link(4, 4)
					case "growing-without-last":
						response.Headers = map[string]string{"Link": "<" + page(4) + ">; rel=\"next\""}
					case "record-on-third":
						response.Body = []byte(legacyJSON([]map[string]string{{"html_url": binding.URL, "commit_id": sha, "body": body}}))
					}
				}
				return response, nil
			}})
			got, err := (githubIntegration{}).LegacyComments(ctx, "acme/app", sha)
			switch kind {
			case "missing-next", "shrunk-last":
				want := "ended before its announced last page"
				if kind == "shrunk-last" {
					want = "last page shrank"
				}
				if err == nil || !strings.Contains(err.Error(), want) || got != nil || calls != 2 {
					t.Fatalf("premature inventory accepted: calls=%d got=%v err=%v", calls, got, err)
				}
			default:
				expectedCalls := 3
				if strings.HasPrefix(kind, "growing") {
					expectedCalls = 4
				}
				if kind == "empty-terminal-two" {
					expectedCalls = 2
				}
				if err != nil || calls != expectedCalls {
					t.Fatalf("complete inventory rejected: calls=%d got=%v err=%v", calls, got, err)
				}
				if kind == "record-on-third" {
					if len(got) != 1 || got[0] != binding {
						t.Fatalf("third-page provenance lost: %+v", got)
					}
				} else if len(got) != 0 {
					t.Fatalf("empty complete inventory fabricated records: %+v", got)
				}
			}
		})
	}
}
