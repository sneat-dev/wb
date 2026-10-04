package cmddaemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func execute(t *testing.T, path string, runtime shared.Runtime, deps Dependencies, args ...string) (string, string, error) {
	t.Helper()
	c := commandForTest(path, runtime, deps)
	var out, errOut bytes.Buffer
	c.SetContext(t.Context())
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err := c.Execute()
	return out.String(), errOut.String(), err
}
func TestLifecycleCurrentOptionsAndErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("native refusal")
	for _, verb := range []string{"start", "restart", "status", "stop", "recover", "serve"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			root := "before"
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
			calls := 0
			check := func(ctx context.Context, r string) {
				t.Helper()
				calls++
				if ctx != t.Context() || r != "after" {
					t.Fatalf("context/root=%v/%s", ctx, r)
				}
			}
			deps.Start = func(ctx context.Context, r StartRequest, progress func(string)) (daemonruntime.Result, error) {
				check(ctx, r.ProjectsRoot)
				if r.Listen != "127.0.0.1:9000" || !r.ForceDetached || !r.ReplaceOtherRoot {
					t.Fatalf("request=%+v", r)
				}
				progress("started")
				return daemonruntime.Result{}, sentinel
			}
			deps.Restart = func(ctx context.Context, r RestartRequest, progress func(string)) (daemonruntime.Result, error) {
				check(ctx, r.ProjectsRoot)
				if !r.IfRunning || !r.ForceDetached || !r.ReplaceOtherRoot {
					t.Fatalf("request=%+v", r)
				}
				progress("restarted")
				return daemonruntime.Result{}, sentinel
			}
			deps.Status = func(ctx context.Context, r string) (daemonruntime.Result, error) {
				check(ctx, r)
				return daemonruntime.Result{}, sentinel
			}
			deps.Stop = deps.Status
			deps.Recover = func(ctx context.Context, r string, apply bool) (daemonruntime.RecoveryResult, error) {
				check(ctx, r)
				if !apply {
					t.Fatal("apply missing")
				}
				return daemonruntime.RecoveryResult{}, sentinel
			}
			deps.Serve = func(ctx context.Context, r ServeRequest, out, errOut io.Writer) error {
				check(ctx, r.ProjectsRoot)
				if r.Listen != "127.0.0.1:9000" || r.LifecycleState != "/pinned" || !r.Quiet || !r.ManagedStart {
					t.Fatalf("request=%+v", r)
				}
				_, _ = io.WriteString(out, "serve out")
				_, _ = io.WriteString(errOut, "serve err")
				return sentinel
			}
			c := commandForTest(verb, runtime, deps)
			root = "after"
			var out, errOut bytes.Buffer
			c.SetContext(t.Context())
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SilenceUsage = true
			c.SilenceErrors = true
			args := map[string][]string{"start": {"--listen=127.0.0.1:9000", "--force-detached", "--replace-other-root"}, "restart": {"--if-running", "--force-detached", "--replace-other-root"}, "recover": {"--apply"}, "serve": {"--listen=127.0.0.1:9000", "--lifecycle-state=/pinned", "--quiet", "--managed-start"}}[verb]
			c.SetArgs(args)
			if err := c.Execute(); err != sentinel || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if verb == "start" && !strings.Contains(errOut.String(), "wb: daemon start: started") {
				t.Fatal(errOut.String())
			}
			if verb == "restart" && !strings.Contains(errOut.String(), "wb: daemon restart: restarted") {
				t.Fatal(errOut.String())
			}
			if verb == "serve" && (out.String() != "serve out" || errOut.String() != "serve err") {
				t.Fatal("streams lost")
			}
		})
	}
}
func TestLifecycleDefaultsAndSuccess(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	deps.Start = func(_ context.Context, r StartRequest, _ func(string)) (daemonruntime.Result, error) {
		if r != (StartRequest{ProjectsRoot: "root", Listen: daemonruntime.DefaultListen}) {
			t.Fatalf("start=%+v", r)
		}
		return daemonruntime.Result{Action: "start"}, nil
	}
	deps.Restart = func(_ context.Context, r RestartRequest, _ func(string)) (daemonruntime.Result, error) {
		if r != (RestartRequest{ProjectsRoot: "root"}) {
			t.Fatalf("restart=%+v", r)
		}
		return daemonruntime.Result{Action: "restart"}, nil
	}
	deps.Serve = func(_ context.Context, r ServeRequest, _, _ io.Writer) error {
		if r != (ServeRequest{ProjectsRoot: "root", Listen: daemonruntime.DefaultListen}) {
			t.Fatalf("serve=%+v", r)
		}
		return nil
	}
	for _, verb := range []string{"start", "restart", "status", "stop", "recover", "serve"} {
		if _, _, err := execute(t, verb, testRuntime(), deps); err != nil {
			t.Fatal(err)
		}
		if verb != "serve" {
			if out, _, err := execute(t, verb, testRuntime(), deps, "--json"); err != nil || !strings.HasPrefix(out, "{") {
				t.Fatalf("%s %q %v", verb, out, err)
			}
		}
	}
}
func TestLifecycleInvalidFormatsAndServeAddressMakeNoCalls(t *testing.T) {
	t.Parallel()
	deps := Dependencies{}
	for _, verb := range []string{"start", "restart", "status", "stop", "recover"} {
		for _, args := range [][]string{{"--format=yaml"}, {"--json", "--format=yaml"}, {"extra"}} {
			if _, _, err := execute(t, verb, testRuntime(), deps, args...); err == nil {
				t.Fatalf("%s %v", verb, args)
			}
		}
	}
	if _, _, err := execute(t, "serve", testRuntime(), deps, "--listen=0.0.0.0:8000"); err == nil {
		t.Fatal("public listen accepted")
	}
}
func TestRecoverReceiptPrecedesFindingsAndWriterErrors(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	deps.Recover = func(context.Context, string, bool) (daemonruntime.RecoveryResult, error) {
		return daemonruntime.RecoveryResult{LockPresent: true, Reason: "alive", Detail: "owned"}, nil
	}
	out, _, err := execute(t, "recover", testRuntime(), deps, "--apply")
	if !strings.Contains(out, "reason=alive") || err == nil || !strings.Contains(err.Error(), "recovery refused: alive: owned") {
		t.Fatalf("%q %v", out, err)
	}
	for _, reason := range []string{"no_stale_owner", "alive"} {
		deps.Recover = func(context.Context, string, bool) (daemonruntime.RecoveryResult, error) {
			return daemonruntime.RecoveryResult{LockPresent: true, Reason: reason}, nil
		}
		_, _, err := execute(t, "recover", testRuntime(), deps)
		if err != nil {
			t.Fatal(err)
		}
	}
	deps.Recover = func(context.Context, string, bool) (daemonruntime.RecoveryResult, error) {
		return daemonruntime.RecoveryResult{LockPresent: true, Reason: "alive"}, nil
	}
	c := commandForTest("recover", testRuntime(), deps)
	sentinel := errors.New("writer")
	c.SetOut(failingWriter{sentinel})
	c.SetErr(io.Discard)
	c.SetArgs([]string{"--apply"})
	if err := c.Execute(); err != sentinel {
		t.Fatalf("writer precedence=%v", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestStopSupervisorHintsPreserveAuthority(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		kind        daemon.Supervisor
		label, want string
	}{{daemon.SupervisorSystemd, "", "systemctl --user stop wb.service"}, {daemon.SupervisorLaunchd, "foreign", "launchctl bootout gui/501/foreign"}, {daemon.SupervisorLaunchd, "", ""}, {daemon.SupervisorLaunchd, daemonruntime.LaunchdLabel, ""}} {
		deps := testDependencies()
		deps.Stop = func(context.Context, string) (daemonruntime.Result, error) {
			return daemonruntime.Result{State: daemonruntime.PublicState{Supervisor: tt.kind, SupervisorLabel: tt.label}}, nil
		}
		_, stderr, err := execute(t, "stop", testRuntime(), deps)
		if err != nil {
			t.Fatal(err)
		}
		if tt.want == "" && stderr != "" || tt.want != "" && !strings.Contains(stderr, tt.want) {
			t.Fatalf("stderr=%q want=%q", stderr, tt.want)
		}
	}
}
func TestOperationsCurrentOptionsDefaultsAndErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation")
	for _, verb := range []string{"submit", "get", "wait", "cancel"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			calls := 0
			check := func(ctx context.Context, root, id string, progress io.Writer) {
				calls++
				if root != "root" || id != "id" || progress == nil {
					t.Fatalf("%s/%s/%v", root, id, progress)
				}
				if verb == "wait" {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > time.Minute {
						t.Fatal("deadline missing")
					}
				} else if ctx != t.Context() {
					t.Fatal("context changed")
				}
			}
			deps.Submit = func(ctx context.Context, r daemonoperation.SubmitRequest, progress io.Writer) (*daemonv1.Operation, error) {
				check(ctx, r.ProjectsRoot, "id", progress)
				want := daemonoperation.SubmitRequest{ProjectsRoot: "root", Cwd: "/cwd", IdempotencyKey: "key", Argv: []string{"echo", "a"}, CPUUnits: 3, Wait: true}
				if !reflect.DeepEqual(r, want) {
					t.Fatalf("submit=%+v", r)
				}
				return nil, sentinel
			}
			deps.Get = func(ctx context.Context, r, id string, p io.Writer) (*daemonv1.Operation, error) {
				check(ctx, r, id, p)
				return nil, sentinel
			}
			deps.Cancel = deps.Get
			deps.Wait = func(ctx context.Context, r, id, cursor string, p io.Writer) (*daemonv1.Operation, error) {
				check(ctx, r, id, p)
				if cursor != "cursor" {
					t.Fatal(cursor)
				}
				return nil, sentinel
			}
			args := map[string][]string{"submit": {"--idempotency-key=key", "--cpu-units=3", "--wait", "--", "echo", "a"}, "get": {"id"}, "cancel": {"id"}, "wait": {"--after-cursor=cursor", "--timeout=1m", "id"}}[verb]
			if _, _, err := execute(t, "operation "+verb, testRuntime(), deps, args...); err != sentinel || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	deps := testDependencies()
	deps.Submit = func(_ context.Context, r daemonoperation.SubmitRequest, _ io.Writer) (*daemonv1.Operation, error) {
		if !reflect.DeepEqual(r, daemonoperation.SubmitRequest{ProjectsRoot: "root", Cwd: "/cwd", Argv: []string{"true"}}) {
			t.Fatalf("default=%+v", r)
		}
		return &daemonv1.Operation{OperationId: "id"}, nil
	}
	for _, verb := range []string{"submit", "get", "wait", "cancel"} {
		args := []string{"id"}
		if verb == "submit" {
			args = []string{"--", "true"}
		}
		out, _, err := execute(t, "operation "+verb, testRuntime(), deps, args...)
		if err != nil || !strings.HasPrefix(out, "operation ") {
			t.Fatalf("%q %v", out, err)
		}
	}
	deps.Getwd = func() (string, error) { return "", sentinel }
	if _, _, err := execute(t, "operation submit", testRuntime(), deps, "--", "true"); err != sentinel {
		t.Fatal(err)
	}
	if _, _, err := execute(t, "operation wait", testRuntime(), deps, "--progress=false", "--progress-file=log", "id"); err == nil {
		t.Fatal("invalid progress accepted")
	}
}
