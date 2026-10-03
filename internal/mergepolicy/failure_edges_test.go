package mergepolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestResumeAndDiscoveryFailuresPrecedeInspection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing receipt", "invalid receipt", "discovery"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			options := Options{Parallel: 1}
			sentinel := errors.New("discovery unavailable")
			service.deps.Discover = func(string, string, []string, []string, bool) ([]discover.Repo, error) { return nil, sentinel }
			service.deps.Read = func(context.Context, string) ([]byte, error) {
				t.Fatal("inspection after failed preflight")
				return nil, nil
			}
			if kind != "discovery" {
				options.Resume = true
				options.ReportDir = t.TempDir()
				if kind == "invalid receipt" {
					if err := os.WriteFile(filepath.Join(options.ReportDir, "merge-policy.json"), []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := service.Run(t.Context(), Request{Options: options}, &bytes.Buffer{})
			if err == nil {
				t.Fatal(kind)
			}
			if kind == "discovery" && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
	}
}
func TestInspectionReportsReadDecodeRuleAndClassicFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"metadata read", "metadata decode", "rules read", "classic decode", "merge queue", "missing methods", "squash only", "clean"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				switch {
				case endpoint == "repos/acme/app":
					if kind == "metadata read" {
						return nil, errors.New("metadata unavailable")
					}
					if kind == "metadata decode" {
						return []byte("{"), nil
					}
					return []byte(`{"default_branch":"main","allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false,"merge_commit_title":"PR_TITLE","merge_commit_message":"PR_BODY"}`), nil
				case strings.HasSuffix(endpoint, "/protection"):
					if kind == "classic decode" {
						return []byte("{"), nil
					}
					if kind == "merge queue" {
						return []byte(`{"required_merge_queue":{"enabled":true}}`), nil
					}
					return []byte(`{}`), nil
				case strings.Contains(endpoint, "/rules/branches/"):
					if kind == "rules read" {
						return nil, errors.New("rules unavailable")
					}
					if kind == "missing methods" {
						return []byte(`[{"type":"pull_request","ruleset_source_type":"Repository","ruleset_source":"acme/app","ruleset_id":1,"parameters":{}}]`), nil
					}
					if kind == "squash only" {
						return []byte(`[{"type":"pull_request","ruleset_source_type":"Organization","ruleset_source":"acme","ruleset_id":1,"parameters":{"allowed_merge_methods":["squash"]}}]`), nil
					}
					return []byte(`[]`), nil
				}
				t.Fatal(endpoint)
				return nil, nil
			}
			report := service.inspectMergePolicyRepository(t.Context(), "acme/app")
			if kind == "clean" {
				if report.Disposition != "compliant" {
					t.Fatal(report)
				}
			} else if report.Disposition != "error" && report.Disposition != "blocked" {
				t.Fatal(kind, report)
			}
		})
	}
}
func TestRulesetPlanSkipsUnselectedAndBlocksUnsafeScope(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.Read = func(context.Context, string) ([]byte, error) { return nil, errors.New("snapshot unavailable") }
	for _, conflicts := range []bool{false, true} {
		report := Report{Repositories: []Repository{{Repository: "acme/app", Rulesets: []RuleRef{{Type: "required_linear_history", SourceType: "Repository", Source: "acme/app", ID: 7}}}, {Repository: "acme/other"}}}
		if conflicts {
			report.Repositories[0].Conflicts = []string{"queue"}
		}
		service.buildMergePolicyRulesetPlan(t.Context(), &report)
		if len(report.Rulesets) != 1 || report.Rulesets[0].Disposition != "blocked" {
			t.Fatal(report)
		}
	}
}
func TestSharedRulesetApplyRejectsChangedInvalidAndFailedUpdates(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"read", "changed", "invalid", "execute"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			body := []byte(`{"rules":[{"type":"required_linear_history"}]}`)
			change := RulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7, ObservedSHA: digestJSON(body)}
			service.deps.Read = func(context.Context, string) ([]byte, error) {
				if kind == "read" {
					return nil, errors.New("read unavailable")
				}
				if kind == "invalid" {
					change.ObservedSHA = digestJSON([]byte("{"))
					return []byte("{"), nil
				}
				return body, nil
			}
			if kind == "changed" {
				change.ObservedSHA = "other"
			}
			if kind == "invalid" {
				change.ObservedSHA = digestJSON([]byte("{"))
			}
			service.deps.Execute = func(context.Context, ...string) githubobserver.CommandResponse {
				return githubobserver.CommandResponse{Err: errors.New("update unavailable"), Stderr: []byte("denied")}
			}
			if err := service.applySharedRuleset(t.Context(), change); err == nil {
				t.Fatal(kind)
			}
		})
	}
}
func TestClassicProtectionInvalidInputAndUpdateFailure(t *testing.T) {
	t.Parallel()
	service := New()
	if err := service.applyClassicProtectionWithoutLinearHistory(t.Context(), "endpoint", []byte("{")); err == nil {
		t.Fatal("invalid protection accepted")
	}
	service.deps.Execute = func(context.Context, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{Err: errors.New("update unavailable")}
	}
	if err := service.applyClassicProtectionWithoutLinearHistory(t.Context(), "endpoint", []byte(`{}`)); err == nil {
		t.Fatal("update error lost")
	}
}
func TestHeartbeatReportsWorkDuringRealBoundedDiscovery(t *testing.T) {
	t.Parallel()
	for _, refuseWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(refuseWrite), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			service := New()
			ticks := make(chan time.Time)
			discoverStarted, releaseDiscovery := make(chan struct{}), make(chan struct{})
			stopEntered, releaseStop, stopFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var discoveryOnce, stopOnce sync.Once
			unblockDiscovery := func() { discoveryOnce.Do(func() { close(releaseDiscovery) }) }
			unblockStop := func() { stopOnce.Do(func() { close(releaseStop) }) }
			t.Cleanup(unblockDiscovery)
			t.Cleanup(unblockStop)
			service.deps.NewTicker = func(interval time.Duration) (<-chan time.Time, func()) {
				if interval != 9*time.Second {
					t.Errorf("heartbeat interval = %v", interval)
				}
				return ticks, func() {
					close(stopEntered)
					<-releaseStop
					close(stopFinished)
				}
			}
			service.deps.Discover = func(string, string, []string, []string, bool) ([]discover.Repo, error) {
				close(discoverStarted)
				<-releaseDiscovery
				return nil, nil
			}
			written := make(chan string, 1)
			result := make(chan error, 1)
			go func() {
				_, err := service.Run(ctx, Request{Options: Options{Parallel: 1}}, heartbeatWriter{written, refuseWrite})
				result <- err
			}()
			await := func(signal <-chan struct{}) {
				t.Helper()
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			await(discoverStarted)
			select {
			case ticks <- time.Time{}:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case output := <-written:
				if output != "merge-policy: working; inspected 0/0 repositories\n" {
					t.Fatalf("heartbeat = %q", output)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			unblockDiscovery()
			await(stopEntered)
			select {
			case err := <-result:
				t.Fatalf("Run returned before heartbeat stop joined: %v", err)
			default:
			}
			unblockStop()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			await(stopFinished)
			select {
			case ticks <- time.Time{}:
				t.Fatal("heartbeat still consumes ticks after Run")
			default:
			}
		})
	}
}

