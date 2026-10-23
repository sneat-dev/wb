package waitrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"strings"
	"sync"
	"testing"
	"time"
)

func readerFixture(t *testing.T, kind string) (context.Context, *int) {
	t.Helper()
	var mu sync.Mutex
	checks := new(int)
	number := "12"
	state := "closed"
	merged := true
	draft := false
	mergeable := "clean"
	if kind != "merged" {
		state = "open"
		merged = false
	}
	if kind == "identity" {
		number = "13"
		draft = true
	}
	if kind == "recover" {
		number = "14"
	}
	if kind == "blocked" {
		mergeable = "blocked"
	}
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{
		Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
			body := "{}"
			switch {
			case strings.HasSuffix(req.Endpoint, "/pulls/"+number):
				body = fmt.Sprintf(`{"number":%s,"state":%q,"draft":%t,"merged":%t,"mergeable_state":%q,"html_url":"https://example.invalid/%s","head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`, number, state, draft, merged, mergeable, number)
			case strings.Contains(req.Endpoint, "/check-runs"):
				mu.Lock()
				*checks++
				n := *checks
				mu.Unlock()
				if kind == "identity" || (kind == "recover" && n == 1) {
					return githubobserver.Response{}, errors.New("gh: not found (HTTP 404)")
				}
				name := "build"
				if kind == "blocked" {
					name = "build-and-test"
				}
				body = fmt.Sprintf(`{"total_count":1,"check_runs":[{"name":%q,"status":"completed","conclusion":"success","app":{"id":1,"slug":"gh"}}]}`, name)
				if kind == "recover" {
					body = `{"total_count":0,"check_runs":[]}`
				}
			case strings.Contains(req.Endpoint, "/actions/runs"):
				body = `{"total_count":0,"workflow_runs":[]}`
			case strings.Contains(req.Endpoint, "/status"):
				body = `{"state":"success","statuses":[]}`
			case strings.Contains(req.Endpoint, "/rules/branches/"):
				body = `[]`
			case strings.HasSuffix(req.Endpoint, "/branches/main"):
				body = `{"protected":false,"protection":{}}`
				if kind == "blocked" {
					body = `{"protected":true,"protection":{"required_status_checks":{"contexts":["build"],"checks":[{"context":"build","app_id":0}]}}}`
				}
			}
			return githubobserver.Response{Body: []byte(body), StatusCode: 200}, nil
		},
		Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
			return githubobserver.CommandResponse{ExitCode: 1}
		},
	})
	return ctx, checks
}
func observeReader(t *testing.T, kind string, condition Condition) (Output, int) {
	t.Helper()
	ctx, checks := readerFixture(t, kind)
	number := map[string]string{"merged": "12", "blocked": "12", "identity": "13", "recover": "14"}[kind]
	clock := time.Unix(0, 0)
	observer := Observer{Now: func() time.Time { return clock }, Sleep: func(context.Context, time.Duration) error { clock = clock.Add(time.Second); return nil }}
	out := observer.Wait(ctx, Request{Targets: []Reference{{Selector: "acme/app#" + number, Repository: "acme/app", Number: number}}, Condition: condition, Slice: 3 * time.Second, Interval: time.Second})
	return out, *checks
}
func TestWaitPRReadsGitHubForARealTarget(t *testing.T) {
	t.Parallel()
	out, _ := observeReader(t, "merged", ChecksSettled)
	if out.SchemaVersion != 1 || out.Status != Settled || len(out.Targets) != 1 {
		t.Fatal(out)
	}
	target := out.Targets[0]
	if target.State != "merged" || target.Checks["pass"] != 1 || target.Base != "main" || !strings.HasPrefix(target.Head, "aaaa") {
		t.Fatal(target)
	}
}
func TestWaitPRNamesARequiredCheckNobodyProduces(t *testing.T) {
	t.Parallel()
	out, _ := observeReader(t, "blocked", ChecksSettled)
	if len(out.Targets) != 1 {
		t.Fatal(out)
	}
	target := out.Targets[0]
	if target.Checks["pending"] != 0 || len(target.Blocked) == 0 || target.Blocked[0] != "build" {
		t.Fatal(target)
	}
}
func TestWaitPRKeepsIdentityFieldsWhenOnlyChecksReadFails(t *testing.T) {
	t.Parallel()
	out, _ := observeReader(t, "identity", ChecksSettled)
	if out.Status == Settled || len(out.Targets) != 1 {
		t.Fatal(out)
	}
	target := out.Targets[0]
	if target.Status != ReadError || target.State != "open" || !target.Draft || target.Head != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || target.Base != "main" || target.URL != "https://example.invalid/13" || target.Mergeable != "clean" {
		t.Fatal(target)
	}
}
func TestWaitPRChangedIgnoresAHeadThatWasNeverReallyBlank(t *testing.T) {
	t.Parallel()
	out, checks := observeReader(t, "recover", Changed)
	if checks < 2 || out.Status != Pending || len(out.Targets) != 1 || out.Targets[0].Status != Pending {
		t.Fatal(out, checks)
	}
}
