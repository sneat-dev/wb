package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"io"
	"strings"
	"testing"
)

func TestInfoAdapterPreservesRedactionArgumentsAndErrors(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"text", "json", "writer", "operation", "yaml", "default"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			projects := "old"
			calls := 0
			sentinel := errors.New("info failure")
			inspect := func(_ context.Context, q worktreerun.InfoRequest) (worktreerun.InfoDocument, error) {
				calls++
				want := "checkout"
				if mode == "default" {
					want = "."
				}
				if q.ProjectsRoot != "current" || q.Worktree != want {
					t.Fatalf("request=%+v", q)
				}
				if mode == "operation" {
					return worktreerun.InfoDocument{}, sentinel
				}
				return worktreerun.InfoDocument{MergerLaneClaim: &orchestrate.MergeLaneClaim{Lane: "lane", Target: "main", Status: "claimed", ReceiptPath: "receipt"}, Collaboration: &worktreecollab.View{Owner: "owner", OwnerStatus: "live", Checkout: worktreecollab.Checkout{ID: "checkout"}, Joined: []worktreecollab.Participant{{SessionID: "peer", Live: true}}}}, nil
			}
			command := NewInfo(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projects} }}, inspect)
			projects = "current"
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(io.Discard)
			command.SilenceErrors = true
			command.SilenceUsage = true
			args := []string{"checkout"}
			if mode == "default" {
				args = nil
			}
			if mode == "json" || mode == "yaml" {
				args = append(args, "--format", mode)
			}
			if mode == "writer" {
				command.SetOut(collaborationWriterError{sentinel})
			}
			command.SetArgs(args)
			err := command.Execute()
			if mode == "yaml" {
				if err == nil || calls != 0 {
					t.Fatalf("invalid format=%v calls=%d", err, calls)
				}
				return
			}
			if mode == "writer" || mode == "operation" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if !strings.Contains(out.String(), "lane") || !strings.Contains(out.String(), "peer") {
				t.Fatalf("output=%s", out.String())
			}
		})
	}
}
func TestInfoAdapterOmitsAbsentAuthorityAndPropagatesJSONWriteError(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("encode")
			c := NewInfo(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, func(context.Context, worktreerun.InfoRequest) (worktreerun.InfoDocument, error) {
				return worktreerun.InfoDocument{}, nil
			})
			c.SetArgs([]string{"--format", format})
			c.SetOut(collaborationWriterError{sentinel})
			c.SetErr(io.Discard)
			if err := c.Execute(); !errors.Is(err, sentinel) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
