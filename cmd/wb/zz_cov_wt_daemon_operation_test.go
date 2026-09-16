//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

// cwWtDaemonOpFixture starts an in-process authenticated daemon for the
// duration of the test, so the operation RPC clients can really connect.
func cwWtDaemonOpFixture(t *testing.T) (root string, deps daemonDependencies) {
	t.Helper()
	root = cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	t.Setenv("WB_CWWT_DAEMON_HELPER", "1")
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	deps = daemonTestDependencies(t, root)
	originalStart := deps.start
	var server *http.Server
	deps.start = func(executable string, args []string, logPath string) (int, error) {
		pid, err := originalStart(executable, args, logPath)
		if err != nil {
			return 0, err
		}
		state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Load()
		if err != nil || !found {
			return 0, fmt.Errorf("load starting lifecycle state: found=%t: %w", found, err)
		}
		listener, err := listenDaemonLocal(root)
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
	_, deps := cwWtDaemonOpFixture(t)

	var submitOutput bytes.Buffer
	submit := newDaemonOperationSubmitCmd(deps)
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
	get := newDaemonOperationGetCmd(deps)
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
	getText := newDaemonOperationGetCmd(deps)
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
	wait := newDaemonOperationWaitCmd(deps)
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
	cursorWait := newDaemonOperationWaitCmd(deps)
	cursorWait.SilenceUsage, cursorWait.SilenceErrors = true, true
	cursorWait.SetOut(&cursorOutput)
	cursorWait.SetErr(&bytes.Buffer{})
	cursorWait.SetArgs([]string{"--json", "--timeout", "5s", "--after-cursor", "ancient", submitted.OperationID})
	if err := cursorWait.Execute(); err != nil {
		t.Fatalf("operation wait --after-cursor: %v", err)
	}

	// Cancel is a valid request for a terminal operation too.
	var cancelOutput bytes.Buffer
	cancel := newDaemonOperationCancelCmd(deps)
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

func TestCwWtDaemonOperationUsageErrors(t *testing.T) {
	_, deps := cwWtDaemonOpFixture(t)

	builders := map[string]func() *cobra.Command{
		"submit": func() *cobra.Command { return newDaemonOperationSubmitCmd(deps) },
		"get":    func() *cobra.Command { return newDaemonOperationGetCmd(deps) },
		"wait":   func() *cobra.Command { return newDaemonOperationWaitCmd(deps) },
		"cancel": func() *cobra.Command { return newDaemonOperationCancelCmd(deps) },
	}
	arguments := map[string][]string{
		"submit": {"--", "true"},
		"get":    {"op-1"},
		"wait":   {"op-1"},
		"cancel": {"op-1"},
	}
	for name, build := range builders {
		args := append([]string{"--format", "yaml"}, arguments[name]...)
		command := build()
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("operation %s --format yaml returned nil, want a usage error", name)
		}

		args = append([]string{"--json", "--format", "yaml"}, arguments[name]...)
		command = build()
		command.SilenceUsage, command.SilenceErrors = true, true
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Errorf("operation %s --json with a conflicting --format returned nil", name)
		}
	}

	// Submit without a command after -- is a usage error.
	submit := newDaemonOperationSubmitCmd(deps)
	submit.SilenceUsage, submit.SilenceErrors = true, true
	submit.SetOut(&bytes.Buffer{})
	submit.SetErr(&bytes.Buffer{})
	submit.SetArgs([]string{"--"})
	if err := submit.Execute(); err == nil || !strings.Contains(err.Error(), "command is required after --") {
		t.Fatalf("submit without a command = %v", err)
	}
	submitNoDash := newDaemonOperationSubmitCmd(deps)
	submitNoDash.SilenceUsage, submitNoDash.SilenceErrors = true, true
	submitNoDash.SetOut(&bytes.Buffer{})
	submitNoDash.SetErr(&bytes.Buffer{})
	submitNoDash.SetArgs([]string{"true"})
	if err := submitNoDash.Execute(); err == nil || !strings.Contains(err.Error(), "command is required after --") {
		t.Fatalf("submit without -- = %v", err)
	}

	// A denied raw-execution policy refuses before any RPC.
	denied := deps
	denied.rawPolicy = func(string) (bool, string, error) { return false, "/tmp/policy.json", nil }
	deniedSubmit := newDaemonOperationSubmitCmd(denied)
	deniedSubmit.SilenceUsage, deniedSubmit.SilenceErrors = true, true
	deniedSubmit.SetOut(&bytes.Buffer{})
	deniedSubmit.SetErr(&bytes.Buffer{})
	deniedSubmit.SetArgs(append([]string{"--"}, cwWtDaemonOpHelperArgv()...))
	if err := deniedSubmit.Execute(); err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") {
		t.Fatalf("denied raw execution = %v", err)
	}
}

