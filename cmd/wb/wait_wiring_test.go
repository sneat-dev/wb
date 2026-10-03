package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// The real root binds current per-instance flags, the context reader, native registration and
// cleanup. Snapshot decision details belong to waitrun's reader tests.
func TestWaitRootBindsCurrentProjectsRootAndReleasesNativeRegistration(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	home, err := wbhome.Root(projects)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
		reads++
		records, err := waitregistry.List(home, waitregistry.Options{})
		if err != nil || len(records) != 1 || records[0].PID != os.Getpid() || records[0].Until != "closed" || len(records[0].Targets) != 1 || records[0].Targets[0] != "acme/app#12" {
			t.Fatalf("active native registration: %+v %v", records, err)
		}
		body := `{}`
		switch {
		case strings.HasSuffix(req.Endpoint, "/pulls/12"):
			body = `{"number":12,"state":"closed","merged":false,"head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`
		case strings.Contains(req.Endpoint, "/check-runs"):
			body = `{"total_count":0,"check_runs":[]}`
		case strings.Contains(req.Endpoint, "/actions/runs"):
			body = `{"total_count":0,"workflow_runs":[]}`
		case strings.Contains(req.Endpoint, "/status"):
			body = `{"state":"success","statuses":[]}`
		case strings.Contains(req.Endpoint, "/rules/branches/"):
			body = `[]`
		case strings.HasSuffix(req.Endpoint, "/branches/main"):
			body = `{"protected":false}`
		default:
			t.Fatalf("unexpected observation request %s", req.Endpoint)
		}
		return githubobserver.Response{Body: []byte(body), StatusCode: 200}, nil
	}, Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{ExitCode: 1}
	}})
	inv := &invocation{}
	command := newRootCmdFor(inv)
	inv.projectsRoot = projects
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--non-interactive", "wait", "pr", "acme/app#12", "--until=closed", "--json"})
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatal(err, out.String(), errOut.String())
	}
	var report struct {
		Status  string `json:"status"`
		Targets []struct {
			State string `json:"state"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != "settled" || len(report.Targets) != 1 || report.Targets[0].State != "closed" || reads != 6 {
		t.Fatal(report, reads, err, out.String())
	}
	records, err := waitregistry.List(home, waitregistry.Options{})
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
}

func TestWaitRootPreservesProjectsRootFlagRefusal(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"wait", "pr", "acme/app#12"}, {"wait", "list"}} {
		command := newRootCmd()
		command.SetArgs(append(args, "--projects-root", t.TempDir()))
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "--projects-root is not supported") || out.Len() != 0 {
			t.Fatal(err, out.String(), stderr.String())
		}
	}
}
