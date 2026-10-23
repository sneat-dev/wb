package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestLaneReleaseReportsOwnedReleaseAndAbsentLane(t *testing.T) {
	t.Parallel()
	for _, released := range []bool{true, false} {
		t.Run(map[bool]string{true: "released", false: "already absent"}[released], func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			command := newLaneCommand(&invocation{projectsRoot: "projects"}, func(root, verb string, pid int) landinglane.Owner {
				if root != "projects" || verb != "wb lane release" || pid <= 0 {
					t.Fatalf("owner lookup %q %q %d", root, verb, pid)
				}
				return landinglane.Owner{WBSessionID: "session"}
			}, func(root string) (string, error) {
				if root != "projects" {
					t.Fatal(root)
				}
				return "home", nil
			}, func(home, repo, target, session string) (bool, error) {
				if home != "home" || repo != "acme/app" || target != "integration/team" || session != "session" {
					t.Fatalf("release args: %q %q %q %q", home, repo, target, session)
				}
				return released, nil
			})
			command.SetOut(&output)
			command.SetArgs([]string{"release", "acme/app", "--target", "integration/team"})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), map[bool]string{true: "released", false: "already absent"}[released]) {
				t.Fatal(output.String())
			}
		})
	}
}

func TestLaneReleasePropagatesValidationAndOperationalErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                string
		args                []string
		rootErr, releaseErr error
		writer              io.Writer
		want                string
	}{
		{name: "missing repo", args: []string{"release"}, want: "accepts 1 arg"},
		{name: "bad repo", args: []string{"release", "app"}, want: "owner/repository"},
		{name: "empty owner", args: []string{"release", "/app"}, want: "owner/repository"},
		{name: "empty name", args: []string{"release", "acme/"}, want: "owner/repository"},
		{name: "empty target", args: []string{"release", "acme/app", "--target="}, want: "nonempty"},
		{name: "root failure", args: []string{"release", "acme/app"}, rootErr: errors.New("root failed"), want: "root failed"},
		{name: "release refusal", args: []string{"release", "acme/app"}, releaseErr: errors.New("busy"), want: "busy"},
		{name: "output failure", args: []string{"release", "acme/app"}, writer: laneBrokenWriter{}, want: "write failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			command := newLaneCommand(&invocation{}, func(string, string, int) landinglane.Owner { return landinglane.Owner{} }, func(string) (string, error) { return "home", test.rootErr }, func(string, string, string, string) (bool, error) { return false, test.releaseErr })
			if test.writer != nil {
				command.SetOut(test.writer)
			} else {
				command.SetOut(io.Discard)
			}
			command.SetErr(io.Discard)
			command.SetArgs(test.args)
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type laneBrokenWriter struct{}

func (laneBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestLaneReleaseIsDiscoverableAndRejectsUnsupportedSelectors(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if code := run([]string{"lane", "release", "acme/app", "--filter", "other"}, &output, &diagnostics); code != exitUsage {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	output.Reset()
	diagnostics.Reset()
	if code := run([]string{"commands", "--search", "lane release", "--format", "json"}, &output, &diagnostics); code != exitOK || !strings.Contains(output.String(), "wb lane release") {
		t.Fatalf("exit %d: %s %s", code, output.String(), diagnostics.String())
	}
}

func TestLaneReleaseUsesRegisteredOwnerAgainstRealStore(t *testing.T) {
	root := t.TempDir()
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: "wbs-lane-release", Runtime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	owner := landinglane.Owner{WBSessionID: record.WBSessionID, PID: record.PID, OperationPID: 999999999}
	acquire := func() {
		t.Helper()
		if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil {
			t.Fatal(err)
		}
	}
	acquire()
	var output, diagnostics bytes.Buffer
	args := []string{"lane", "release", "acme/app", "--projects-root", root}
	for _, want := range []string{"released", "already absent"} {
		output.Reset()
		diagnostics.Reset()
		if code := run(args, &output, &diagnostics); code != exitOK || !strings.Contains(output.String(), want) {
			t.Fatalf("exit %d: %s %s", code, output.String(), diagnostics.String())
		}
	}
	owner.OperationPID = os.Getpid()
	acquire()
	output.Reset()
	diagnostics.Reset()
	if code := run(args, &output, &diagnostics); code != exitFindings || !strings.Contains(diagnostics.String(), "still active") {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	if _, held, err := landinglane.Read(home, "acme/app", "main"); err != nil || !held {
		t.Fatalf("active lane held=%v err=%v", held, err)
	}
}
