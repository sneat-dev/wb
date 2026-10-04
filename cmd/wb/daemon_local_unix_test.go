//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestDaemonOperationCLI_SubmitThenWait(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-daemon-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	t.Chdir(root)
	t.Setenv("WB_DAEMON_CLI_HELPER", "1")
	projectsRoot := root

	deps := daemonTestDependencies(t, root)
	originalStart := deps.Start
	var server *http.Server
	deps.Start = func(executable string, args []string, logPath string) (int, error) {
		pid, err := originalStart(executable, args, logPath)
		if err != nil {
			return 0, err
		}
		state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Load()
		if err != nil || !found {
			return 0, fmt.Errorf("load starting lifecycle state: found=%t: %w", found, err)
		}
		listener, err := daemonruntime.ListenLocal(root)
		if err != nil {
			return 0, err
		}
		service, err := daemonTestService(t, root, "test-build", fmt.Sprint(state.Queue.Generation), func() error { return nil })
		if err != nil {
			_ = listener.Close()
			return 0, err
		}
		path, handler := daemonv1connect.NewDaemonServiceHandler(service)
		mux := http.NewServeMux()
		mux.Handle(path, daemonruntime.AuthenticatedHandler(state.OwnerToken, handler))
		server = &http.Server{Handler: mux}
		go func() { _ = server.Serve(listener) }()
		return pid, nil
	}
	t.Cleanup(func() {
		if server != nil {
			_ = server.Close()
		}
	})

	var submitOutput bytes.Buffer
	submit := newDaemonOperationSubmitCmd(&invocation{projectsRoot: projectsRoot}, deps)
	submit.SetOut(&submitOutput)
	submit.SetErr(&bytes.Buffer{})
	submit.SetArgs([]string{"--json", "--", os.Args[0], "-test.run=TestDaemonOperationCLIHelperProcess", "--", "cli"})
	if err := submit.Execute(); err != nil {
		t.Fatal(err)
	}
	var submitted daemonOperationResult
	if err := json.Unmarshal(submitOutput.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit receipt: %v\n%s", err, submitOutput.String())
	}
	if submitted.OperationID == "" || submitted.ArgsSHA256 == "" {
		t.Fatalf("submit receipt = %#v", submitted)
	}

	var waitOutput bytes.Buffer
	wait := newDaemonOperationWaitCmd(&invocation{projectsRoot: projectsRoot}, deps)
	wait.SetOut(&waitOutput)
	wait.SetErr(&bytes.Buffer{})
	wait.SetArgs([]string{"--json", "--timeout", "10s", submitted.OperationID})
	if err := wait.Execute(); err != nil {
		t.Fatal(err)
	}
	var completed daemonOperationResult
	if err := json.Unmarshal(waitOutput.Bytes(), &completed); err != nil {
		t.Fatalf("decode wait receipt: %v\n%s", err, waitOutput.String())
	}
	if completed.State != "succeeded" || !strings.Contains(completed.StdoutTail, "cli-helper:cli") {
		t.Fatalf("completed receipt = %#v", completed)
	}
}

func TestDaemonOperationCLIHelperProcess(t *testing.T) {
	if os.Getenv("WB_DAEMON_CLI_HELPER") != "1" {
		return
	}
	argument := ""
	for index, value := range os.Args {
		if value == "--" && index+1 < len(os.Args) {
			argument = os.Args[index+1]
			break
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "cli-helper:%s\n", argument)
	time.Sleep(10 * time.Millisecond)
	os.Exit(0)
}

// AC: a-leftover-daemon-cannot-be-silently-doubled
//
// An accepting socket at a retired runtime path is enough to refuse a start:
// the leftover daemon does not have to be healthy, and nothing under that path
// may be disturbed by WB looking.
