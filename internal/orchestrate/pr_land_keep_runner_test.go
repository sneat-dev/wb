package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestRunGitPushDeleteWithLeaseKeepsCredentialsOutOfArgvAndErrors(t *testing.T) {
	t.Parallel()
	const remoteURL = "https://user:secret@example.test/acme/app.git"
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		argv := call.Argv()
		return len(argv) == 4 && argv[0] == "git" && argv[1] == "config" && argv[2] == "--get-regexp" &&
			strings.HasPrefix(argv[3], "^remote\\.wb-landing-")
	}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	fake.Expect(func(call runnertest.Call) bool {
		argv := call.Argv()
		return len(argv) == 5 && argv[0] == "git" && argv[1] == "push" &&
			argv[2] == "--force-with-lease=refs/heads/candidate:abc123" &&
			strings.HasPrefix(argv[3], "wb-landing-") && argv[4] == ":refs/heads/candidate"
	}, runner.Result{CombinedOutput: "fatal: unable to access " + remoteURL + "\n"}, errors.New("exit status 1: "+remoteURL))

	_, err := runGitPushDeleteWithLease(context.Background(), fake, t.TempDir(), remoteURL,
		"refs/heads/candidate", "abc123")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "<redacted-origin-push-url>") {
		t.Fatalf("redacted push error = %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("runner calls = %+v, want config check and push", calls)
	}
	for _, call := range calls {
		if call.Op != "RunOpts" || !call.Opts.CaptureCombined || strings.Contains(strings.Join(call.Argv(), " "), "secret") {
			t.Fatalf("runner call = %+v", call)
		}
	}
	pushEnv := strings.Join(calls[1].Opts.Env, "\x00")
	if !strings.Contains(pushEnv, "GIT_CONFIG_VALUE_") || !strings.Contains(pushEnv, remoteURL) ||
		!strings.Contains(pushEnv, "remote."+calls[1].Args[2]+".url") {
		t.Fatalf("push environment lacks isolated remote configuration")
	}
}

func TestRunGitPushDeleteWithLeaseAcceptsEmptyRemoteURLWithoutRedaction(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		return strings.HasPrefix(strings.Join(call.Argv(), " "), "git config --get-regexp ^remote\\.wb-landing-")
	}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	fake.Expect(func(call runnertest.Call) bool {
		return len(call.Argv()) == 5 && call.Argv()[1] == "push"
	}, runner.Result{CombinedOutput: "deleted branch\n"}, nil)
	got, err := runGitPushDeleteWithLease(context.Background(), fake, t.TempDir(), "", "refs/heads/candidate", "abc123")
	if err != nil || got != "deleted branch" {
		t.Fatalf("empty-URL push = %q, %v", got, err)
	}
}

func TestUnusedTemporaryGitRemoteNameReportsConfigFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		return strings.HasPrefix(strings.Join(call.Argv(), " "), "git config --get-regexp ^remote\\.wb-landing-")
	}, runner.Result{ExitCode: 2, CombinedOutput: "invalid config"}, errors.New("git config failed"))
	name, err := unusedTemporaryGitRemoteName(context.Background(), fake, t.TempDir())
	if name != "" || err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("temporary remote = %q, %v", name, err)
	}
}
