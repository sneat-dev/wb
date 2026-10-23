package syncrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestFinishSyncLifecycleHooksDispatchesOnlyChangedCheckouts(t *testing.T) {
	t.Parallel()
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "cloned", Path: "/projects/acme/cloned"}, Status: fleetsync.Cloned, HeadSHA: "clone-head"},
		{Repo: discover.Repo{Org: "acme", Name: "updated", Path: "/projects/acme/updated"}, Status: fleetsync.Pulled, Updated: true, BeforeHeadSHA: "old", HeadSHA: "new"},
		{Repo: discover.Repo{Org: "acme", Name: "current", Path: "/projects/acme/current"}, Status: fleetsync.Pulled, BeforeHeadSHA: "same", HeadSHA: "same"},
		{Repo: discover.Repo{Org: "acme", Name: "dirty", Path: "/projects/acme/dirty"}, Status: fleetsync.SkippedDirty, HeadSHA: "dirty"},
	}
	var got []lifecyclehooks.Event
	dispatch := func(_ context.Context, events []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		got = append(got, events...)
		return lifecyclehooks.Report{Executed: len(events)}, nil
	}
	if code := finishSyncLifecycleHooks(context.Background(), results, dispatch, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if len(got) != 2 {
		t.Fatalf("events=%+v, want cloned and updated only", got)
	}
	if got[0].Repository != "github.com/acme/cloned" || got[0].OldSHA != "" || got[0].NewSHA != "clone-head" {
		t.Fatalf("clone event=%+v", got[0])
	}
	if got[1].Repository != "github.com/acme/updated" || got[1].OldSHA != "old" || got[1].NewSHA != "new" {
		t.Fatalf("pull event=%+v", got[1])
	}
}
func TestFinishSyncLifecycleHooksWarnsWithoutChangingSyncOutcome(t *testing.T) {
	t.Parallel()
	results := []fleetsync.Result{{
		Repo:   discover.Repo{Org: "acme", Name: "updated", Path: "/projects/acme/updated"},
		Status: fleetsync.Pulled, Updated: true, BeforeHeadSHA: "old", HeadSHA: "new",
	}}
	var errOut bytes.Buffer
	dispatch := func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		return lifecyclehooks.Report{}, errors.New("invalid hook configuration")
	}
	if code := finishSyncLifecycleHooks(context.Background(), results, dispatch, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want successful repository update", code)
	}
	if !strings.Contains(errOut.String(), "warning: lifecycle hooks were not dispatched") {
		t.Fatalf("warning = %q", errOut.String())
	}
}
func TestResolveSyncOwnersRequiresAuthentication(t *testing.T) {
	t.Parallel()
	_, err := resolveSyncOwners(nil,
		func() (string, error) { return "", fmt.Errorf("invalid token") },
		func() ([]string, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "GitHub authentication failed") {
		t.Fatalf("resolveSyncOwners() error = %v, want authentication failure", err)
	}
}
func TestResolveSyncOwnersSeparatesRequestedOwnersFromMembership(t *testing.T) {
	t.Parallel()
	owners, err := resolveSyncOwners([]string{"sneat-co"},
		func() (string, error) { return "trakhimenok", nil },
		func() ([]string, error) { t.Fatal("member org lookup should not run for --org"); return nil, nil },
	)
	if err != nil || !reflect.DeepEqual(owners, []string{"sneat-co"}) {
		t.Fatalf("resolveSyncOwners() = %v, %v; want [sneat-co], nil", owners, err)
	}
}

//nolint:paralleltest // native default authentication uses a process-wide PATH fixture.
func TestCwDepsSyncOwnersRestrictionAndDiscovery(t *testing.T) {
	// An explicit -o restriction short-circuits org discovery but still
	// requires working authentication.
	owners, err := resolveSyncOwners([]string{"only-org"}, func() (string, error) { return "user", nil },
		func() ([]string, error) { return nil, errors.New("must not be called") })
	if err != nil || len(owners) != 1 || owners[0] != "only-org" {
		t.Fatalf("explicit owners = %v, %v", owners, err)
	}
	// No restriction: the authenticated user plus every member org.
	owners, err = resolveSyncOwners(nil, func() (string, error) { return "user", nil },
		func() ([]string, error) { return []string{"org-a", "org-b"}, nil })
	if err != nil || len(owners) != 3 || owners[0] != "user" {
		t.Fatalf("discovered owners = %v, %v", owners, err)
	}
	// Authentication failure is a hard error, never "no owners".
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "", errors.New("gh missing") },
		func() ([]string, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "GitHub authentication failed") {
		t.Fatalf("auth failure = %v", err)
	}
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "", nil },
		func() ([]string, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "authenticated user is empty") {
		t.Fatalf("empty user = %v", err)
	}
	if _, err := resolveSyncOwners(nil, func() (string, error) { return "user", nil },
		func() ([]string, error) { return nil, errors.New("boom") }); err == nil || !strings.Contains(err.Error(), "could not list GitHub organizations") {
		t.Fatalf("org discovery failure = %v", err)
	}

	// syncOwners is the discover-backed wrapper.
	cwCovFakeGH(t, "cwcov-user", []string{"cwcov-org"}, `[]`)
	owners, err = resolveSyncOwners(nil, discover.AuthUser, discover.MemberOrgs)
	if err != nil || len(owners) != 2 {
		t.Fatalf("syncOwners = %v, %v", owners, err)
	}
	if only, err := resolveSyncOwners([]string{"explicit"}, discover.AuthUser, discover.MemberOrgs); err != nil || len(only) != 1 || only[0] != "explicit" {
		t.Fatalf("syncOwners(-o) = %v, %v", only, err)
	}
}
func TestCwDepsSyncLifecycleEventsSelectsMovedCheckouts(t *testing.T) {
	t.Parallel()
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "clone"}, Status: fleetsync.Cloned, HeadSHA: "aaa", BeforeHeadSHA: ""},
		{Repo: discover.Repo{Org: "acme", Name: "pull"}, Status: fleetsync.NoOp, Updated: true, HeadSHA: "bbb", BeforeHeadSHA: "aaa"},
		{Repo: discover.Repo{Org: "acme", Name: "unchanged"}, Status: fleetsync.NoOp},
		{Repo: discover.Repo{Org: "acme", Name: "cloned-no-sha"}, Status: fleetsync.Cloned},
	}
	events := syncLifecycleEvents(results)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per moved checkout", events)
	}
	if events[0].Name != lifecyclehooks.EventCheckoutUpdated ||
		events[0].Repository != "github.com/acme/clone" || events[0].Cause != "sync-clone" {
		t.Errorf("clone event = %+v", events[0])
	}
	if events[1].Cause != "sync-pull" || events[1].OldSHA != "aaa" || events[1].NewSHA != "bbb" {
		t.Errorf("pull event = %+v", events[1])
	}
	if events := syncLifecycleEvents(nil); len(events) != 0 {
		t.Errorf("no results produced events: %+v", events)
	}
}
func TestCwDepsFinishSyncLifecycleHooksReportsWarnings(t *testing.T) {
	t.Parallel()
	var errOut bytes.Buffer
	// No events at all: the dispatcher must not even be called.
	called := false
	code := finishSyncLifecycleHooks(context.Background(), nil, func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		called = true
		return lifecyclehooks.Report{}, nil
	}, &errOut)
	if code != 0 || called {
		t.Fatalf("empty results: code=%d called=%t", code, called)
	}
	// Warnings and a dispatch error are both surfaced as warnings, and a hook
	// problem never changes sync's own exit code.
	results := []fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "app"}, Status: fleetsync.Cloned, HeadSHA: "sha"}}
	code = finishSyncLifecycleHooks(context.Background(), results, func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		return lifecyclehooks.Report{Warnings: []string{"hook env is incomplete"}}, errors.New("dispatcher unavailable")
	}, &errOut)
	if code != 0 {
		t.Fatalf("hook failure changed the exit code: %d", code)
	}
	for _, want := range []string{"warning: hook env is incomplete", "warning: lifecycle hooks were not dispatched: dispatcher unavailable"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
}
func TestCwDepsFinishSyncReportsIssuesAndPublishIntent(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	meta := fleetsync.RunMeta{StartedAt: time.Now().UTC(), ProjectsRoot: projects, Scanned: 1}

	// A failed repository is a findings exit even when every other result
	// succeeded.
	var out, errOut bytes.Buffer
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "ok"}, Status: fleetsync.NoOp},
		{Repo: discover.Repo{Org: "acme", Name: "bad"}, Status: fleetsync.Failed, Err: errors.New("pull refused")},
	}
	if code := Finalize(meta, results, Options{ProjectsRoot: projects, Workers: 1, DryRun: true}, Effects{}, &out, &errOut); code != 1 {
		t.Fatalf("finishSync with a failed repository = %d, want 1", code)
	}

	// A dry-run --publish states that nothing was published rather than
	// publishing an outcome that never happened.
	out.Reset()
	errOut.Reset()
	clean := []fleetsync.Result{{Repo: discover.Repo{Org: "acme", Name: "ok"}, Status: fleetsync.NoOp}}
	if code := Finalize(meta, clean, Options{ProjectsRoot: projects, Workers: 1, DryRun: true, Publish: true}, Effects{}, &out, &errOut); code != 0 {
		t.Fatalf("dry-run publish finishSync = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "dry-run: skipping remote publish") {
		t.Errorf("dry-run publish did not say it skipped: %q", out.String())
	}
}

