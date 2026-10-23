// Package integration preserves native agent/store/SSH guarantees through actual family composition.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/cmdagent"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)
const baseConfig = `
agents:
  profiles:
    cheap:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
`
const exampleRemoteConfig = `
session_move:
  targets:
    hetzner-vm1:
      default_courier: ssh
      ssh:
        host: 178.104.41.143
        user: ai
        wb_path: /home/ai/go/bin/wb
`

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }

type fixture struct {
	root, home, configPath, sshPath string
	service                         *agentrun.Service
	deps                            agentrun.Dependencies
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{root: root, home: filepath.Join(root, ".wb"), configPath: filepath.Join(root, "wb.yaml")}
	f.writeConfig(t, baseConfig+"  providers:\n    deepseek:\n      credential_env: PATH\n")
	deps := agentrun.DefaultDependencies()
	deps.ConfigPath = func() string { return f.configPath }
	deps.BeforeCreate = func(string, []string) error { return errors.New("no checkout expected") }
	deps.AfterCreate = func(string, io.Writer, string, []worktrees.CreateResult) {}
	f.deps = deps
	f.service = agentrun.New(deps)
	return f
}
func (f *fixture) writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(f.configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) remoteConfig(t *testing.T, body string) string {
	t.Helper()
	f.writeConfig(t, body)
	return f.root
}

func (f *fixture) command() *cobra.Command {
	deps := cmdagent.Dependencies{Operations: f.service.Operations(), ResolveRemote: func(machine string) (agents.RemoteTarget, error) {
		return agents.ResolveRemoteTarget(f.configPath, machine)
	}, CallRemote: func(ctx context.Context, target agents.RemoteTarget, request agents.RemoteRequest) (agents.RemoteResponse, error) {
		rd := agents.DefaultRemoteDeps()
		rd.LookPath = func(string) (string, error) {
			if f.sshPath == "" {
				return "", errors.New("no ssh fixture")
			}
			if _, err := os.Stat(f.sshPath); err != nil {
				return "", err
			}
			return f.sshPath, nil
		}
		return agents.CallRemote(ctx, target, request, rd)
	}, ReadTaskFile: os.ReadFile, Discovery: func(*cobra.Command, string) {}}
	cmd := &cobra.Command{Use: "wb", SilenceUsage: true, SilenceErrors: true}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &codedError{2, err.Error()} })
	cmd.AddCommand(cmdagent.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: f.root} }, ExitError: func(code int, msg string) error { return &codedError{code, msg} }}, deps))
	return cmd
}
func (f *fixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	code := f.runWithStdin(args, strings.NewReader(""), &out, &errout)
	return code, out.String(), errout.String()
}
func (f *fixture) runWithStdin(args []string, in io.Reader, out, errout io.Writer) int {
	cmd := f.command()
	cmd.SetArgs(args)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errout)
	err := cmd.Execute()
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(errout, err)
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}
func (f *fixture) remote(t *testing.T, r agents.RemoteRequest) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := agentrun.Serve(t.Context(), f.service.Operations(), f.root, bytes.NewReader(raw), &out, &errout)
	return code, out.String(), errout.String()
}
func (f *fixture) ssh(t *testing.T, response string) string {
	t.Helper()
	directory := t.TempDir()
	requestPath := filepath.Join(directory, "request.json")
	responsePath := filepath.Join(directory, "response.json")
	if err := os.WriteFile(responsePath, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	f.sshPath = filepath.Join(directory, "ssh")
	script := "#!/bin/sh\ncat > " + requestPath + "\ncat " + responsePath + "\n"
	if err := testenv.WriteExecutableFile(f.sshPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return requestPath
}

func seedAgentRun(t *testing.T, home string, mutate func(*agents.Record)) agents.Record {
	t.Helper()
	store := agents.NewStore(home)
	id, err := agents.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := agents.Record{
		AgentID: id, State: agents.StateRunning, RequestedProfile: "cheap",
		Resolved: agents.Resolved{
			Profile: "cheap", Harness: agents.HarnessCodex, Provider: "deepseek",
			Model: "deepseek-flash", Reasoning: "high",
			Routing: agents.Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: agents.WireAPIResponses},
		},
		Task: "the private bounded task", TaskSummary: "the private bounded task",
		Repository: "acme/app", WorktreeMode: agents.ModeNew, Worktree: "task-one",
		WorktreeDir: "/fleet/.worktrees/task-one", Branch: "task-one", Base: "main", BaseSHA: "abc123",
		StartedAt: time.Now().UTC(), LogPath: store.LogPath(id),
	}
	if mutate != nil {
		mutate(&record)
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func decodeRemoteResponseForTest(t *testing.T, raw string) agents.RemoteResponse {
	t.Helper()
	var response agents.RemoteResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("response is not a remote protocol document: %v\n%s", err, raw)
	}
	return response
}

func encodeRemoteResponseForTest(t *testing.T, response agents.RemoteResponse) string {
	t.Helper()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readRemoteRequestForTest(t *testing.T, path string) agents.RemoteRequest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake ssh recorded no request: %v", err)
	}
	var request agents.RemoteRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("the request on stdin is not a remote request document: %v\n%s", err, raw)
	}
	return request
}
