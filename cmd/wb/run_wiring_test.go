//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestRunAsyncCompositionPreservesDaemonClientRefusal(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	sentinel := errors.New("another projects root owns the supervisor")
	deps.CheckOtherRoot = func(got string, replace bool) error {
		if got != root || replace {
			t.Fatalf("root=%q replace=%v", got, replace)
		}
		return sentinel
	}
	deps.RawPolicy = func(string) (bool, string, error) {
		t.Fatal("normal worker submission must not request raw administrator opt-in")
		return false, "", nil
	}
	inv := &invocation{projectsRoot: root}
	command := newRunCmdWithDaemonDependencies(inv, deps)
	command.SilenceUsage, command.SilenceErrors = true, true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"--async", "--worker", "worker-refusal", "--", "/bin/sh", "-c", "exit 0"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command.SetContext(ctx)
	err := command.Execute()
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "start local daemon") || out.Len() != 0 {
		t.Fatalf("error=%v output=%q", err, out.String())
	}
}

func TestRunAsyncCompositionPreservesAuthenticatedSubmitRefusal(t *testing.T) {
	root, deps := cwWtDaemonOpFixture(t)
	deps.RawPolicy = func(string) (bool, string, error) {
		t.Fatal("normal worker submission must not request raw administrator opt-in")
		return false, "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	execute := func(script string) (string, error) {
		inv := &invocation{projectsRoot: root}
		command := newRunCmdWithDaemonDependencies(inv, deps)
		command.SilenceUsage, command.SilenceErrors = true, true
		var out, errOut bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&errOut)
		command.SetContext(ctx)
		command.SetArgs([]string{"--async", "--worker", "worker-submit-refusal", "--idempotency-key", "root-binding-conflict", "--", "/bin/sh", "-c", script})
		err := command.Execute()
		return out.String(), err
	}
	out, err := execute("exit 0")
	if err != nil {
		t.Fatalf("first submit: %v output=%q", err, out)
	}
	var receipt daemonOperationResult
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.OperationID == "" || receipt.TargetWorkerID != "worker-submit-refusal" || receipt.IdempotencyKey != "root-binding-conflict" || receipt.ArgumentCount != 3 || receipt.ArgsSHA256 == "" {
		t.Fatalf("actual authenticated receipt=%+v", receipt)
	}
	out, err = execute("exit 1")
	if connect.CodeOf(err) != connect.CodeAlreadyExists || !strings.Contains(err.Error(), "different operation payload") || out != "" {
		t.Fatalf("refusal=%v output=%q", err, out)
	}
}
