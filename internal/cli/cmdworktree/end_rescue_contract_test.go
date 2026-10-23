package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
)

type endInventoryFunc func(context.Context, string, string, string) ([]worktreeend.Worktree, error)

func (f endInventoryFunc) Worktrees(c context.Context, r, t, p string) ([]worktreeend.Worktree, error) {
	return f(c, r, t, p)
}

type endCapturePort struct{ dirtyErr error }

func (p endCapturePort) DirtyPaths(context.Context, string) ([]string, error) { return nil, p.dirtyErr }
func (endCapturePort) Preserve(context.Context, string, string) (string, error) {
	panic("clean fixture must not capture")
}

type endLinkPort struct{}

func (endLinkPort) LiveLinks(string) ([]string, []string, error) {
	return []string{"linked"}, []string{"unlink"}, nil
}

type endRetirePort struct{ calls *[]string }

func (p endRetirePort) Retire(_ context.Context, r, t, repo, w string) error {
	*p.calls = append(*p.calls, r, t, repo, w)
	return nil
}

type endNotePort struct{ got *string }

func (p endNotePort) Seal(_ string, n string) (string, error) { *p.got = n; return "note", nil }

func endRescueExecute(command *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestEndCommandUsesLazyFlagsContextAndActualEngine(t *testing.T) {
	t.Parallel()
	root := "before"
	ctx := context.WithValue(context.Background(), receiptContextKey{}, "value")
	var retired []string
	var note string
	runtime := endRescueRuntime()
	runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
	factory := func(r string, w io.Writer) (*worktreeend.Engine, error) {
		if r != "parsed" || w != io.Discard {
			t.Fatalf("factory(%q,%v)", r, w)
		}
		return &worktreeend.Engine{ProjectsRoot: r, Inventory: endInventoryFunc(func(c context.Context, r, task, repo string) ([]worktreeend.Worktree, error) {
			if c != ctx || r != "parsed" || task != "task" || repo != "acme/app" {
				t.Fatalf("inventory inputs %v %q %q %q", c, r, task, repo)
			}
			return []worktreeend.Worktree{{Repository: repo, Path: "/private"}}, nil
		}), Capture: endCapturePort{}, Notes: endNotePort{&note}, Retirer: endRetirePort{&retired}}, nil
	}
	command := NewEnd(runtime, factory)
	command.SetContext(ctx)
	root = "parsed"
	out, err := endRescueExecute(command, "task", "--repo", "acme/app", "--apply", "--no-capture", "--note", "closing", "--format", "json")
	if err != nil || !strings.Contains(out, "\"applied\": true") || note != "closing" || !reflect.DeepEqual(retired, []string{"parsed", "task", "acme/app", "/private"}) {
		t.Fatalf("out=%s err=%v note=%q retired=%v", out, err, note, retired)
	}
}
func TestEndCommandPreservesFactoryEngineRefusalAndWriterPrecedence(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation failed")
	for _, kind := range []string{"format", "factory", "engine", "refusal", "finding", "writer", "plan"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			factory := func(string, io.Writer) (*worktreeend.Engine, error) {
				calls++
				if kind == "factory" {
					return nil, boom
				}
				e := &worktreeend.Engine{Inventory: endInventoryFunc(func(context.Context, string, string, string) ([]worktreeend.Worktree, error) {
					if kind == "engine" {
						return nil, boom
					}
					return []worktreeend.Worktree{{Path: "/private"}}, nil
				}), Capture: endCapturePort{}}
				if kind == "refusal" {
					e.Links = endLinkPort{}
				}
				if kind == "finding" || kind == "writer" {
					e.Capture = endCapturePort{dirtyErr: boom}
				}
				return e, nil
			}
			command := NewEnd(endRescueRuntime(), factory)
			args := []string{"task"}
			if kind == "format" {
				args = append(args, "--format", "xml")
			}
			if kind == "writer" {
				command.SetOut(&cwWtFailWriter{Allow: 0})
				command.SetErr(io.Discard)
				command.SetArgs(args)
				err := command.Execute()
				if !errors.Is(err, errCwWtWrite) {
					t.Fatalf("writer must win: %v", err)
				}
				return
			}
			out, err := endRescueExecute(command, args...)
			switch kind {
			case "format":
				if err == nil || calls != 0 {
					t.Fatalf("format %v calls %d", err, calls)
				}
			case "factory", "engine":
				if !errors.Is(err, boom) {
					t.Fatalf("identity %v", err)
				}
			case "refusal":
				if err == nil || !strings.Contains(err.Error(), "exit 2") || strings.Contains(out, "would end task") {
					t.Fatalf("refusal %v %q", err, out)
				}
			case "finding":
				if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.Contains(out, "operation failed") {
					t.Fatalf("findings %v %q", err, out)
				}
			case "plan":
				if err != nil || !strings.Contains(out, "would end") {
					t.Fatalf("plan %v %q", err, out)
				}
			}
		})
	}
}
func TestRescueCommandPreservesOrderedOperationsAndCurrentInputs(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), receiptContextKey{}, "ctx")
	root := "before"
	runtime := endRescueRuntime()
	runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
	var order []string
	report := canonicalrescue.Report{Path: "/clone", Changes: []canonicalrescue.Change{{Path: "secret.go", Status: "M"}}}
	ops := RescueOperations{Inspect: func(c context.Context, p string, o canonicalrescue.Options) (canonicalrescue.Report, error) {
		if c != ctx || p != "/clone" || o.ProjectsRoot != "after" || o.Branch != "rescue/custom" {
			t.Fatalf("inspect %v %q %+v", c, p, o)
		}
		order = append(order, "inspect")
		return report, nil
	}, Capture: func(c context.Context, r canonicalrescue.Report) (canonicalrescue.Report, error) {
		if c != ctx || !reflect.DeepEqual(r, report) {
			t.Fatal("capture inputs")
		}
		order = append(order, "capture")
		r.RescueBranch = "rescue/custom"
		return r, nil
	}, Push: func(c context.Context, r canonicalrescue.Report, remote string) (canonicalrescue.Report, error) {
		if c != ctx || remote != "upstream" || r.RescueBranch != "rescue/custom" {
			t.Fatal("push inputs")
		}
		order = append(order, "push")
		r.Pushed = true
		return r, nil
	}, Restore: func(c context.Context, r canonicalrescue.Report, allow bool) (canonicalrescue.Report, error) {
		if c != ctx || !allow || !r.Pushed {
			t.Fatal("restore inputs")
		}
		order = append(order, "restore")
		r.Restored = true
		return r, nil
	}}
	command := NewRescue(runtime, ops)
	command.SetContext(ctx)
	root = "after"
	out, err := endRescueExecute(command, "/clone", "--apply", "--push", "--restore", "--allow-unpushed", "--branch", "rescue/custom", "--remote", "upstream")
	if err != nil || !reflect.DeepEqual(order, []string{"inspect", "capture", "push", "restore"}) || !strings.Contains(out, "is now clean") {
		t.Fatalf("order=%v out=%s err=%v", order, out, err)
	}
}
func TestRescueCommandValidationAndEveryOperationFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation failed")
	for _, kind := range []string{"format", "fleet-path", "fleet-apply", "restore", "inspect", "capture", "push", "restore-failure", "clean", "report", "apply"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var calls []string
			report := canonicalrescue.Report{Path: ".", Changes: []canonicalrescue.Change{{Path: "a"}}}
			if kind == "clean" {
				report.Changes = nil
			}
			op := func(n string) (canonicalrescue.Report, error) {
				calls = append(calls, n)
				if kind == n || kind == "restore-failure" && n == "restore" {
					return report, boom
				}
				return report, nil
			}
			ops := RescueOperations{Inspect: func(_ context.Context, p string, _ canonicalrescue.Options) (canonicalrescue.Report, error) {
				if p != "." {
					t.Fatal(p)
				}
				return op("inspect")
			}, Capture: func(context.Context, canonicalrescue.Report) (canonicalrescue.Report, error) { return op("capture") }, Push: func(context.Context, canonicalrescue.Report, string) (canonicalrescue.Report, error) {
				return op("push")
			}, Restore: func(context.Context, canonicalrescue.Report, bool) (canonicalrescue.Report, error) {
				return op("restore")
			}}
			args := []string{}
			switch kind {
			case "format":
				args = []string{"--format", "xml"}
			case "fleet-path":
				args = []string{"--fleet", "/clone"}
			case "fleet-apply":
				args = []string{"--fleet", "--apply"}
			case "restore":
				args = []string{"--restore"}
			case "capture", "push", "restore-failure":
				args = []string{"--apply", "--push", "--restore"}
			case "apply":
				args = []string{"--apply"}
			}
			out, err := endRescueExecute(NewRescue(endRescueRuntime(), ops), args...)
			switch kind {
			case "format", "fleet-path", "fleet-apply", "restore":
				if err == nil || len(calls) != 0 || strings.Contains(err.Error(), "exit ") {
					t.Fatalf("ordinary validation %v calls %v", err, calls)
				}
			case "inspect", "capture", "push", "restore-failure":
				if !errors.Is(err, boom) {
					t.Fatalf("identity %v", err)
				}
			case "clean", "apply":
				if err != nil {
					t.Fatal(err)
				}
			case "report":
				if err == nil || !strings.Contains(out, "Nothing has been changed") {
					t.Fatalf("report %v %s", err, out)
				}
			}
		})
	}
}
func TestRescueFleetFiltersUsesNativePathsSkipsErrorsAndSorts(t *testing.T) {
	t.Parallel()
	runtime := endRescueRuntime()
	runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: "/root", Filter: "acme"} }
	var inspected []string
	ops := RescueOperations{ScanLocal: func(root string) ([]discover.Repo, error) {
		if root != "/root" {
			t.Fatal(root)
		}
		return []discover.Repo{{Org: "other", Path: "/excluded"}, {Org: "acme"}, {Org: "acme", Path: "/z"}, {Org: "acme", Path: "/broken"}, {Org: "acme", Path: "/clean"}, {Org: "acme", Path: "/a"}}, nil
	}, Inspect: func(_ context.Context, path string, o canonicalrescue.Options) (canonicalrescue.Report, error) {
		inspected = append(inspected, path)
		if o.ProjectsRoot != "/root" {
			t.Fatal(o)
		}
		if path == "/broken" {
			return canonicalrescue.Report{}, errors.New("unreadable")
		}
		report := canonicalrescue.Report{Path: path}
		if path != "/clean" {
			report.Changes = []canonicalrescue.Change{{Path: "private"}}
		}
		return report, nil
	}}
	out, err := endRescueExecute(NewRescue(runtime, ops), "--fleet")
	if err == nil || !strings.Contains(err.Error(), "exit 1") || strings.Index(out, "/a") > strings.Index(out, "/z") || strings.Contains(out, "private") || !reflect.DeepEqual(inspected, []string{"/z", "/broken", "/clean", "/a"}) {
		t.Fatalf("out=%q err=%v inspected=%v", out, err, inspected)
	}
	out, err = endRescueExecute(NewRescue(runtime, ops), "--fleet", "--format", "json")
	if err != nil || !strings.Contains(out, "private") {
		t.Fatalf("json %q %v", out, err)
	}
}
func TestEndRescueRenderersPropagateEveryWriteAndJSONFailure(t *testing.T) {
	t.Parallel()
	end := worktreeend.Result{Task: "task", Members: []worktreeend.MemberResult{{Repository: "acme/app", Dirty: []string{"a"}, CaptureRef: "ref", Detail: "reason"}}, ClaimOutcome: "released"}
	changes := make([]canonicalrescue.Change, 25)
	report := canonicalrescue.Report{Path: "/clone", Changes: changes, Pushed: true, Restored: true}
	runtime := endRescueRuntime()
	for _, which := range []string{"end-text", "end-json", "rescue-text", "rescue-json", "rescue-plan", "fleet", "fleet-clean", "fleet-json"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			render := func(w io.Writer) error {
				cmd := &cobra.Command{}
				cmd.SetOut(w)
				switch which {
				case "end-text":
					return printWorktreeEnd(cmd, "text", end)
				case "end-json":
					return printWorktreeEnd(cmd, "json", end)
				case "rescue-text":
					return renderRescueReport(runtime, cmd, "text", true, report)
				case "rescue-json":
					return renderRescueReport(runtime, cmd, "json", true, report)
				case "rescue-plan":
					return renderRescueReport(runtime, cmd, "text", false, report)
				default:
					ops := RescueOperations{ScanLocal: func(string) ([]discover.Repo, error) {
						if which == "fleet-clean" {
							return nil, nil
						}
						return []discover.Repo{{Path: "/clone"}}, nil
					}, Inspect: func(context.Context, string, canonicalrescue.Options) (canonicalrescue.Report, error) {
						return report, nil
					}}
					format := "text"
					if which == "fleet-json" {
						format = "json"
					}
					return runFleetRescueReport(runtime, ops, cmd, format)
				}
			}
			successful := &cwWtFailWriter{Allow: 100}
			_ = render(successful)
			for allow := 0; allow < successful.Writes; allow++ {
				writer := &cwWtFailWriter{Allow: allow}
				err := render(writer)
				if !errors.Is(err, errCwWtWrite) {
					t.Fatalf("%s ignored failure at %d", which, allow)
				}
				if err == nil {
					break
				}
			}
			if err := render(&cwWtFailWriter{Allow: 0}); err == nil {
				t.Fatal("first write failure ignored")
			}
		})
	}
	ops := RescueOperations{ScanLocal: func(string) ([]discover.Repo, error) { return nil, io.ErrUnexpectedEOF }}
	_, err := endRescueExecute(NewRescue(runtime, ops), "--fleet")
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "scan local repositories") {
		t.Fatal(err)
	}
}
