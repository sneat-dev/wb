package githubobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCommitPaginationSurvivesFreshCacheAndNotModifiedWithoutLink(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	state := t.TempDir()
	first := "repos/acme/app/pulls/624/commits?per_page=100"
	second := "https://api.github.com/repos/acme/app/pulls/624/commits?per_page=100&page=2"
	bodies := map[string]string{}
	for page, endpoint := range []string{first, second} {
		var commits []map[string]string
		for number := page*100 + 1; number <= 106 && number <= (page+1)*100; number++ {
			commits = append(commits, map[string]string{"sha": fmt.Sprintf("%040x", number)})
		}
		body, err := json.Marshal(commits)
		if err != nil {
			t.Fatal(err)
		}
		bodies[endpoint] = string(body)
	}
	calls := 0
	observer := &Observer{StateDir: state, Now: func() time.Time { return now }}
	observer.Run = func(_ context.Context, _ string, args ...string) commandResult {
		calls++
		endpoint := args[1]
		body, ok := bodies[endpoint]
		if !ok {
			t.Fatalf("unexpected endpoint %q", endpoint)
		}
		if calls > 2 {
			if !strings.Contains(strings.Join(args, " "), "If-None-Match") {
				t.Fatal("missing conditional validator")
			}
			// The unchanged entity need not repeat pagination metadata on 304.
			return commandResult{Stdout: []byte("HTTP/2 304 Not Modified\nETag: \"page\"\n\n")}
		}
		link := ""
		if endpoint == first {
			link = "<" + second + ">; rel=\"next\""
		}
		response := pageResponse(body, link)
		response.Stdout = []byte(strings.Replace(string(response.Stdout), "HTTP/2 200 OK\n", "HTTP/2 200 OK\nETag: \"page\"\n", 1))
		return response
	}
	request := GetRequest{Repository: "acme/app", Endpoint: first, FreshWindow: time.Second}
	check := func(reader *Observer) {
		t.Helper()
		pages, err := reader.GetPages(context.Background(), request, 0)
		if err != nil {
			t.Fatal(err)
		}
		var commits []map[string]string
		for _, page := range pages {
			var batch []map[string]string
			if err := json.Unmarshal(page.Body, &batch); err != nil {
				t.Fatal(err)
			}
			commits = append(commits, batch...)
		}
		if len(pages) != 2 || len(commits) != 106 || commits[105]["sha"] != fmt.Sprintf("%040x", 106) {
			t.Fatalf("lost head: pages=%d commits=%d", len(pages), len(commits))
		}
	}
	check(observer)
	check(observer)
	if calls != 2 {
		t.Fatalf("fresh reuse made %d network calls", calls)
	}
	now = now.Add(2 * time.Second)
	check(observer)
	if calls != 4 {
		t.Fatalf("revalidation made %d network calls", calls)
	}
	// A new process reading the persisted 304 entry must retain the same next link.
	restarted := &Observer{StateDir: state, Now: func() time.Time { return now }, Run: func(context.Context, string, ...string) commandResult {
		t.Fatal("persisted fresh read unexpectedly used network")
		return commandResult{}
	}}
	check(restarted)
}

func TestLegacyObserverCacheWithoutPaginationMetadataIsNotReused(t *testing.T) {
	t.Parallel()
	request := GetRequest{Repository: "acme/app", Endpoint: "repos/acme/app/pulls/624/commits?per_page=100", FreshWindow: time.Hour}
	state := t.TempDir()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	observer := &Observer{StateDir: state, Now: func() time.Time { return now }}
	key := cacheKey(request.Repository, request.Target, request.Head, request.Endpoint, request.Query, request.Accept)
	cachePath, _, err := observer.pathsForKey(key)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("[{}]")
	if err := writeCacheEntry(cachePath, &cacheEntry{SchemaVersion: 1, Body: body, BodySHA256: digest(body), StatusCode: 200, ObservedAt: now, ETag: "old"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	observer.Run = func(_ context.Context, _ string, args ...string) commandResult {
		calls++
		if strings.Contains(strings.Join(args, " "), "If-None-Match") {
			t.Fatal("legacy entry supplied a conditional validator")
		}
		return pageResponse("[]", "")
	}
	response, err := observer.Get(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || response.Cached || string(response.Body) != "[]" {
		t.Fatalf("legacy cache survived: calls=%d cached=%v body=%s", calls, response.Cached, response.Body)
	}
}
