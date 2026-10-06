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

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

// cwWtDaemonOpFixture starts an in-process authenticated daemon for the
// duration of the test, so the operation RPC clients can really connect.
func cwWtDaemonOpFixture(t *testing.T) (root string, deps daemonDependencies) {
	t.Helper()
	root = cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	t.Setenv("WB_CWWT_DAEMON_HELPER", "1")

	deps = daemonTestDependencies(t, root)
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
		operationsDirectory, err := daemon.OperationsDir(root)
		if err != nil {
			_ = listener.Close()
			return 0, err
		}
		service, err := daemon.NewService(root, operationsDirectory, "cwWt-build", fmt.Sprint(state.Queue.Generation), func() error { return nil })
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
	return root, deps
}

func cwWtDaemonOpHelperArgv() []string {
	return []string{os.Args[0], "-test.run=TestCwWtDaemonOperationHelperProcess", "--", "cwWt"}
}

func TestCwWtDaemonOperationHelperProcess(t *testing.T) {
	if os.Getenv("WB_CWWT_DAEMON_HELPER") != "1" {
		return
	}
	argument := ""
	for index, value := range os.Args {
		if value == "--" && index+1 < len(os.Args) {
			argument = os.Args[index+1]
			break
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "cwWt-helper:%s\n", argument)
	os.Exit(0)
}

func TestCwWtDaemonOperationSubmitGetWaitCancel(t *testing.T) {
	root, deps := cwWtDaemonOpFixture(t)

	var submitOutput bytes.Buffer
	submit := daemonCommandForTest("operation submit", &invocation{projectsRoot: root}, deps)
	submit.SilenceUsage, submit.SilenceErrors = true, true
	submit.SetOut(&submitOutput)
	submit.SetErr(&bytes.Buffer{})
	submit.SetArgs(append([]string{"--json", "--"}, cwWtDaemonOpHelperArgv()...))
	if err := submit.Execute(); err != nil {
		t.Fatalf("operation submit: %v", err)
	}
	var submitted daemonOperationResult
	if err := json.Unmarshal(submitOutput.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit receipt: %v\n%s", err, submitOutput.String())
	}
	if submitted.OperationID == "" || submitted.ArgsSHA256 == "" || submitted.CommandKind == "" {
		t.Fatalf("submit receipt = %#v", submitted)
	}

	var getOutput bytes.Buffer
	get := daemonCommandForTest("operation get", &invocation{projectsRoot: root}, deps)
	get.SilenceUsage, get.SilenceErrors = true, true
	get.SetOut(&getOutput)
	get.SetErr(&bytes.Buffer{})
	get.SetArgs([]string{"--json", submitted.OperationID})
	if err := get.Execute(); err != nil {
		t.Fatalf("operation get: %v", err)
	}
	var fetched daemonOperationResult
	if err := json.Unmarshal(getOutput.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get receipt: %v\n%s", err, getOutput.String())
	}
	if fetched.OperationID != submitted.OperationID {
		t.Fatalf("get receipt = %#v, want %s", fetched, submitted.OperationID)
	}

	// Text rendering of the same receipt.
	getOutput.Reset()
	getText := daemonCommandForTest("operation get", &invocation{projectsRoot: root}, deps)
	getText.SilenceUsage, getText.SilenceErrors = true, true
	getText.SetOut(&getOutput)
	getText.SetErr(&bytes.Buffer{})
	getText.SetArgs([]string{submitted.OperationID})
	if err := getText.Execute(); err != nil {
		t.Fatalf("operation get text: %v", err)
	}
	if !strings.Contains(getOutput.String(), "operation "+submitted.OperationID+": state=") {
		t.Fatalf("get text = %q", getOutput.String())
	}

	// Wait for the terminal receipt, writing progress to a file.
	progressFile := filepath.Join(t.TempDir(), "progress.log")
	var waitOutput bytes.Buffer
	wait := daemonCommandForTest("operation wait", &invocation{projectsRoot: root}, deps)
	wait.SilenceUsage, wait.SilenceErrors = true, true
	wait.SetOut(&waitOutput)
	wait.SetErr(&bytes.Buffer{})
	wait.SetArgs([]string{"--json", "--timeout", "30s", "--progress-file", progressFile, submitted.OperationID})
	if err := wait.Execute(); err != nil {
		t.Fatalf("operation wait: %v", err)
	}
	var completed daemonOperationResult
	if err := json.Unmarshal(waitOutput.Bytes(), &completed); err != nil {
		t.Fatalf("decode wait receipt: %v\n%s", err, waitOutput.String())
	}
	if completed.State != "succeeded" {
		t.Fatalf("wait receipt = %#v", completed)
	}
	if !strings.Contains(completed.StdoutTail, "cwWt-helper:cwWt") {
		t.Fatalf("wait receipt stdout tail = %q", completed.StdoutTail)
	}
	if _, err := os.Stat(progressFile); err != nil {
		t.Fatalf("progress file was not created: %v", err)
	}

	// --after-cursor waits for a receipt newer than the given cursor.
	var cursorOutput bytes.Buffer
	cursorWait := daemonCommandForTest("operation wait", &invocation{projectsRoot: root}, deps)
	cursorWait.SilenceUsage, cursorWait.SilenceErrors = true, true
	cursorWait.SetOut(&cursorOutput)
	cursorWait.SetErr(&bytes.Buffer{})
	cursorWait.SetArgs([]string{"--json", "--timeout", "5s", "--after-cursor", "ancient", submitted.OperationID})
	if err := cursorWait.Execute(); err != nil {
		t.Fatalf("operation wait --after-cursor: %v", err)
	}

	// Cancel is a valid request for a terminal operation too.
	var cancelOutput bytes.Buffer
	cancel := daemonCommandForTest("operation cancel", &invocation{projectsRoot: root}, deps)
	cancel.SilenceUsage, cancel.SilenceErrors = true, true
	cancel.SetOut(&cancelOutput)
	cancel.SetErr(&bytes.Buffer{})
	cancel.SetArgs([]string{"--json", submitted.OperationID})
	if err := cancel.Execute(); err != nil {
		t.Fatalf("operation cancel: %v", err)
	}
	if cancelOutput.Len() == 0 {
		t.Fatal("operation cancel wrote nothing")
	}
}

func TestCwWtSubmitWorkerOperationInProcess(t *testing.T) {
	root, deps := cwWtDaemonOpFixture(t)
	var out, errOut bytes.Buffer
	command := newRunCmdWithDaemonDependencies(&invocation{projectsRoot: root}, deps)
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(append([]string{"--async", "--worker", "worker-cwwt", "--idempotency-key", "cwWt-key", "--"}, cwWtDaemonOpHelperArgv()...))
	if err := command.Execute(); err != nil {
		t.Fatalf("submitWorkerOperation: %v (stderr=%s)", err, errOut.String())
	}
	var result daemonOperationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode worker submit receipt: %v\n%s", err, out.String())
	}
	if result.OperationID == "" {
		t.Fatalf("worker submit receipt = %#v", result)
	}
	if result.IdempotencyKey != "cwWt-key" {
		t.Fatalf("idempotency key = %q", result.IdempotencyKey)
	}
	_ = time.Now()
}

// TestCwWtRunAsyncCLIWiresIntoSubmitWorkerOperation preserves the original
// async journey through the actual command factory and authenticated transport.
func TestCwWtRunAsyncCLIWiresIntoSubmitWorkerOperation(t *testing.T) {
	root, deps := cwWtDaemonOpFixture(t)
	command := newRunCmdWithDaemonDependencies(&invocation{projectsRoot: root}, deps)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetContext(context.Background())
	args := append([]string{"--async", "--worker", "worker-cwwt-cli", "--idempotency-key", "cwWt-cli-key", "--"}, cwWtDaemonOpHelperArgv()...)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("run --async: %v (stderr=%s)", err, errOut.String())
	}
	var result daemonOperationResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode async run receipt: %v\n%s", err, out.String())
	}
	if result.OperationID == "" || result.IdempotencyKey != "cwWt-cli-key" {
		t.Fatalf("async run receipt = %#v", result)
	}
}
