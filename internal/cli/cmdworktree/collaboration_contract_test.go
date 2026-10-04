package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
)

type collaborationRecording struct {
	err    error
	called string
	args   []string
	force  bool
	inbox  worktreecollab.InboxView
}

func (r *collaborationRecording) Join(_ context.Context, p string) (worktreecollab.State, error) {
	r.called = "join"
	r.args = []string{p}
	return worktreecollab.State{}, r.err
}
func (r *collaborationRecording) Leave(_ context.Context, p string) (worktreecollab.State, error) {
	r.called = "leave"
	r.args = []string{p}
	return worktreecollab.State{}, r.err
}
func (r *collaborationRecording) Take(_ context.Context, p, e string, f bool, why string) (worktreecollab.State, error) {
	r.called = "take-ownership"
	r.args = []string{p, e, why}
	r.force = f
	return worktreecollab.State{}, r.err
}
func (r *collaborationRecording) Transfer(_ context.Context, p, s string) (worktreecollab.State, error) {
	r.called = "transfer-ownership"
	r.args = []string{p, s}
	return worktreecollab.State{}, r.err
}
func (r *collaborationRecording) Send(_ context.Context, p, k string, to []string, b string) (worktreecollab.SendReceipt, bool, error) {
	r.called = "send"
	r.args = append([]string{p, k, b}, to...)
	return worktreecollab.SendReceipt{}, true, r.err
}
func (r *collaborationRecording) Inbox(_ context.Context, p string) (worktreecollab.InboxView, error) {
	r.called = "inbox"
	r.args = []string{p}
	return r.inbox, r.err
}
func (r *collaborationRecording) Ack(_ context.Context, p, m string) (uint64, error) {
	r.called = "ack"
	r.args = []string{p, m}
	return 3, r.err
}

type collaborationWriterError struct{ err error }

func (w collaborationWriterError) Write([]byte) (int, error) { return 0, w.err }

type collaborationReaderError struct{ err error }

func (r collaborationReaderError) Read([]byte) (int, error) { return 0, r.err }

func TestCollaborationAdapterReadsCurrentFlagsAndPreservesFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, args []string
		method     string
	}{
		{[]string{"join"}, []string{"checkout"}, "join"},
		{[]string{"leave"}, []string{"checkout"}, "leave"},
		{[]string{"take-ownership"}, []string{"checkout", "--expected-owner", "owner", "--force", "--reason", "audit"}, "take-ownership"},
		{[]string{"transfer-ownership"}, []string{"checkout", "--to-session", "peer"}, "transfer-ownership"},
		{[]string{"message", "send"}, []string{"checkout", "--to-session", "peer", "--idempotency-key", "key", "--message-file", "-"}, "send"},
		{[]string{"message", "inbox"}, []string{"checkout"}, "inbox"},
		{[]string{"message", "ack"}, []string{"checkout", "message"}, "ack"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			for _, failure := range []string{"operation", "writer", "success"} {
				sentinel := errors.New("sentinel " + failure)
				record := &collaborationRecording{inbox: worktreecollab.InboxView{Notice: &worktreecollab.OwnerChange{}}}
				if failure == "operation" {
					record.err = sentinel
				}
				projects := "old"
				factories := 0
				root := &cobra.Command{Use: "worktree"}
				root.AddGroup(&cobra.Group{ID: "recover", Title: "Recovery"})
				children := CollaborationCommands(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projects} }}, func(p string) (CollaborationOperations, error) {
					factories++
					if p != "current" {
						t.Fatalf("root=%s", p)
					}
					return record, nil
				})
				for _, c := range children {
					if c.GroupID != "recover" {
						t.Fatal("group lost")
					}
				}
				root.AddCommand(children...)
				projects = "current"
				var out bytes.Buffer
				root.SetOut(&out)
				if failure == "writer" {
					root.SetOut(collaborationWriterError{sentinel})
				}
				root.SetErr(io.Discard)
				root.SetIn(strings.NewReader("body"))
				root.SilenceErrors = true
				root.SilenceUsage = true
				root.SetArgs(append(append([]string{}, tc.path...), tc.args...))
				err := root.Execute()
				if failure == "success" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, sentinel) {
					t.Fatalf("error precedence=%v", err)
				}
				if factories != 1 || record.called != tc.method || record.args[0] != "checkout" {
					t.Fatalf("calls=%d record=%+v", factories, record)
				}
				if tc.method == "take-ownership" && (!record.force || record.args[1] != "owner" || record.args[2] != "audit") {
					t.Fatalf("take=%+v", record)
				}
				if tc.method == "send" && strings.Join(record.args, "|") != "checkout|key|body|peer" {
					t.Fatalf("send=%v", record.args)
				}
			}
		})
	}
}
func TestCollaborationAdapterRefusesBodyReadFailureBeforeSend(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read failed")
	r := &collaborationRecording{}
	commands := CollaborationCommands(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, func(string) (CollaborationOperations, error) { return r, nil })
	root := &cobra.Command{Use: "worktree"}
	root.AddGroup(&cobra.Group{ID: "recover", Title: "Recovery"})
	root.AddCommand(commands...)
	root.SetIn(collaborationReaderError{sentinel})
	root.SetErr(io.Discard)
	root.SetArgs([]string{"message", "send", "checkout", "--to-session", "peer", "--idempotency-key", "key", "--message-file", "-"})
	if err := root.Execute(); !errors.Is(err, sentinel) || r.called != "" {
		t.Fatalf("err=%v called=%s", err, r.called)
	}
}
