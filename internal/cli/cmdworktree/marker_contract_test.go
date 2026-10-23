package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
)

func TestMarkerCommandPreservesLazyInputAndOutputErrorCustody(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"format", "conflict", "selection", "writer", "findings", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before", Filter: "before"}
			refusal := errors.New(stage)
			calls := 0
			exits := 0
			rt := shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, message string) error {
				exits++
				if code != shared.ExitFindings || message != "1 checkout(s) could not be described" {
					t.Fatal(code, message)
				}
				return refusal
			}}
			ctx := context.WithValue(context.Background(), mergeContextKey{}, "marker")
			command := NewMarker(rt, MarkerOperations{Version: func() string { return "version" }, Run: func(actual context.Context, req checkoutsetup.MarkerRequest) ([]checkoutsetup.MarkerOutcome, error) {
				calls++
				if actual != ctx || req.Options.ProjectsRoot != "after" || req.Filter != "current" || req.Options.BaseBranch != "branch" || req.Options.Version != "wb version" || !req.DryRun {
					t.Fatalf("request=%+v context=%v", req, actual)
				}
				if stage == "selection" {
					return nil, refusal
				}
				return []checkoutsetup.MarkerOutcome{{Path: "path", Error: "failure"}}, nil
			}})
			flags.ProjectsRoot = "after"
			flags.Filter = "current"
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(io.Discard)
			command.SetContext(ctx)
			args := []string{"path", "--base", "branch", "--dry-run"}
			if stage == "format" {
				args = append(args, "--format", "bad")
			}
			if stage == "conflict" {
				args = append(args, "--fleet")
			}
			if stage == "success" {
				args = append(args, "--format", "json")
			}
			if stage == "writer" {
				command.SetOut(mergeRefusingWriter{refusal})
			}
			err := mergeExecute(command, args...)
			if stage == "format" || stage == "conflict" {
				if err == nil || calls != 0 {
					t.Fatal(err, calls)
				}
				return
			}
			if err != refusal {
				t.Fatalf("error identity=%v", err)
			}
			if stage == "selection" || stage == "writer" {
				if exits != 0 {
					t.Fatal("findings replaced earlier error")
				}
			} else if exits != 1 || out.Len() == 0 {
				t.Fatal(exits, out.String())
			}
			if stage == "success" && !strings.HasSuffix(out.String(), "]\n") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestOwnCommandKeepsAdmissionReleaseAndIgnoredWriterFailure(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"admission", "record", "success", "writer"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			refusal := errors.New(stage)
			released := 0
			recorded := 0
			command := NewOwn(OwnOperations{Admission: func(_ *cobra.Command, apply bool) (worktrees.AgentIdentity, func(), error) {
				if !apply {
					t.Fatal("mutation not admitted")
				}
				if stage == "admission" {
					return worktrees.AgentIdentity{}, func() { released++ }, refusal
				}
				return worktrees.AgentIdentity{Registered: true, PID: 4}, func() { released++ }, nil
			}, Record: func(req checkoutsetup.OwnershipRequest) (checkoutsetup.OwnershipResult, error) {
				recorded++
				if req.Path != "." || !req.Admitted.Registered || req.Overrides.Runtime != "flag" || req.Overrides.PID != 8 {
					t.Fatal(req)
				}
				if stage == "record" {
					return checkoutsetup.OwnershipResult{}, refusal
				}
				return checkoutsetup.OwnershipResult{Path: "absolute", Identity: worktrees.AgentIdentity{Runtime: "owner", PID: 4}}, nil
			}})
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(io.Discard)
			if stage == "writer" {
				command.SetOut(mergeRefusingWriter{refusal})
			}
			err := mergeExecute(command, "--runtime", "flag", "--pid", "8")
			if stage == "admission" {
				if err != refusal || recorded != 0 || released != 0 {
					t.Fatal(err, recorded, released)
				}
			} else if stage == "record" {
				if err != refusal || released != 1 {
					t.Fatal(err, released)
				}
			} else if err != nil || released != 1 || recorded != 1 {
				t.Fatal(err, released, recorded)
			}
			if stage == "success" && !strings.Contains(out.String(), "absolute: owner recorded as owner (pid 4)") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestCwWtMarkerWriterSweep(t *testing.T) {
	t.Parallel()
	outcomes := []checkoutsetup.MarkerOutcome{{Path: "/a", Kind: "canonical", MarkerWritten: true, ExcludeWritten: true}, {Path: "/b", Kind: "worktree", MarkerWritten: true}, {Path: "/c", Kind: "worktree", ExcludeWritten: true}, {Path: "/d", Kind: "worktree"}, {Path: "/e", Kind: "worktree", Error: "boom"}}
	finished := false
	for allow := 0; allow <= 10; allow++ {
		if err := renderMarkerOutcomes(&markerFailureWriter{Allow: allow}, "text", false, outcomes); err == nil {
			finished = true
			break
		}
	}
	if !finished {
		t.Fatal("no write budget up to10 let renderer finish")
	}
	for allow := 0; allow < 6; allow++ {
		if err := renderMarkerOutcomes(&markerFailureWriter{Allow: allow}, "text", false, outcomes); err == nil {
			t.Fatalf("write failure ignored at budget%d", allow)
		}
	}
}

func TestMarkerAndOwnSuccessfulExplicitPathContracts(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	marker := NewMarker(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, MarkerOperations{Version: func() string { return "test" }, Run: func(context.Context, checkoutsetup.MarkerRequest) ([]checkoutsetup.MarkerOutcome, error) {
		return nil, nil
	}})
	marker.SetOut(&output)
	marker.SetErr(io.Discard)
	if err := mergeExecute(marker, "--format", "json"); err != nil || output.String() != "null\n" {
		t.Fatal(err, output.String())
	}
	released := false
	own := NewOwn(OwnOperations{Admission: func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() { released = true }, nil
	}, Record: func(req checkoutsetup.OwnershipRequest) (checkoutsetup.OwnershipResult, error) {
		if req.Path != "explicit" {
			t.Fatal(req.Path)
		}
		return checkoutsetup.OwnershipResult{Path: req.Path}, nil
	}})
	own.SetOut(io.Discard)
	own.SetErr(io.Discard)
	if err := mergeExecute(own, "explicit"); err != nil || !released {
		t.Fatal(err, released)
	}
}
