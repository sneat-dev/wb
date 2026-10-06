package cmdhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
)

func TestLifecycleFlagsDefaultsAndCurrentContextReachOperations(t *testing.T) {
	t.Parallel()
	for _, supplied := range []bool{false, true} {
		t.Run(fmt.Sprint(supplied), func(t *testing.T) {
			t.Parallel()
			f := fakeCommands()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			calls := 0
			f.life.Check = func(config string) (lifecyclehooks.CheckReport, error) {
				calls++
				want := ""
				if supplied {
					want = "configuration"
				}
				if config != want {
					t.Fatal(config)
				}
				return lifecyclehooks.CheckReport{}, nil
			}
			f.life.Status = func(limit int) (lifecyclehooks.Status, error) {
				calls++
				want := 20
				if supplied {
					want = 7
				}
				if limit != want {
					t.Fatal(limit)
				}
				return lifecyclehooks.Status{}, nil
			}
			f.life.Resume = func() (lifecyclehooks.ResumeReport, error) { calls++; return lifecyclehooks.ResumeReport{}, nil }
			f.life.Retry = func(id string) (lifecyclehooks.Report, error) {
				calls++
				if id != "receipt" {
					t.Fatal(id)
				}
				return lifecyclehooks.Report{}, nil
			}
			f.life.GC = func(got lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) {
				calls++
				want := lifecyclehooks.GCOptions{Keep: 1000, OlderThan: 30 * 24 * time.Hour}
				if supplied {
					want = lifecyclehooks.GCOptions{Apply: true, Keep: 7, OlderThan: 2 * time.Hour}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("gc=%+v want=%+v", got, want)
				}
				return lifecyclehooks.GCReport{}, nil
			}
			f.life.Backfill = func(gotCtx context.Context, root, filter string, apply bool) (lifecyclehooks.BackfillPlan, error) {
				calls++
				if gotCtx != ctx || root != "/projects" || filter != "acme" || apply != supplied {
					t.Fatal(gotCtx, root, filter, apply)
				}
				return lifecyclehooks.BackfillPlan{}, nil
			}
			f.life.Drain = func(gotCtx context.Context, got DrainOptions) (lifecyclehooks.Report, error) {
				calls++
				want := DrainOptions{ConfigPath: "config", StateDir: "state", ReceiptPath: "receipt", Parallel: 2}
				if supplied {
					want.Parallel = 7
				}
				if gotCtx != ctx || got != want {
					t.Fatalf("drain=%+v want=%+v", got, want)
				}
				return lifecyclehooks.Report{}, nil
			}
			selections := [][]string{{"check"}, {"status"}, {"resume"}, {"retry", "receipt"}, {"gc"}, {"backfill"}, {"run-pending", "--config", "config", "--state-dir", "state", "--receipt", "receipt"}}
			if supplied {
				selections[0] = append(selections[0], "--config", "configuration")
				selections[1] = append(selections[1], "--limit", "7")
				selections[4] = append(selections[4], "--apply", "--keep", "7", "--older-than", "2h")
				selections[5] = append(selections[5], "--apply")
				selections[6] = append(selections[6], "--parallel", "7")
			}
			for _, args := range selections {
				cmd := New(f.runtime, f.git, f.life, f.agent)
				cmd.SetContext(ctx)
				cmd.SetArgs(append([]string{"lifecycle"}, args...))
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 7 {
				t.Fatal(calls)
			}
		})
	}
}
func richLifecycle(f commands) commands {
	f.life.Check = func(string) (lifecyclehooks.CheckReport, error) {
		return lifecyclehooks.CheckReport{Configured: true, Executors: []lifecyclehooks.ExecutorCheck{{Name: "code-index", Status: "ready", Run: "/trusted"}, {Name: "bad", Status: "refused", Finding: "unsafe"}}}, nil
	}
	f.life.Status = func(int) (lifecyclehooks.Status, error) {
		return lifecyclehooks.Status{Worker: "running", WorkerHealth: &lifecyclehooks.WorkerHealth{Status: "running", Message: "active"}, Pending: []lifecyclehooks.Queued{{Executor: "index", Event: lifecyclehooks.Event{Repository: "repo", NewSHA: "abcdef0123456789"}}}, Running: []lifecyclehooks.Queued{{Executor: "index", Event: lifecyclehooks.Event{Repository: "repo", NewSHA: "short"}}}, Receipts: []lifecyclehooks.Receipt{{ID: "receipt", Status: "failed", Executor: "index", Repository: "repo", NewSHA: "head", Message: "index failed", StdoutPath: "stdout.log", StderrPath: "stderr.log"}, {ID: "ok", Status: "succeeded"}}, Findings: []string{"receipt warning"}}, nil
	}
	f.life.Resume = func() (lifecyclehooks.ResumeReport, error) {
		return lifecyclehooks.ResumeReport{WorkerStarted: true, Warnings: []string{"resume warning"}}, nil
	}
	f.life.Retry = func(string) (lifecyclehooks.Report, error) {
		return lifecyclehooks.Report{Warnings: []string{"retry warning"}}, nil
	}
	f.life.GC = func(opt lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) {
		return lifecyclehooks.GCReport{Candidates: []string{"old"}, Findings: []string{"gc warning"}}, nil
	}
	f.life.Backfill = func(context.Context, string, string, bool) (lifecyclehooks.BackfillPlan, error) {
		return lifecyclehooks.BackfillPlan{Executions: []lifecyclehooks.Planned{{Executor: "index", Event: lifecyclehooks.Event{Repository: "repo", NewSHA: "head"}}}, Skipped: []string{"skipped repository"}}, nil
	}
	f.life.Drain = func(context.Context, DrainOptions) (lifecyclehooks.Report, error) {
		return lifecyclehooks.Report{Warnings: []string{"drain warning"}}, nil
	}
	return f
}
func TestLifecycleTextAndJSONKeepPrivateReportSemantics(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"check"}, {"status"}, {"resume"}, {"retry", "receipt"}, {"gc"}, {"gc", "--apply"}, {"backfill"}, {"backfill", "--apply"}, {"run-pending", "--config", "c", "--state-dir", "s", "--receipt", "r"}} {
		for _, format := range []string{"text", "json"} {
			if args[0] == "run-pending" && format == "json" {
				continue
			}
			t.Run(strings.Join(args, " ")+format, func(t *testing.T) {
				t.Parallel()
				f := richLifecycle(fakeCommands())
				call := append([]string{"lifecycle"}, args...)
				if format == "json" {
					call = append(call, "--format=json")
				}
				out, errOut, err := executeFamily(f, call...)
				if err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					if !json.Valid([]byte(out)) {
						t.Fatal(out)
					}
					return
				}
				want := map[string]string{"check": "code-index", "status": "stdout.log", "resume": "started=true", "retry": "Queued retry receipt", "gc": "old", "backfill": "index", "run-pending": ""}[args[0]]
				if !strings.Contains(out, want) {
					t.Fatal(out)
				}
				if args[0] == "status" && !strings.Contains(errOut, "receipt warning") {
					t.Fatal(errOut)
				}
			})
		}
	}
}
func TestLifecycleErrorsAndPartialResumeHaveUnchangedIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, kind := range []string{"check", "status", "resume", "retry", "gc", "backfill", "run-pending"} {
		for _, format := range []string{"text", "json"} {
			if kind == "run-pending" && format == "json" {
				continue
			}
			t.Run(kind+format, func(t *testing.T) {
				t.Parallel()
				f := fakeCommands()
				f.life.Check = func(string) (lifecyclehooks.CheckReport, error) { return lifecyclehooks.CheckReport{}, sentinel }
				f.life.Status = func(int) (lifecyclehooks.Status, error) { return lifecyclehooks.Status{}, sentinel }
				f.life.Resume = func() (lifecyclehooks.ResumeReport, error) {
					return lifecyclehooks.ResumeReport{Warnings: []string{"partial"}}, sentinel
				}
				f.life.Retry = func(string) (lifecyclehooks.Report, error) { return lifecyclehooks.Report{}, sentinel }
				f.life.GC = func(lifecyclehooks.GCOptions) (lifecyclehooks.GCReport, error) {
					return lifecyclehooks.GCReport{}, sentinel
				}
				f.life.Backfill = func(context.Context, string, string, bool) (lifecyclehooks.BackfillPlan, error) {
					return lifecyclehooks.BackfillPlan{}, sentinel
				}
				f.life.Drain = func(context.Context, DrainOptions) (lifecyclehooks.Report, error) {
					return lifecyclehooks.Report{Warnings: []string{"partial"}}, sentinel
				}
				args := []string{"lifecycle", kind}
				if kind == "retry" {
					args = append(args, "receipt")
				}
				if kind == "run-pending" {
					args = append(args, "--config", "c", "--state-dir", "s", "--receipt", "r")
				} else if format == "json" {
					args = append(args, "--json")
				}
				out, _, err := executeFamily(f, args...)
				if err != sentinel {
					t.Fatal(err)
				}
				if kind == "resume" && format == "json" && !json.Valid([]byte(out)) {
					t.Fatal("partial JSON report lost", out)
				}
			})
		}
	}
	f := fakeCommands()
	f.life.Check = func(string) (lifecyclehooks.CheckReport, error) {
		return lifecyclehooks.CheckReport{Findings: []string{"one"}}, nil
	}
	if _, _, err := executeFamily(f, "lifecycle", "check"); err == nil || !strings.Contains(err.Error(), "1 problem(s)") {
		t.Fatal(err)
	}
}
