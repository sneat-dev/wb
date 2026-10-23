package orchestrate

import (
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestTemporaryRemoteAllocationPreservesBoundedCollisionProtocol(t *testing.T) {
	t.Parallel()
	configFailure := errors.New("owned config failure")
	for _, row := range []struct {
		name       string
		collisions int
		result     runner.Result
		err        error
		wantError  string
	}{
		{"available first", 0, runner.Result{ExitCode: 1}, errors.New("exit status 1"), ""},
		{"one collision then available", 1, runner.Result{ExitCode: 1}, errors.New("exit status 1"), ""},
		{"three collisions exhausted", 3, runner.Result{}, nil, "could not allocate an unused temporary Git remote name"},
		{"combined diagnostic takes precedence", 0, runner.Result{ExitCode: 2, CombinedOutput: "combined config fault\n", Stdout: "ignored fallback", Stderr: "ignored stderr"}, configFailure, "check temporary Git remote name: owned config failure: combined config fault"},
		{"split diagnostic fallback", 0, runner.Result{ExitCode: 2, Stdout: "split ", Stderr: "config fault\n"}, configFailure, "check temporary Git remote name: owned config failure: split config fault"},
		{"exit one with diagnostic is not absence", 0, runner.Result{ExitCode: 1, Stderr: "config refusal"}, configFailure, "check temporary Git remote name: owned config failure: config refusal"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := runnertest.New(t)
			names := []string{}
			match := func(call runnertest.Call) bool {
				name := assertTemporaryRemoteConfigQuery(t, call, dir)
				names = append(names, name)
				return true
			}
			for range row.collisions {
				fake.Expect(match, runner.Result{CombinedOutput: "existing remote configuration"}, nil)
			}
			wantCalls := row.collisions
			if row.collisions < 3 {
				fake.Expect(match, row.result, row.err)
				wantCalls++
			}
			name, err := unusedTemporaryGitRemoteName(t.Context(), fake, dir)
			if row.wantError == "" {
				if err != nil || name != names[len(names)-1] {
					t.Fatalf("available name = %q, %v", name, err)
				}
			} else if name != "" || err == nil || err.Error() != row.wantError {
				t.Fatalf("allocation refusal = %q, %v", name, err)
			}
			if len(fake.Calls()) != wantCalls || len(names) != wantCalls {
				t.Fatalf("allocation calls = %d, want %d", len(fake.Calls()), wantCalls)
			}
		})
	}
}

func TestTemporaryRemoteAllocationRefusalPreventsDeletePush(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"config failure", "collision exhaustion"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := runnertest.New(t)
			calls := 1
			want := "check temporary Git remote name: owned config failure: config fault"
			if kind == "collision exhaustion" {
				calls = 3
				want = "could not allocate an unused temporary Git remote name"
			}
			for range calls {
				result := runner.Result{ExitCode: 2, CombinedOutput: "config fault"}
				injected := errors.New("owned config failure")
				if kind == "collision exhaustion" {
					result = runner.Result{CombinedOutput: "existing config"}
					injected = nil
				}
				fake.Expect(func(call runnertest.Call) bool { assertTemporaryRemoteConfigQuery(t, call, dir); return true }, result, injected)
			}
			output, err := runGitPushDeleteWithLease(t.Context(), fake, dir, "https://user:secret@example.test/acme/app.git", "refs/heads/candidate", "exact-lease-sha")
			if output != "" || err == nil || err.Error() != want || strings.Contains(err.Error(), "secret") {
				t.Fatalf("delete refusal = %q, %v", output, err)
			}
			if len(fake.Calls()) != calls {
				t.Fatalf("refusal emitted later command: %+v", fake.Calls())
			}
		})
	}
}

func assertTemporaryRemoteConfigQuery(t *testing.T, call runnertest.Call, dir string) string {
	t.Helper()
	if call.Op != "RunOpts" || call.Dir != dir || call.Name != "git" || len(call.Args) != 3 || call.Args[0] != "config" || call.Args[1] != "--get-regexp" || !call.Opts.CaptureCombined || !reflect.DeepEqual(call.Opts.Env, console.Env()) {
		t.Fatalf("allocation command changed: %+v", call)
	}
	query := call.Args[2]
	name := strings.TrimSuffix(strings.TrimPrefix(query, "^remote\\."), "\\.")
	if query != "^remote\\."+name+"\\." || !regexp.MustCompile(`^wb-landing-[A-Z2-7]{26,}$`).MatchString(name) {
		t.Fatalf("unsafe/non-cryptographic allocation format: %q", query)
	}
	return name
}

func TestTemporaryRemoteDeletionRedactsSplitDiagnosticsWithExactLease(t *testing.T) {
	t.Parallel()
	const remoteURL = "https://user:secret@example.test/acme/app.git"
	const remoteRef = "refs/heads/candidate"
	const lease = "exact-owned-lease"
	dir := t.TempDir()
	baseEnv := console.Env()
	fake := runnertest.New(t)
	allocated := ""
	fake.Expect(func(call runnertest.Call) bool {
		allocated = assertTemporaryRemoteConfigQuery(t, call, dir)
		return true
	}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	fake.Expect(func(call runnertest.Call) bool {
		if call.Op != "RunOpts" || call.Dir != dir || call.Name != "git" || !call.Opts.CaptureCombined || !reflect.DeepEqual(call.Args, []string{"push", "--force-with-lease=" + remoteRef + ":" + lease, allocated, ":" + remoteRef}) {
			t.Fatalf("lease push changed: %+v", call)
		}
		if strings.Contains(strings.Join(call.Argv(), " "), remoteURL) {
			t.Fatal("credential URL exposed in argv")
		}
		env := call.Opts.Env
		if len(env) != len(baseEnv)+3 || !reflect.DeepEqual(env[:len(baseEnv)], baseEnv) {
			t.Fatal("push replaced inherited child environment")
		}
		count, err := strconv.Atoi(strings.TrimPrefix(env[len(baseEnv)], "GIT_CONFIG_COUNT="))
		if err != nil || count < 1 || env[len(baseEnv)+1] != "GIT_CONFIG_KEY_"+strconv.Itoa(count-1)+"=remote."+allocated+".url" || env[len(baseEnv)+2] != "GIT_CONFIG_VALUE_"+strconv.Itoa(count-1)+"="+remoteURL {
			t.Fatal("URL was not scoped to the isolated child remote")
		}
		return true
	}, runner.Result{Stdout: "stdout " + remoteURL + "\n", Stderr: "stderr " + remoteURL + "\n"}, errors.New("push refusal "+remoteURL))
	output, err := runGitPushDeleteWithLease(t.Context(), fake, dir, remoteURL, remoteRef, lease)
	if output != "" || err == nil || strings.Contains(err.Error(), remoteURL) || strings.Contains(err.Error(), "secret") || strings.Count(err.Error(), "<redacted-origin-push-url>") != 3 || !strings.Contains(err.Error(), "stdout") || !strings.Contains(err.Error(), "stderr") {
		t.Fatalf("split-stream refusal = %q, %v", output, err)
	}
	if len(fake.Calls()) != 2 || !reflect.DeepEqual(console.Env(), baseEnv) {
		t.Fatal("push refusal mutated process environment or emitted later commands")
	}
}
