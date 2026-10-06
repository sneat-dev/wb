package syncrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/syncreport"
)

func TestSyncServicePreservesDiscoveryClockStreamsCompletionAndHookOrder(t *testing.T) {
	t.Parallel()
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "interactive"}[interactive], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			ctx := context.WithValue(context.Background(), syncContextKey{}, "ctx")
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			var order []string
			var out, errOut bytes.Buffer
			repos := []discover.Repo{{Org: "acme", Name: "one", TransferError: "refusal"}, {Org: "beta", Name: "two", TransferError: "refusal"}}
			deps := Dependencies{Discovery: fleetdiscovery.Resolver{AuthUser: func() (string, error) { order = append(order, "auth"); return "user", nil }, MemberOrgs: func() ([]string, error) { t.Fatal("explicit owners must restrict"); return nil, nil }, ScanLocal: func(r string) ([]discover.Repo, error) {
				if r != root {
					t.Fatal(r)
				}
				return repos, nil
			}, ListRemote: func(owner string) ([]discover.Repo, error) {
				if owner != "acme" {
					t.Fatal(owner)
				}
				return nil, nil
			}, Reconcile: func(local, remote []discover.Repo) []discover.Repo { return local }}, Reconcile: func(c context.Context, r []discover.Repo) []discover.Repo {
				if c != ctx || !reflect.DeepEqual(r, repos) {
					t.Fatal("reconcile inputs")
				}
				order = append(order, "reconcile")
				return r
			}, Dispatch: func(c context.Context, e []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
				if c != ctx || len(e) != 1 || e[0].NewSHA != "new" {
					t.Fatalf("hook inputs %v %v", c, e)
				}
				order = append(order, "hooks")
				return lifecyclehooks.Report{Warnings: []string{"hook warning"}}, errors.New("hook failed")
			}, Now: func() time.Time { return now }}
			presentation := Presentation{Interactive: func(c context.Context, r []discover.Repo, totals map[string]int, o Options, w io.Writer) []fleetsync.Result {
				if c != ctx || w != &errOut || totals["acme"] != 1 || totals["beta"] != 1 || !o.Interactive {
					t.Fatal("UI inputs")
				}
				return []fleetsync.Result{{Repo: repos[0], Status: fleetsync.Cloned, HeadSHA: "new"}}
			}, Summary: func(w io.Writer, results []fleetsync.Result, prune, styled bool) {
				if w != io.Writer(&out) && !interactive || w != io.Writer(&errOut) && interactive || !prune || styled != interactive {
					t.Fatal("summary stream/policy")
				}
				order = append(order, "summary")
			}}
			complete := func(meta fleetsync.RunMeta, r []fleetsync.Result, o Options, w, ew io.Writer) int {
				if meta.StartedAt != now || meta.Discovered != 2 || meta.ProjectsRoot != root || meta.Filter != "" || !reflect.DeepEqual(meta.Owners, []string{"acme"}) || ew != &errOut {
					t.Fatalf("meta %+v", meta)
				}
				order = append(order, "complete")
				if !interactive {
					r[0].Status = fleetsync.Cloned
					r[0].HeadSHA = "new"
				}
				return 1
			}
			code := New(deps, complete, presentation).Run(ctx, Options{ProjectsRoot: root, Owners: []string{"acme"}, Workers: 2, PruneArchived: true, Interactive: interactive}, &out, &errOut)
			if code != 1 || !reflect.DeepEqual(order, []string{"auth", "reconcile", "summary", "complete", "hooks"}) || !strings.Contains(errOut.String(), "hook warning") || !strings.Contains(errOut.String(), "hook failed") {
				t.Fatalf("code=%d order=%v err=%s", code, order, errOut.String())
			}
		})
	}
}

type syncContextKey struct{}