func cwCovFakeGH(t *testing.T, user string, orgs []string, remoteReposJSON string) {
	t.Helper()
	binDir := t.TempDir()
	orgsJSON, err := json.Marshal(cwCovOrgLogins(orgs))
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "api" ]; then
  case "$2" in
    user)
      printf 'HTTP/2 200 OK\n\n{"login":"%s"}\n'
      exit 0
      ;;
    user/orgs)
      printf 'HTTP/2 200 OK\n\n%s\n'
      exit 0
      ;;
    *)
      printf '{"total_count":0,"items":[]}\n'
      exit 0
      ;;
  esac
fi
if [ "$1" = "repo" ] && [ "$2" = "list" ]; then
  printf '%%s\n' '%s'
  exit 0
fi
printf '{"total_count":0,"items":[]}\n'
exit 0
`, user, string(orgsJSON), remoteReposJSON)
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_HOME", t.TempDir())
}
func cwCovOrgLogins(orgs []string) []map[string]string {
	out := make([]map[string]string, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, map[string]string{"login": org})
	}
	return out
}

func TestSyncReportWriterUsesStderrForInteractiveRuns(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	_, _ = syncReportWriter(true, &stdout, &stderr).Write([]byte("interactive report"))
	if stdout.Len() != 0 {
		t.Fatalf("interactive report was written to stdout: %q", stdout.String())
	}
	if got, want := stderr.String(), "interactive report"; got != want {
		t.Fatalf("interactive report on stderr = %q, want %q", got, want)
	}
}
func TestSyncReportWriterKeepsNonInteractiveReportsOnStdout(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	_, _ = syncReportWriter(false, &stdout, &stderr).Write([]byte("plain report"))
	if got, want := stdout.String(), "plain report"; got != want {
		t.Fatalf("non-interactive report on stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("non-interactive report was written to stderr: %q", stderr.String())
	}
}
func TestCwDepsSyncReportWriterPicksTheVisibleStream(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if got := syncReportWriter(false, &out, &errOut); got != io.Writer(&out) {
		t.Error("a non-interactive run must keep its report on stdout")
	}
	if got := syncReportWriter(true, &out, &errOut); got != io.Writer(&errOut) {
		t.Error("an interactive run must move its report to stderr after the TUI restores the terminal")
	}
}
