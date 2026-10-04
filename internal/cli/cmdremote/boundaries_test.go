package cmdremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/spf13/cobra"
)

var errBoundaryWrite = errors.New("private writer refusal")

type boundaryWriter struct{ calls int }

func (w *boundaryWriter) Write([]byte) (int, error) { w.calls++; return 0, errBoundaryWrite }
func boundaryCommand(flags *shared.Flags, ops Operations) *cobra.Command {
	c := New(shared.Runtime{Flags: func() shared.Flags { return *flags }}, ops)
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	return c
}

func TestRemoteConstructorsRejectArgumentsAndFormatsBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"claim", "release", "claims", "machines", "status", "enroll", "publish"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{}
			c := boundaryCommand(&flags, Operations{})
			args := []string{name, "unexpected"}
			if name == "claim" || name == "release" {
				args = []string{name}
			}
			c.SetArgs(args)
			if err := c.Execute(); err == nil {
				t.Fatal("argument refusal missing")
			}
			c = boundaryCommand(&flags, Operations{})
			args = []string{name, "--format=toml"}
			if name == "claim" || name == "release" {
				args = append(args, "task")
			}
			c.SetArgs(args)
			if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "toml") {
				t.Fatalf("format refusal: %v", err)
			}
		})
	}
}

func TestRemoteClaimAndReleaseReadCurrentFlagsAndWriters(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before"}
	var claims []remoterun.ClaimRequest
	var releases []remoterun.ReleaseRequest
	c := boundaryCommand(&flags, Operations{
		Claim: func(req remoterun.ClaimRequest) (remoterun.ClaimResult, error) {
			claims = append(claims, req)
			return remoterun.ClaimResult{Text: "claimed\n"}, nil
		},
		Release: func(req remoterun.ReleaseRequest) (remoterun.ReleaseResult, error) {
			releases = append(releases, req)
			return remoterun.ReleaseResult{Text: "released\n"}, nil
		},
	})
	for _, name := range []string{"claim", "release"} {
		flags.ProjectsRoot = "current-" + name
		var out bytes.Buffer
		c.SetOut(&out)
		args := []string{name, "task", "--force"}
		if name == "claim" {
			args = append(args, "--take-over", "--note=hello", "--stale=2h")
		}
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		if out.String() != map[string]string{"claim": "claimed\n", "release": "released\n"}[name] {
			t.Fatalf("bound output: %q", out.String())
		}
	}
	if len(claims) != 1 || claims[0] != (remoterun.ClaimRequest{ProjectsRoot: "current-claim", Task: "task", Note: "hello", TakeOver: true, Force: true, Stale: 2 * time.Hour}) {
		t.Fatalf("claim: %+v", claims)
	}
	if len(releases) != 1 || releases[0] != (remoterun.ReleaseRequest{ProjectsRoot: "current-release", Task: "task", Force: true}) {
		t.Fatalf("release: %+v", releases)
	}
}

func TestRemoteReadsUseCurrentRootAndPreserveWriterPolicy(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"machines", "claims", "status"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before"}
			var roots []string
			ops := Operations{
				Machines: func(root string, stale time.Duration) ([]remoterun.MachineRow, error) {
					roots = append(roots, root)
					if stale != 3*time.Hour {
						t.Fatalf("stale=%s", stale)
					}
					return nil, nil
				},
				Claims: func(root string, stale time.Duration) ([]remoterun.ClaimRow, error) {
					roots = append(roots, root)
					if stale != 3*time.Hour {
						t.Fatalf("stale=%s", stale)
					}
					return nil, nil
				},
				Status: func(req remoterun.StatusRequest, p remoterun.StatusProgress) (remoterun.StatusResult, error) {
					roots = append(roots, req.ProjectsRoot)
					if req.Stale != 3*time.Hour {
						t.Fatalf("stale=%s", req.Stale)
					}
					p.Start("refresh")
					p.Finish("finished")
					return remoterun.StatusResult{}, nil
				},
			}
			c := boundaryCommand(&flags, ops)
			for i, args := range [][]string{{name, "--stale=3h"}, {name, "--stale=3h", "--json"}} {
				flags.ProjectsRoot = "current"
				w := &boundaryWriter{}
				c.SetOut(w)
				c.SetArgs(args)
				err := c.Execute()
				if i == 0 && err != nil {
					t.Fatalf("text writer error policy: %v", err)
				}
				if i == 1 && !errors.Is(err, errBoundaryWrite) {
					t.Fatalf("JSON writer error identity: %v", err)
				}
				if w.calls == 0 {
					t.Fatal("bound writer unused")
				}
			}
			if !reflect.DeepEqual(roots, []string{"current", "current"}) {
				t.Fatalf("roots=%v", roots)
			}
		})
	}
}