type heartbeatWriter struct {
	written chan<- string
	refuse  bool
}

func (writer heartbeatWriter) Write(value []byte) (int, error) {
	writer.written <- string(value)
	if writer.refuse {
		return 0, errors.New("heartbeat output unavailable")
	}
	return len(value), nil
}

func TestDefaultHeartbeatTickerUsesActualTimer(t *testing.T) {
	t.Parallel()
	ticks, stop := New().deps.NewTicker(9 * time.Second)
	t.Cleanup(stop)
	if ticks == nil {
		t.Fatal("actual timer has no channel")
	}
	select {
	case <-ticks:
		t.Fatal("actual nine-second timer fired immediately")
	default:
	}
}

func TestRunReportAndCheckpointFailuresReturnPartialReceipts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory audit", "directory apply", "write audit", "write apply", "progress", "checkpoint", "final write"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			service.deps.Discover = func(string, string, []string, []string, bool) ([]discover.Repo, error) { return nil, nil }
			dir := t.TempDir()
			options := Options{Parallel: 1, Apply: strings.HasSuffix(kind, "apply") || kind == "progress" || kind == "checkpoint" || kind == "final write", ReportDir: dir}
			if strings.HasPrefix(kind, "directory") {
				blocker := filepath.Join(dir, "file")
				if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
					t.Fatal(err)
				}
				options.ReportDir = filepath.Join(blocker, "reports")
			}
			if strings.HasPrefix(kind, "write") {
				if err := os.Mkdir(filepath.Join(dir, "merge-policy.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := errors.New("checkpoint failed")
			service.deps.Persist = func(Report) error {
				if kind == "checkpoint" {
					return sentinel
				}
				return nil
			}
			var out io.Writer = &bytes.Buffer{}
			if kind == "progress" {
				out = errorWriter{}
			}
			if kind == "final write" {
				out = callbackWriter{callback: func() {
					if err := os.Remove(filepath.Join(dir, "merge-policy.json")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(dir, "merge-policy.json"), 0o700); err != nil {
						t.Fatal(err)
					}
				}}
			}
			report, err := service.Run(t.Context(), Request{Options: options}, out)
			if err == nil {
				t.Fatal(kind, report)
			}
			if kind == "checkpoint" && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if !strings.HasPrefix(kind, "directory") && report.ReportPath == "" {
				t.Fatal("partial receipt path lost", report)
			}
		})
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("progress refused") }

