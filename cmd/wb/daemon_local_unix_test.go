//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestDaemonLocalTransportRequiresTokenAndProtectsSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := listenDaemonLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	info, err := os.Stat(daemonLocalAddress(root))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("socket permissions = %o", permissions)
	}
	service, err := daemon.NewService(root, "test-build", "9")
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, authenticatedDaemonHandler("expected-token", handler))
	server := &http.Server{Handler: mux}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(listener) }()

	wrong, err := newDaemonOperationClient(root, "wrong-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong-token error = %v", err)
	}
	right, err := newDaemonOperationClient(root, "expected-token")
	if err != nil {
		t.Fatal(err)
	}
	response, err := right.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.SchedulerGeneration != "9" {
		t.Fatalf("generation = %q", response.Msg.SchedulerGeneration)
	}
}

func TestDaemonOperationCLI_SubmitThenWait(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-daemon-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Chdir(root)
	t.Setenv("WB_DAEMON_CLI_HELPER", "1")
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	deps := daemonTestDependencies(t, root)
	originalStart := deps.start
	var server *http.Server
	deps.start = func(executable string, args []string, logPath string) (int, error) {
		pid, err := originalStart(executable, args, logPath)
		if err != nil {
			return 0, err
		}
		state, found, err := (daemon.Store{Path: daemonStatePath(root)}).Load()
		if err != nil || !found {
			return 0, fmt.Errorf("load starting lifecycle state: found=%t: %w", found, err)
		}
		listener, err := listenDaemonLocal(root)
		if err != nil {
			return 0, err
		}
		service, err := daemon.NewService(root, "test-build", fmt.Sprint(state.Queue.Generation))
		if err != nil {
			_ = listener.Close()
			return 0, err
		}
		path, handler := daemonv1connect.NewDaemonServiceHandler(service)
		mux := http.NewServeMux()
		mux.Handle(path, authenticatedDaemonHandler(state.OwnerToken, handler))
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
	submit := newDaemonOperationSubmitCmd(deps)
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
	wait := newDaemonOperationWaitCmd(deps)
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

func TestDaemonLocalTransportRejectsOverlongSocketPath(t *testing.T) {
	root := filepath.Join("/tmp", strings.Repeat("a", 100))
	if _, err := listenDaemonLocal(root); err == nil {
		t.Fatal("expected overlong socket path to fail")
	}
}