func TestCwWtDaemonOperationProgressWriter(t *testing.T) {
	var stderr bytes.Buffer
	writer, closeWriter, err := daemonOperationProgressWriter(&stderr, false, "")
	if err != nil || writer == nil {
		t.Fatalf("disabled progress = (%v, %v)", writer, err)
	}
	closeWriter()

	if _, _, err := daemonOperationProgressWriter(&stderr, false, "/tmp/x"); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("disabled progress with a file = %v", err)
	}

	writer, closeWriter, err = daemonOperationProgressWriter(&stderr, true, "")
	if err != nil || writer != &stderr {
		t.Fatalf("stderr progress = (%v, %v)", writer, err)
	}
	closeWriter()

	path := filepath.Join(t.TempDir(), "progress.log")
	writer, closeWriter, err = daemonOperationProgressWriter(&stderr, true, path)
	if err != nil {
		t.Fatalf("file progress: %v", err)
	}
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	closeWriter()
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "hello\n" {
		t.Fatalf("progress file = (%q, %v)", contents, err)
	}

	// An unopenable destination is an error, never a silent fallback.
	if _, _, err := daemonOperationProgressWriter(&stderr, true, filepath.Join(t.TempDir(), "missing", "progress.log")); err == nil || !strings.Contains(err.Error(), "open human progress file") {
		t.Fatalf("unopenable progress file = %v", err)
	}
}

func TestCwWtDaemonOperationProgressFailurePropagation(t *testing.T) {
	operation := &daemonv1.Operation{
		OperationId: "wbo-cwWt", State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED,
		Cursor: "c", CpuUnits: 1, FinishedUnixMilli: 1,
		TargetWorkerId: "worker-1", StdoutTail: []byte("out\n"), StderrTail: []byte("err\n"),
	}
	for allow := 0; allow < 5; allow++ {
		if err := writeDaemonOperation(&cwWtFailWriter{Allow: allow}, "text", operation); err == nil {
			t.Fatalf("writeDaemonOperation with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtRequireDaemonRawExecutionPolicy(t *testing.T) {
	root := t.TempDir()
	if err := requireDaemonRawExecutionPolicy(daemonDependencies{
		rawPolicy: func(string) (bool, string, error) { return true, "/tmp/policy", nil },
	}, root); err != nil {
		t.Fatalf("allowed policy: %v", err)
	}
	err := requireDaemonRawExecutionPolicy(daemonDependencies{
		rawPolicy: func(string) (bool, string, error) { return false, "/tmp/policy", nil },
	}, root)
	if err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") || !strings.Contains(err.Error(), "/tmp/policy") {
		t.Fatalf("denied policy = %v", err)
	}
	err = requireDaemonRawExecutionPolicy(daemonDependencies{
		rawPolicy: func(string) (bool, string, error) { return false, "", errors.New("cwWt: policy unreadable") },
	}, root)
	if err == nil || !strings.Contains(err.Error(), "load daemon raw-execution policy") {
		t.Fatalf("policy load error = %v", err)
	}

	// A nil policy falls back to the default dependency's loader.
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	if err := requireDaemonRawExecutionPolicy(daemonDependencies{}, root); err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") {
		t.Fatalf("default policy = %v", err)
	}
}

func TestCwWtSubmitWorkerOperationInProcess(t *testing.T) {
	_, deps := cwWtDaemonOpFixture(t)
	var out, errOut bytes.Buffer
	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetOut(&out)
	command.SetErr(&errOut)
	if err := submitWorkerOperation(command, deps, "worker-cwwt", "cwWt-key", cwWtDaemonOpHelperArgv()); err != nil {
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