func TestSyncServiceAuthenticationDiscoveryAndEmptyDryRunContracts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"auth", "discovery", "empty", "dry-run"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var out, errOut bytes.Buffer
			boom := errors.New("failure")
			completeCalls := 0
			deps := Dependencies{Discovery: fleetdiscovery.Resolver{AuthUser: func() (string, error) {
				if kind == "auth" {
					return "", boom
				}
				return "user", nil
			}, MemberOrgs: func() ([]string, error) { return nil, nil }, ScanLocal: func(string) ([]discover.Repo, error) {
				if kind == "discovery" {
					return nil, boom
				}
				return nil, nil
			}, ListRemote: func(string) ([]discover.Repo, error) { return nil, nil }, Reconcile: func(local, remote []discover.Repo) []discover.Repo { return nil }}, Reconcile: func(context.Context, []discover.Repo) []discover.Repo { return nil }, Dispatch: func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
				t.Fatal("empty/dry-run hooks")
				return lifecyclehooks.Report{}, nil
			}, Now: time.Now}
			code := New(deps, func(meta fleetsync.RunMeta, r []fleetsync.Result, o Options, w, ew io.Writer) int {
				completeCalls++
				if meta.Discovered != 0 || meta.Scanned != 0 || len(r) != 0 {
					t.Fatal(meta)
				}
				return 0
			}, Presentation{}).Run(context.Background(), Options{ProjectsRoot: root, Workers: 1, DryRun: kind == "dry-run"}, &out, &errOut)
			if kind == "auth" || kind == "discovery" {
				if code != 1 || completeCalls != 0 || !strings.Contains(out.String(), "Sync issues:") {
					t.Fatalf("code=%d calls=%d out=%s", code, completeCalls, out.String())
				}
				if kind == "auth" && !strings.Contains(errOut.String(), "gh auth login") {
					t.Fatal(errOut.String())
				}
			} else if code != 0 || completeCalls != 1 || !strings.Contains(out.String(), "no repos found") {
				t.Fatalf("code=%d calls=%d out=%s", code, completeCalls, out.String())
			}
		})
	}
}
func TestSyncCompletionPreservesReportMarkerPublishOrderAndWarnings(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"clean", "publish", "publish-error", "failed", "dry-publish"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var out, errOut bytes.Buffer
			var order []string
			results := []fleetsync.Result{}
			if kind == "failed" {
				results = append(results, fleetsync.Result{Status: fleetsync.Failed})
			}
			opts := Options{ProjectsRoot: root, Filter: "acme", Workers: 3, Publish: strings.Contains(kind, "publish"), DryRun: kind == "dry-publish"}
			effects := Effects{Markers: func(r []fleetsync.Result, p string, w io.Writer) {
				if p != root || w != &errOut || !strings.Contains(out.String(), "Sync issues:") {
					t.Fatal("report must precede markers")
				}
				order = append(order, "markers")
			}, Publish: func(p, f string, n int, w, ew io.Writer) error {
				if p != root || f != "acme" || n != 3 || w != &out || ew != &errOut {
					t.Fatal("publish inputs")
				}
				order = append(order, "publish")
				if kind == "publish-error" {
					return io.ErrUnexpectedEOF
				}
				return nil
			}}
			code := Finalize(fleetsync.RunMeta{}, results, opts, effects, &out, &errOut)
			if kind == "failed" {
				if code != 1 || len(order) != 0 {
					t.Fatalf("failed %d %v", code, order)
				}
			} else {
				if code != 0 {
					t.Fatal(code)
				}
				want := []string{"markers"}
				if opts.Publish {
					want = append(want, "publish")
				}
				if opts.DryRun {
					want = nil
					if !strings.Contains(out.String(), "skipping remote publish") {
						t.Fatal(out.String())
					}
				}
				if !reflect.DeepEqual(order, want) {
					t.Fatalf("order=%v want=%v", order, want)
				}
			}
			if kind == "publish-error" && !strings.Contains(errOut.String(), "remote publish failed (sync itself succeeded)") {
				t.Fatal(errOut.String())
			}
		})
	}
}
func TestSyncNativePublisherPreservesPathSelectionAndCanceledProviderError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := PublishSyncReport(context.Background(), "invalid", root, syncreport.Report{}); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if got := syncReportCloneURL("acme/workbench"); got != "git@github.com:acme/workbench.git" {
		t.Fatal(got)
	}
	path, err := syncReportClonePath(root, "acme/workbench")
	if err != nil || path != filepath.Join(root, "github.com", "acme", "workbench") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PublishSyncReport(ctx, "acme/workbench", root, syncreport.Report{}); err == nil {
		t.Fatal("canceled native provider accepted")
	}
	if _, err := os.Stat(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
