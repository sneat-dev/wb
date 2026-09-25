package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// prependToPATH puts directory first on PATH, ahead of the real PATH, so a
// fake "herdr" resolves before any real one while ordinary shell utilities
// (sh, cat, printf) the fake script itself relies on remain reachable —
// mirroring cmd/wb/agent_remote_test.go's fakeSSHOnPath.
func prependToPATH(t *testing.T, directory string) {
	t.Helper()
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// neutralizeAmbientHerdr is a belt-and-suspenders safety net for these two
// real-os/exec tests: this test process's own ambient environment is a
// live herdr pane (HERDR_PANE_ID etc. are genuinely set, and
// HERDR_SOCKET_PATH genuinely reaches the founder's real server). PATH
// resolution already makes the fake script win, and the fake script never
// reads these variables — but if any of that ever regressed, a child
// process must still be unable to reach the real server or address a real
// pane, session, or client socket: every ambient identity variable this
// package knows about (see ambientIdentityEnvVars in runner.go) is
// cleared or pointed at something that cannot exist.
func neutralizeAmbientHerdr(t *testing.T) {
	t.Helper()
	t.Setenv(envSocketPath, filepath.Join(t.TempDir(), "unreachable-by-construction.sock"))
	t.Setenv(envPaneID, "")
	t.Setenv(envTabID, "")
	t.Setenv(envWorkspaceID, "")
	t.Setenv(envSession, "")
	t.Setenv(envClientSocketPath, "")
}

// fakeHerdrScript is a POSIX shell script standing in for the real herdr
// binary. It records the exact argv it received (NUL-separated, so no
// argument's own content — including embedded shell metacharacters or
// spaces — can be confused with a separator) into $FAKE_HERDR_ARGV_FILE,
// then replies with the fixed contents of $FAKE_HERDR_STDOUT_FILE and the
// exit code in $FAKE_HERDR_EXIT_FILE (0 if that file is absent).
//
// This never runs against the real herdr; it is herdr for the duration of
// this one test process, resolved from a temp PATH exactly the way
// [ResolveBinary] resolves the real thing.
const fakeHerdrScript = `#!/bin/sh
set -eu
: > "$FAKE_HERDR_ARGV_FILE"
for arg in "$@"; do
  printf '%s\0' "$arg" >> "$FAKE_HERDR_ARGV_FILE"
done
cat "$FAKE_HERDR_STDOUT_FILE"
if [ -f "$FAKE_HERDR_EXIT_FILE" ]; then
  exit "$(cat "$FAKE_HERDR_EXIT_FILE")"
fi
exit 0
`

// installFakeHerdr writes fakeHerdrScript as an executable named "herdr" on
// a fresh temp directory, points PATH at that directory only, and returns
// the paths the test uses to control its canned reply and to read back the
// argv it recorded.
func installFakeHerdr(t *testing.T) (argvFile, stdoutFile, exitFile string) {
	t.Helper()
	directory := t.TempDir()
	scriptPath := filepath.Join(directory, "herdr")
	if err := testenv.WriteExecutableFile(scriptPath, []byte(fakeHerdrScript), 0o700); err != nil {
		t.Fatalf("write fake herdr script: %v", err)
	}

	argvFile = filepath.Join(directory, "argv.recorded")
	stdoutFile = filepath.Join(directory, "stdout.reply")
	exitFile = filepath.Join(directory, "exit.code")
	if err := os.WriteFile(stdoutFile, []byte(`{"id":"cli:agent:prompt","result":{}}`), 0o600); err != nil {
		t.Fatalf("seed fake herdr stdout reply: %v", err)
	}

	prependToPATH(t, directory)
	neutralizeAmbientHerdr(t)
	t.Setenv("FAKE_HERDR_ARGV_FILE", argvFile)
	t.Setenv("FAKE_HERDR_STDOUT_FILE", stdoutFile)
	t.Setenv("FAKE_HERDR_EXIT_FILE", exitFile)
	return argvFile, stdoutFile, exitFile
}

// TestFakeHERDRBinaryArgvQuotingEndToEnd drives a real os/exec call — no
// injected Runner — through ResolveBinary, NewClient and AgentPrompt
// against the fake herdr script above, then reads back exactly what argv
// the OS delivered. It proves two things at once: PATH resolution finds a
// herdr on PATH when HERDR_BIN_PATH is unset, and every argument — including
// one built to look like a shell injection attempt — arrives at the process
// boundary byte-for-byte, because exec.CommandContext never invokes a
// shell.
func TestFakeHERDRBinaryArgvQuotingEndToEnd(t *testing.T) {
	argvFile, _, _ := installFakeHerdr(t)

	client, err := NewClient(lookupFromMap(nil)) // no HERDR_BIN_PATH: forces PATH resolution
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	dangerousText := `hello "world"; $(rm -rf /) && echo pwned | tee /tmp/pwned` + "\t" + "not-a-real-tab-because-validation-should-reject-it"
	// The tab above must be rejected before any exec happens; assert that
	// first, then use a text that only contains characters ValidatePromptText
	// allows but a shell would still treat specially.
	if err := ValidatePromptText(dangerousText); err == nil {
		t.Fatal("test fixture text should contain a control character")
	}

	shellLikeButValidText := `hello "world"; $(rm -rf /) && echo pwned | tee /tmp/pwned #comment ' backtick:` + "`id`"
	target := "reviewer's pane; rm -rf ~"

	if err := client.AgentPrompt(context.Background(), target, shellLikeButValidText); err != nil {
		t.Fatalf("AgentPrompt() error = %v", err)
	}

	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	got := splitNulTerminated(recorded)
	want := []string{"agent", "prompt", target, shellLikeButValidText}
	if !equalStrings(got, want) {
		t.Fatalf("fake herdr received argv %#v, want %#v", got, want)
	}
}

// TestFakeHERDRBinaryNonZeroExit proves the same real-exec path correctly
// surfaces a herdr-reported structured error, using the JSON-error/exit-1
// contract observed live against the real (read-only) herdr in this task.
func TestFakeHERDRBinaryNonZeroExit(t *testing.T) {
	directory := t.TempDir()
	argvFile := filepath.Join(directory, "argv.recorded")
	prependToPATH(t, directory)
	neutralizeAmbientHerdr(t)
	t.Setenv("FAKE_HERDR_ARGV_FILE", argvFile)

	// herdr reports structured errors as JSON on stderr with exit status 1
	// (observed live against the real, read-only herdr in this task).
	scriptPath := filepath.Join(directory, "herdr")
	script := `#!/bin/sh
set -eu
: > "$FAKE_HERDR_ARGV_FILE"
for arg in "$@"; do
  printf '%s\0' "$arg" >> "$FAKE_HERDR_ARGV_FILE"
done
echo '{"error":{"code":"agent_not_found","message":"agent target ghost not found"}}' 1>&2
exit 1
`
	if err := testenv.WriteExecutableFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(lookupFromMap(nil))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.AgentGet(context.Background(), "ghost")
	if !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentGet() error = %v, want wrapping ErrUnknownTarget", err)
	}
}