type callbackWriter struct{ callback func() }

func (w callbackWriter) Write(p []byte) (int, error) { w.callback(); return len(p), nil }
func TestSharedApplyAndRepositoryCheckpointFailureOrdering(t *testing.T) {
	t.Parallel()
	service := New()
	sentinel := errors.New("checkpoint unavailable")
	service.deps.Persist = func(Report) error { return sentinel }
	for _, shared := range []bool{false, true} {
		report := Report{ReportPath: "report"}
		if shared {
			report.Rulesets = []RulesetChange{{SourceType: "Repository", Source: "acme/app", ID: 7, ObservedSHA: "old"}}
			service.deps.Read = func(context.Context, string) ([]byte, error) { return nil, errors.New("snapshot unavailable") }
		}
		if err := service.applyMergePolicy(t.Context(), &report, 1, io.Discard); !errors.Is(err, sentinel) {
			t.Fatal(report, err)
		}
		if shared && report.Rulesets[0].Disposition != "error" {
			t.Fatal(report)
		}
	}
	repo := Repository{Rulesets: []RuleRef{{SourceType: "Repository", Source: "acme/app", ID: 7}}}
	result, err := service.applyRepositoryMergePolicy(t.Context(), repo, map[string]bool{"repository/acme/app/7": true}, func(Repository) error { t.Fatal("checkpoint for blocked lease"); return nil })
	if err != nil || result.Disposition != "blocked" {
		t.Fatal(result, err)
	}
}
func TestClassicProtectionFailureAndWorkerCheckpointOrdering(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"update", "checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			body := []byte(`{"default_branch":"main","allow_merge_commit":true,"merge_commit_title":"PR_TITLE","merge_commit_message":"PR_BODY"}`)
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint == "repos/acme/app" {
					return body, nil
				}
				if strings.HasSuffix(endpoint, "/protection") {
					return []byte(`{"required_linear_history":{"enabled":true}}`), nil
				}
				return []byte(`[]`), nil
			}
			mutations := 0
			service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				mutations++
				if strings.Join(args[:3], " ") != "api --method PUT" {
					t.Fatal("repository PATCH after protection failure", args)
				}
				if kind == "update" {
					return githubobserver.CommandResponse{Err: errors.New("protection refused")}
				}
				return githubobserver.CommandResponse{}
			}
			repo := service.inspectMergePolicyRepository(t.Context(), "acme/app")
			if !repo.ClassicLinear || repo.Disposition != "drift" {
				t.Fatal(repo)
			}
			report := Report{ReportPath: "report", Repositories: []Repository{repo}}
			sentinel := errors.New("checkpoint refused")
			checkpoints := 0
			service.deps.Persist = func(Report) error {
				checkpoints++
				if kind == "checkpoint" && checkpoints >= 2 {
					return sentinel
				}
				return nil
			}
			err := service.applyMergePolicy(t.Context(), &report, 1, io.Discard)
			if mutations != 1 {
				t.Fatal(mutations)
			}
			if kind == "checkpoint" {
				if !errors.Is(err, sentinel) || checkpoints != 3 {
					t.Fatal(checkpoints, report, err)
				}
			} else if err != nil || report.Repositories[0].Disposition != "error" || !strings.Contains(report.Repositories[0].Error, "remove classic required linear history") {
				t.Fatal(report, err)
			}
		})
	}
}
func TestReportPathHomeFailureIsPropagated(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, "")
	t.Setenv("HOME", "")
	if _, err := mergePolicyReportPath(Scope{}, ""); err == nil {
		t.Fatal("missing home accepted")
	}
}
