package runexec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestActualChildStreamsOperationIdentityAndArbitraryStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ops := defaultExecuteOperations()
	ops.Getwd = func() (string, error) { return root, nil }
	var out, errOut bytes.Buffer
	result, err := (Executor{ops: &ops}).Run(context.Background(), ExecuteRequest{Argv: []string{"/bin/sh", "-c", "read value; printf 'out:%s:%s' \"$value\" \"$WB_OPERATION_ID\"; printf 'err:%s' \"$value\" >&2; exit 7"}, Stdin: strings.NewReader("hello\n"), Stdout: &out, Stderr: &errOut})
	if err != nil || !result.ChildFailed || result.ExitCode != 7 {
		t.Fatalf("result=%+v err=%v stderr=%s", result, err, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "out:hello:wbo-") || !strings.Contains(errOut.String(), "err:hello") {
		t.Fatalf("out=%q stderr=%q", out.String(), errOut.String())
	}
}
func TestActualChildSuccessLaunchFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"success", "launch", "cancel"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ops := defaultExecuteOperations()
			root := t.TempDir()
			ops.Getwd = func() (string, error) { return root, nil }
			var out, errOut bytes.Buffer
			argv := []string{"/bin/sh", "-c", "printf success"}
			ctx := context.Background()
			if path == "launch" {
				argv = []string{root + "/missing"}
			}
			if path == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				argv = []string{"/bin/sh", "-c", "sleep 5"}
			}
			result, err := (Executor{ops: &ops}).Run(ctx, ExecuteRequest{Argv: argv, Stdout: &out, Stderr: &errOut})
			switch path {
			case "success":
				if err != nil || result.ChildFailed || out.String() != "success" {
					t.Fatalf("result=%+v err=%v out=%q", result, err, out.String())
				}
			case "launch":
				if err == nil || !strings.Contains(err.Error(), "execute ") {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
		})
	}
	// Covers the actual exported default operation without a duplicate CLI build.
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	if _, err := Execute(ctx, ExecuteRequest{Argv: []string{"/bin/sh", "-c", "exit 0"}, Stdout: &out, Stderr: &out}); err != nil {
		t.Fatal(err)
	}
}