func TestRemoteOperationsFailBeforeWriting(t *testing.T) {
	t.Parallel()
	refused := errors.New("typed operation refusal")
	ops := Operations{
		Claim: func(remoterun.ClaimRequest) (remoterun.ClaimResult, error) { return remoterun.ClaimResult{}, refused },
		Release: func(remoterun.ReleaseRequest) (remoterun.ReleaseResult, error) {
			return remoterun.ReleaseResult{}, refused
		},
		Machines: func(string, time.Duration) ([]remoterun.MachineRow, error) { return nil, refused },
		Claims:   func(string, time.Duration) ([]remoterun.ClaimRow, error) { return nil, refused },
		Status: func(remoterun.StatusRequest, remoterun.StatusProgress) (remoterun.StatusResult, error) {
			return remoterun.StatusResult{}, refused
		},
		Enroll: func(context.Context, remoterun.EnrollRequest) (remoterun.EnrollResult, error) {
			return remoterun.EnrollResult{}, refused
		},
		Publish: func(remotepublish.Request, remotepublish.Progress, io.Writer) (remotepublish.Result, error) {
			return remotepublish.Result{}, refused
		},
	}
	for _, name := range []string{"claim", "release", "machines", "claims", "status", "enroll", "publish"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{}
			c := boundaryCommand(&flags, ops)
			w := &boundaryWriter{}
			c.SetOut(w)
			args := []string{name}
			if name == "claim" || name == "release" {
				args = append(args, "task")
			}
			c.SetArgs(args)
			if err := c.Execute(); err != refused {
				t.Fatalf("error identity=%v", err)
			}
			if w.calls != 0 {
				t.Fatalf("wrote before refusal: %d", w.calls)
			}
		})
	}
}

func TestRemoteClaimReleaseAndEnrollmentWriterErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"claim", "release", "enroll"} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/text", true: "/json"}[jsonOut], func(t *testing.T) {
				t.Parallel()
				flags := shared.Flags{}
				c := boundaryCommand(&flags, Operations{
					Claim: func(remoterun.ClaimRequest) (remoterun.ClaimResult, error) {
						return remoterun.ClaimResult{Text: "claimed"}, nil
					},
					Release: func(remoterun.ReleaseRequest) (remoterun.ReleaseResult, error) {
						return remoterun.ReleaseResult{Text: "released"}, nil
					},
					Enroll: func(context.Context, remoterun.EnrollRequest) (remoterun.EnrollResult, error) {
						return remoterun.EnrollResult{}, nil
					},
				})
				w := &boundaryWriter{}
				c.SetOut(w)
				args := []string{name}
				if name != "enroll" {
					args = append(args, "task")
				}
				if jsonOut {
					args = append(args, "--json")
				}
				c.SetArgs(args)
				if err := c.Execute(); !errors.Is(err, errBoundaryWrite) || w.calls != 1 {
					t.Fatalf("writer: %v calls=%d", err, w.calls)
				}
			})
		}
	}
	var out bytes.Buffer
	if err := writeClaimOutcome(&out, false, remotestate.ClaimOutcome{}, ""); err != nil || out.Len() != 0 {
		t.Fatalf("empty claim: %v %q", err, out.String())
	}
}

