package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/progress"
)

func TestUpdatedHeadSettlementUsesInvocationLocalClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		bodies                []string
		readDelays            []time.Duration
		errorRead, cancelRead int
		tickAdvance           time.Duration
		neverTick             bool
		wantHead, wantReason  string
		wantStages            []string
	}{
		{name: "published immediately", bodies: []string{`{"number":7,"head":{"sha":"new-head"}}`}, wantHead: "new-head", wantStages: []string{"now", "read"}},
		{name: "published after deadline still wins", bodies: []string{`{"number":7,"head":{"sha":"new-head"}}`}, readDelays: []time.Duration{90*time.Second + time.Nanosecond}, wantHead: "new-head", wantStages: []string{"now", "read"}},
		{name: "case blank malformed and transport remain unsettled", bodies: []string{`{"number":7,"head":{"sha":"OLD-HEAD"}}`, `{"number":7,"head":{"sha":" \n"}}`, `{`, `unused`, `{"number":7,"head":{"sha":"new-head"}}`}, errorRead: 4, tickAdvance: 3 * time.Second, wantHead: "new-head", wantStages: []string{"now", "read", "now", "progress", "after", "read", "now", "progress", "after", "read", "now", "progress", "after", "read", "now", "progress", "after", "read"}},
		{name: "exact deadline waits then expires", bodies: []string{`{"number":7,"head":{"sha":"old-head"}}`, `{"number":7,"head":{"sha":"old-head"}}`}, readDelays: []time.Duration{90 * time.Second}, tickAdvance: time.Nanosecond, wantReason: "updated head for acme/app#7 did not appear within 1m30s", wantStages: []string{"now", "read", "now", "progress", "after", "read", "now"}},
		{name: "expired read failure yields settlement timeout", bodies: []string{`unused`}, readDelays: []time.Duration{90*time.Second + time.Nanosecond}, errorRead: 1, wantReason: "updated head for acme/app#7 did not appear within 1m30s", wantStages: []string{"now", "read", "now"}},
		{name: "cancelled unsettled read", bodies: []string{`{"number":7,"head":{"sha":"old-head"}}`}, cancelRead: 1, neverTick: true, wantReason: context.Canceled.Error(), wantStages: []string{"now", "read", "now", "progress", "after"}},
		{name: "published receipt wins cancellation", bodies: []string{`{"number":7,"head":{"sha":"new-head"}}`}, cancelRead: 1, wantHead: "new-head", wantStages: []string{"now", "read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			instant := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			stages := []string{}
			reads := 0
			ctx = githubobserver.WithReader(ctx, githubobserver.Reader{
				Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
					stages = append(stages, "read")
					reads++
					if r.Dir != "" || r.Repository != "acme/app" || r.Target != "" || r.Head != "" || r.FreshWindow != 0 || r.Endpoint != "repos/acme/app/pulls/7" {
						t.Fatalf("request=%+v", r)
					}
					if reads > len(tc.bodies) {
						t.Fatalf("unexpected extra read %d", reads)
					}
					if reads <= len(tc.readDelays) {
						instant = instant.Add(tc.readDelays[reads-1])
					}
					if reads == tc.cancelRead {
						cancel()
					}
					if reads == tc.errorRead {
						return githubobserver.Response{}, errors.New("temporary transport refusal")
					}
					return githubobserver.Response{Body: []byte(tc.bodies[reads-1])}, nil
				},
				Read: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("unexpected Read")
					return nil, errors.New("unexpected Read")
				},
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					t.Fatal("settlement must not mutate")
					return githubobserver.CommandResponse{ExitCode: 1, Err: errors.New("unexpected Execute")}
				},
			})
			now := func() time.Time { stages = append(stages, "now"); return instant }
			after := func(d time.Duration) <-chan time.Time {
				stages = append(stages, "after")
				if d != 3*time.Second {
					t.Fatalf("poll=%s,want production3s", d)
				}
				tick := make(chan time.Time, 1)
				if !tc.neverTick {
					instant = instant.Add(tc.tickAdvance)
					tick <- instant
				}
				return tick
			}
			reporter := func(e progress.Event) {
				stages = append(stages, "progress")
				want := progress.Event{Operation: "pr_land", Phase: "update_branch", State: progress.Waiting, Detail: "waiting for the updated head"}
				if !reflect.DeepEqual(e, want) {
					t.Fatalf("progress=%+v,want=%+v", e, want)
				}
			}
			head, reason := waitForUpdatedHead(ctx, "acme/app", "7", "old-head", reporter, now, after)
			if head != tc.wantHead || reason != tc.wantReason || reads != len(tc.bodies) || !reflect.DeepEqual(stages, tc.wantStages) {
				t.Fatalf("head=%q reason=%q reads=%d stages=%q", head, reason, reads, stages)
			}
		})
	}
}
