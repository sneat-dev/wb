//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestE2ESecureStagePathProtocolKeepsNativeAuthorityAndVisibleDiagnostics(t *testing.T) {
	const childKey = "WB_TEST_STAGE_PATH_PROTOCOL_CHILD"
	const stageKey = "WB_TEST_STAGE_PATH_PROTOCOL_DIRECTORY"
	const executableKey = "WB_TEST_STAGE_PATH_PROTOCOL_EXECUTABLE"
	const diagnostic = "controlled path-protocol diagnostic stderr"
	if os.Getenv(childKey) == "1" {
		// This isolated test process observes the actual parent stderr forwarding;
		// its child still derives stdout through the real inherited-FD Getwd helper.
		descriptor, err := os.Open(os.Getenv(stageKey))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = descriptor.Close() }()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		got, err := runSecureStageHelperWithExecutable(ctx, descriptor, func() (string, error) { return os.Getenv(executableKey), nil }, secureStagePathArgument)
		if err != nil || string(got) != os.Getenv(stageKey)+"\n" {
			t.Fatalf("native path stdout = %q, %v", got, err)
		}
		admitted, err := admitSecureDirectoryPath(got, nil)
		if err != nil || admitted != os.Getenv(stageKey) {
			t.Fatalf("native held path admission = %q, %v", admitted, err)
		}
		checkOutput, checkErr := runSecureStageHelperWithExecutable(ctx, descriptor, func() (string, error) { return os.Getenv(executableKey), nil }, secureStageCheckArgument, filepath.Dir(os.Getenv(stageKey)))
		if checkErr != nil || !strings.Contains(string(checkOutput), diagnostic) {
			t.Fatalf("nonpath native check lost CombinedOutput diagnostic = %q, %v", checkOutput, checkErr)
		}
		_, _ = fmt.Fprintf(os.Stdout, "held-path=%s\n", admitted)
		return
	}
	t.Parallel()
	_, stage, _ := wtLifeCovStage(t)
	stage, err := filepath.EvalSymlinks(stage)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	wrapper := filepath.Join(t.TempDir(), "native-path-with-diagnostic.sh")
	// The wrapper does not produce a path or change cwd/environment/descriptors.
	// It execs the actual covered test binary with every original argument intact.
	body := "#!/bin/sh\nprintf '%s\\n' " + quote(diagnostic) + " >&2\nexec " + quote(executable) + " \"$@\"\n"
	if err := testenv.WriteExecutableFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	childArgs := []string{"-test.run=^TestE2ESecureStagePathProtocolKeepsNativeAuthorityAndVisibleDiagnostics$", "-test.count=1"}
	childEnv := append(console.Env(), childKey+"=1", stageKey+"="+stage, executableKey+"="+wrapper)
	if directory := wtLifeCovCoverDir(); testing.CoverMode() != "" && directory != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+directory)
		// The isolated test writes to the parent's sink, and its early-dispatch
		// native helper inherits the same runtime sink without changing its argv.
		childEnv = append(childEnv, "GOCOVERDIR="+directory)
	}
	command := exec.CommandContext(ctx, executable, childArgs...)
	command.Env = childEnv
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		t.Fatalf("isolated native path observation = %v\nstdout=%s\nstderr=%s", err, stdout, stderr.Bytes())
	}
	if !strings.Contains(string(stdout), "held-path="+stage+"\n") || strings.Contains(string(stdout), diagnostic) {
		t.Fatalf("native path stdout lost authority or included diagnostic: %q", stdout)
	}
	if strings.Count(stderr.String(), diagnostic) != 1 {
		t.Fatalf("success diagnostic was not forwarded exactly once: %q", stderr.String())
	}
}

func TestE2ESecureStagePathProtocolPreservesNativeAndControlledRefusals(t *testing.T) {
	t.Parallel()
	root, stage, descriptor := wtLifeCovStage(t)
	stage, err := filepath.EvalSymlinks(stage)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	got, err := runSecureStageHelperWithExecutable(ctx, descriptor, os.Executable, secureStagePathArgument)
	if err != nil || string(got) != stage+"\n" {
		t.Fatalf("unmodified native path = %q, %v", got, err)
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, os.Executable, secureStageCheckArgument, root)
	if err != nil || len(got) != 0 {
		t.Fatalf("native containment check = %q, %v", got, err)
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, os.Executable, secureStagePathArgument, "extra")
	if err == nil || !strings.Contains(string(got), "invalid path arguments") {
		t.Fatalf("nonpath malformed argv diagnostic = %q, %v", got, err)
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, os.Executable, secureStageCheckArgument)
	if err == nil || !strings.Contains(string(got), "invalid containment check arguments") {
		t.Fatalf("single nonpath argument diagnostic = %q, %v", got, err)
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, os.Executable)
	if err == nil || !strings.Contains(string(got), "missing operation") {
		t.Fatalf("empty nonpath argv diagnostic = %q, %v", got, err)
	}
	regular := filepath.Join(t.TempDir(), "regular-not-directory")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	badDescriptor, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = badDescriptor.Close() })
	got, err = runSecureStageHelperWithExecutable(ctx, badDescriptor, os.Executable, secureStagePathArgument)
	admitted, admissionErr := admitSecureDirectoryPath(got, err)
	if err == nil || admitted != "" || admissionErr == nil || !strings.Contains(string(got), "enter inherited stage directory") || !strings.Contains(admissionErr.Error(), "enter inherited stage directory") {
		t.Fatalf("native descriptor refusal = %q, %v; admission=%q, %v", got, err, admitted, admissionErr)
	}
	// This row tests only failure stream collection. It does not supply any
	// successful path/custody observation and cannot admit a held directory.
	fault := wtLifeCovScript(t, "controlled-protocol-failure.sh", "printf 'controlled stdout failure\\n'\nprintf 'controlled stderr failure\\n' >&2\nexit 7\n")
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, func() (string, error) { return fault, nil }, secureStagePathArgument)
	var exitErr *exec.ExitError
	admitted, admissionErr = admitSecureDirectoryPath(got, err)
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || string(got) != "controlled stdout failure\ncontrolled stderr failure\n" || admitted != "" || admissionErr == nil || !strings.Contains(admissionErr.Error(), "controlled stderr failure") {
		t.Fatalf("controlled two-stream failure = %q, %v; admission=%q, %v", got, err, admitted, admissionErr)
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, func() (string, error) { return fault, nil }, secureStageCheckArgument, root)
	if err == nil || !strings.Contains(string(got), "controlled stdout failure") || !strings.Contains(string(got), "controlled stderr failure") {
		t.Fatalf("nonpath CombinedOutput contract = %q, %v", got, err)
	}
	// The selected lookup error is an actual private read error, not a claim
	// that this host's os.Executable lookup fails during native operation.
	_, cause := os.ReadFile(filepath.Join(t.TempDir(), "absent-executable-source"))
	if !errors.Is(cause, os.ErrNotExist) {
		t.Fatal("missing executable source fixture did not fail")
	}
	got, err = runSecureStageHelperWithExecutable(ctx, descriptor, func() (string, error) { return "", cause }, secureStagePathArgument)
	if got != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "locate WB secure staging helper") {
		t.Fatalf("controlled lookup refusal preserves native cause = %q, %v", got, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	got, err = runSecureStageHelperWithExecutable(canceled, descriptor, os.Executable, secureStagePathArgument)
	if len(got) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before native launch = %q, %v", got, err)
	}
}