func TestRemoteEnrollmentUsesCurrentContextInputAndFlags(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "old"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	in := strings.NewReader("private credential\n")
	var got remoterun.EnrollRequest
	c := boundaryCommand(&flags, Operations{Enroll: func(actual context.Context, req remoterun.EnrollRequest) (remoterun.EnrollResult, error) {
		if actual != ctx {
			t.Fatal("context changed")
		}
		got = req
		return remoterun.EnrollResult{Machine: req.Machine, DaemonRestart: req.RestartDaemon}, nil
	}})
	flags.ProjectsRoot = "current"
	c.SetContext(ctx)
	c.SetIn(in)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"enroll", "--machine=studio", "--token-stdin", "--token-file=private-file", "--restart-daemon=false", "--json"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.ProjectsRoot != "current" || got.Machine != "studio" || got.HubURL != defaultRemoteHubURL || got.TokenFile != "private-file" || !got.TokenStdin || got.RestartDaemon || got.Input != in {
		t.Fatalf("request=%+v", got)
	}
	if strings.Contains(out.String(), "private credential") || !strings.Contains(out.String(), `"daemon_restart":false`) {
		t.Fatalf("output=%q", out.String())
	}
}

func TestRemotePublicationNotesStayOnCurrentStderrAndJSONOnStdout(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "old", NonInteractive: true}
	var requests []remotepublish.Request
	c := boundaryCommand(&flags, Operations{Publish: func(req remotepublish.Request, p remotepublish.Progress, notes io.Writer) (remotepublish.Result, error) {
		requests = append(requests, req)
		if _, err := io.WriteString(notes, "private diagnostic\n"); err != nil {
			return remotepublish.Result{}, err
		}
		p.Start(0)
		p.Phase("private phase")
		p.Finish("done")
		return remotepublish.Result{DryRun: true}, nil
	}})
	for _, root := range []string{"first", "second"} {
		flags.ProjectsRoot = root
		flags.Filter = "team/*"
		var out, notes bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&notes)
		c.SetArgs([]string{"publish", "--dry-run", "--parallel=2", "--json"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		var snapshot remotestate.Snapshot
		if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
			t.Fatalf("JSON isolation: %v %q", err, out.String())
		}
		if strings.Contains(out.String(), "private") || !strings.Contains(notes.String(), "private diagnostic") {
			t.Fatalf("stdout=%q stderr=%q", out.String(), notes.String())
		}
	}
	want := []remotepublish.Request{{ProjectsRoot: "first", Filter: "team/*", Parallel: 2, DryRun: true}, {ProjectsRoot: "second", Filter: "team/*", Parallel: 2, DryRun: true}}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%+v", requests)
	}
}

func TestRemoteStatusMissingMachineDiagnosticUsesBoundStderr(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	c := boundaryCommand(&flags, Operations{Status: func(req remoterun.StatusRequest, p remoterun.StatusProgress) (remoterun.StatusResult, error) {
		if req.Machine != "missing" {
			t.Fatal(req)
		}
		p.Start("refresh")
		p.Finish("done")
		return remoterun.StatusResult{MissingMachine: true}, nil
	}})
	var out, diag bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&diag)
	c.SetArgs([]string{"status", "--machine=missing", "--json"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(diag.String(), "no machine missing in the remote store\n") {
		t.Fatalf("diagnostic=%q", diag.String())
	}
	if strings.Contains(out.String(), "no machine") || !json.Valid(out.Bytes()) {
		t.Fatalf("JSON=%q", out.String())
	}
	c.SetArgs([]string{"status", "--machine=missing", "--format=text"})
	out.Reset()
	diag.Reset()
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.HasSuffix(diag.String(), "no machine missing in the remote store\n") {
		t.Fatalf("missing text=%q diag=%q", out.String(), diag.String())
	}
}
