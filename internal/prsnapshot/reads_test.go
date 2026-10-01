package prsnapshot

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// fakeGitHub answers every GitHub read below a context from memory and records
// each endpoint asked, so a test counts the reads of an observation exactly
// (nothing runs `gh`, nothing reaches the network, and tests run in parallel).
type fakeGitHub struct {
	mu        sync.Mutex
	reads     []string
	state     string
	merged    bool
	checkRuns string
	branch    string
}

func (f *fakeGitHub) context() context.Context {
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: f.get,
		Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
			return githubobserver.CommandResponse{ExitCode: 1}
		},
	})
}

func (f *fakeGitHub) get(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
	f.mu.Lock()
	f.reads = append(f.reads, request.Endpoint)
	f.mu.Unlock()
	endpoint := request.Endpoint
	body := "{}"
	switch {
	case strings.HasSuffix(endpoint, "/pulls/5"):
		body = `{"number":5,"state":"` + f.state + `","draft":false,"merged":` + map[bool]string{true: "true", false: "false"}[f.merged] +
			`,"mergeable_state":"clean","html_url":"https://example.invalid/5","head":{"ref":"feature","sha":"` + fakeHead + `"},"base":{"ref":"main","sha":"b"}}`
	case strings.Contains(endpoint, "/check-runs"):
		body = f.checkRuns
	case strings.Contains(endpoint, "/actions/runs"):
		body = `{"total_count":0,"workflow_runs":[]}`
	case strings.Contains(endpoint, "/status"):
		body = `{"state":"success","statuses":[]}`
	case strings.HasSuffix(endpoint, "/branches/main"):
		body = f.branch
	case strings.Contains(endpoint, "/rules/branches/main"):
		body = `[]`
	}
	return githubobserver.Response{Body: []byte(body), StatusCode: 200}, nil
}

func (f *fakeGitHub) count(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, read := range f.reads {
		if strings.Contains(read, part) {
			n++
		}
	}
	return n
}

const (
	greenRuns = `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","app":{"id":1,"slug":"gh"}}]}`
	redRuns   = `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"failure","html_url":"https://example.invalid/run/1","app":{"id":1,"slug":"gh"}}]}`
	noRuns    = `{"total_count":0,"check_runs":[]}`
	unruled   = `{"protected":false,"protection":{}}`
	requires  = `{"protected":true,"protection":{"required_status_checks":{"contexts":["build"],"checks":[]}}}`
)

// TestObservationReadCountsAreTested counts every GitHub read of each kind of
// observation: the daemon's hourly budget is stated in these numbers.
func TestObservationReadCountsAreTested(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		github   *fakeGitHub
		lean     bool
		reads    int
		check    func(*testing.T, Snapshot)
		pullOnly bool
	}{
		"green": {github: &fakeGitHub{state: "open", checkRuns: greenRuns, branch: unruled}, lean: true, reads: ReadsPerObservation, check: func(t *testing.T, s Snapshot) {
			if !s.Green {
				t.Errorf("snapshot = %+v", s)
			}
		}},
		"blocked reuses the reads": {github: &fakeGitHub{state: "open", checkRuns: noRuns, branch: requires}, lean: true, reads: ReadsPerObservation, check: func(t *testing.T, s Snapshot) {
			if s.Green || len(s.Blocked) != 1 || s.Blocked[0] != "build" {
				t.Errorf("snapshot = %+v", s)
			}
		}},
		"red, lean": {github: &fakeGitHub{state: "open", checkRuns: redRuns, branch: unruled}, lean: true, reads: ReadsPerObservation, check: func(t *testing.T, s Snapshot) {
			if len(s.Failed) != 1 || len(s.Failures) != 0 {
				t.Errorf("snapshot = %+v", s)
			}
		}},
		"merged": {github: &fakeGitHub{state: "closed", merged: true, checkRuns: greenRuns, branch: unruled}, reads: ReadsPerInactiveObservation, pullOnly: true, check: func(t *testing.T, s Snapshot) {
			if !s.Merged || s.Green || len(s.Checks) != 0 {
				t.Errorf("snapshot = %+v", s)
			}
		}},
		"closed": {github: &fakeGitHub{state: "closed", checkRuns: greenRuns, branch: unruled}, lean: true, reads: ReadsPerInactiveObservation, pullOnly: true, check: func(t *testing.T, s Snapshot) {
			if s.State != "closed" || s.Merged {
				t.Errorf("snapshot = %+v", s)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			github := test.github
			observe := Observe
			if test.lean {
				observe = ObserveLean
			}
			snapshot := observe(github.context(), "acme/app", "5")
			if snapshot.Err != nil {
				t.Fatalf("Err = %v", snapshot.Err)
			}
			test.check(t, snapshot)
			if len(github.reads) != test.reads || github.count("/pulls/5") != 1 {
				t.Fatalf("%d reads (%d of the pull request): %v; want %d with the pull request read once", len(github.reads), github.count("/pulls/5"), github.reads, test.reads)
			}
			if !test.pullOnly && github.count("/actions/runs") != 1 {
				t.Errorf("the Actions workflow runs were read %d times: %v", github.count("/actions/runs"), github.reads)
			}
		})
	}
}

// TestFullObservationOfARedHeadReadsMoreThanTheLeanOne pins what the lean
// observation saves.
func TestFullObservationOfARedHeadReadsMoreThanTheLeanOne(t *testing.T) {
	t.Parallel()
	github := &fakeGitHub{state: "open", checkRuns: redRuns, branch: unruled}
	if snapshot := Observe(github.context(), "acme/app", "5"); snapshot.Err != nil || len(github.reads) <= ReadsPerObservation {
		t.Fatalf("full observation: %+v after %d reads, want more than %d", snapshot, len(github.reads), ReadsPerObservation)
	}
}

// TestAnOpenPullRequestWhoseChecksCannotBeReadIsAnError proves an unreadable
// head is an error, never a state.
func TestAnOpenPullRequestWhoseChecksCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	failing := githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			if strings.HasSuffix(request.Endpoint, "/pulls/5") {
				return (&fakeGitHub{state: "open"}).get(context.Background(), request)
			}
			return githubobserver.Response{}, context.DeadlineExceeded
		},
	})
	if snapshot := Observe(failing, "acme/app", "5"); snapshot.Err == nil || snapshot.Green {
		t.Fatalf("snapshot = %+v, want an error", snapshot)
	}
}
