package agents

import (
	"context"
	"strings"
	"testing"
)

// TestContainerTransportReservedCredentialNamesRefusesOwnedVariables proves the
// transport, worker environment, fixed machine names and every reserved prefix
// can never carry a provider credential, while an ordinary name still can.
func TestContainerTransportReservedCredentialNamesRefusesOwnedVariables(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"HOME",
		"DOCKER_HOST",
		"GOCACHE",
		"USER",
		"TZ",
		"DOCKER_INSECURE_NO_VERIFY",
		"LD_PRELOAD",
		"NODE_OPTIONS",
		"NX_DAEMON",
		"SSL_CERT_FILE",
		"HTTPS_PROXY",
		"https_proxy",
	} {
		if !reservedContainerWorkerEnvName(name) {
			t.Errorf("reservedContainerWorkerEnvName(%q) = false, want a reserved name", name)
		}
	}
	for _, name := range []string{"WB_PROVIDER_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if reservedContainerWorkerEnvName(name) {
			t.Errorf("reservedContainerWorkerEnvName(%q) = true, want an available name", name)
		}
	}
}

// TestContainerTransportCredentialNameShapeRefusesAmbiguousNames proves only a
// conventional uppercase environment name is accepted.
func TestContainerTransportCredentialNameShapeRefusesAmbiguousNames(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":                       false,
		strings.Repeat("A", 129): false,
		"1LEADING_DIGIT":         false,
		"lower_case":             false,
		"WITH-DASH":              false,
		"A B":                    false,
		"CORRECT_ENV_NAME_1":     true,
		strings.Repeat("A", 128): true,
	}
	for name, want := range cases {
		if got := validContainerCredentialName(name); got != want {
			t.Errorf("validContainerCredentialName(%q) = %t, want %t", name, got, want)
		}
	}
}

// TestContainerWorkerCommandRefusesUnsafeCredentials proves every credential
// shape that could smuggle a transport name or an oversized value is refused
// before any command is built.
func TestContainerWorkerCommandRefusesUnsafeCredentials(t *testing.T) {
	t.Parallel()
	runtime := &ContainerRuntime{}
	cases := []struct {
		name       string
		credential Credential
		want       string
	}{
		{"surrounding whitespace", Credential{EnvName: " WB_KEY", Value: "secret"}, "surrounding whitespace"},
		{"reserved name", Credential{EnvName: "HOME", Value: "secret"}, "reserved for the container transport"},
		{"empty value", Credential{EnvName: "WB_KEY", Value: ""}, "has an empty value"},
		{"oversized value", Credential{EnvName: "WB_KEY", Value: strings.Repeat("x", maxContainerCredentialValueBytes+1)}, "above the"},
		{"NUL value", Credential{EnvName: "WB_KEY", Value: "sec\x00ret"}, "NUL byte"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			command, err := runtime.WorkerCommand(context.Background(), testCase.credential, []string{"/bin/true"})
			if command != nil {
				t.Fatal("a refused credential must not build a command")
			}
			if err == nil {
				t.Fatal("an unsafe credential must be refused")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// TestContainerWorkerCommandRefusesAnUnverifiedRuntime proves a runtime that is
// closed, has no container, has no worktree, has no argv or carries a NUL
// argument never yields a command.
func TestContainerWorkerCommandRefusesAnUnverifiedRuntime(t *testing.T) {
	t.Parallel()
	ready := func() *ContainerRuntime {
		return &ContainerRuntime{
			containerID: testContainerID,
			ownerUser:   "501:20",
			dockerPath:  "/test/docker",
			workspace:   containerWorkspacePlan{worktreeDir: "/worktree"},
		}
	}
	cases := []struct {
		name    string
		runtime func() *ContainerRuntime
		argv    []string
		want    string
	}{
		{"closed runtime", func() *ContainerRuntime { runtime := ready(); runtime.closed = true; return runtime }, []string{"/bin/true"}, "runtime is closed"},
		{"no verified container", func() *ContainerRuntime { return &ContainerRuntime{} }, []string{"/bin/true"}, "no verified container"},
		{"no workspace worktree", func() *ContainerRuntime {
			return &ContainerRuntime{containerID: testContainerID}
		}, []string{"/bin/true"}, "no workspace worktree"},
		{"empty argv", ready, nil, "non-empty worker argv"},
		{"NUL argument", ready, []string{"/bin/true", "a\x00b"}, "NUL byte"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			command, err := testCase.runtime().WorkerCommand(context.Background(), Credential{}, testCase.argv)
			if command != nil {
				t.Fatal("a refused runtime must not build a command")
			}
			if err == nil {
				t.Fatal("an unverified runtime must be refused")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// TestContainerWorkerCommandCarriesOnlyTheCredentialNameInArgv proves a valid
// command targets the verified container, keeps the fixed worker environment,
// and puts the credential value only in the Docker client environment.
func TestContainerWorkerCommandCarriesOnlyTheCredentialNameInArgv(t *testing.T) {
	t.Parallel()
	runtime := &ContainerRuntime{
		containerID:  testContainerID,
		ownerUser:    "501:20",
		dockerPath:   "/test/docker",
		transportEnv: []string{"PATH=/usr/bin:/bin"},
		workspace:    containerWorkspacePlan{worktreeDir: "/worktree"},
	}
	command, err := runtime.WorkerCommand(nil, Credential{EnvName: "WB_PROVIDER_KEY", Value: "secret"}, []string{"/bin/true", "arg"})
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	if command == nil {
		t.Fatal("a verified runtime must build a command")
	}
	if command.Path != "/test/docker" {
		t.Fatalf("command path = %q, want the verified docker tool", command.Path)
	}
	argv := strings.Join(command.Args, " ")
	if !strings.Contains(argv, testContainerID) {
		t.Fatalf("argv = %q, want the verified container ID", argv)
	}
	if strings.Contains(argv, "secret") {
		t.Fatalf("argv = %q, must not carry the credential value", argv)
	}
	if !strings.Contains(argv, "--env WB_PROVIDER_KEY") {
		t.Fatalf("argv = %q, want the credential environment name", argv)
	}
	environment := strings.Join(command.Env, " ")
	if !strings.Contains(environment, "WB_PROVIDER_KEY=secret") {
		t.Fatalf("client environment = %q, want the credential value", environment)
	}
	if !strings.Contains(environment, "PATH=/usr/bin:/bin") {
		t.Fatalf("client environment = %q, want the captured transport", environment)
	}
}

// TestPrepareWorkerHomeRefusesAnUnverifiedRuntime proves the worker-home
// preparation never runs an exec without a verified container, verified limits
// and an available command transport.
func TestPrepareWorkerHomeRefusesAnUnverifiedRuntime(t *testing.T) {
	t.Parallel()
	ready := func() *ContainerRuntime {
		return &ContainerRuntime{
			containerID: testContainerID,
			ownerUser:   "501:20",
			limits:      ContainerVerifiedLimits{MemoryBytes: 1 << 30},
			commands:    &containerCommands{command: func(context.Context, []string) ([]byte, error) { return nil, nil }},
		}
	}
	cases := []struct {
		name    string
		runtime func() *ContainerRuntime
		want    string
	}{
		{"nil runtime", func() *ContainerRuntime { return nil }, "runtime is nil"},
		{"closed runtime", func() *ContainerRuntime { runtime := ready(); runtime.closed = true; return runtime }, "runtime is closed"},
		{"no verified container", func() *ContainerRuntime { return &ContainerRuntime{} }, "no verified container"},
		{"unverified limits", func() *ContainerRuntime {
			return &ContainerRuntime{containerID: testContainerID, commands: &containerCommands{}}
		}, "limits have not been verified"},
		{"unavailable transport", func() *ContainerRuntime {
			return &ContainerRuntime{containerID: testContainerID, limits: ContainerVerifiedLimits{MemoryBytes: 1 << 30}, commands: &containerCommands{}}
		}, "command transport is unavailable"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.runtime().PrepareWorkerHome(context.Background())
			if err == nil {
				t.Fatal("an unverified runtime must not prepare a worker home")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

// TestPrepareWorkerHomeRunsWithADefaultContextWhenNoneIsSupplied proves a nil
// context becomes a bounded background context rather than reaching the exec
// with a nil context.
func TestPrepareWorkerHomeRunsWithADefaultContextWhenNoneIsSupplied(t *testing.T) {
	t.Parallel()
	var seenContext context.Context
	runtime := &ContainerRuntime{
		containerID: testContainerID,
		ownerUser:   "501:20",
		limits:      ContainerVerifiedLimits{MemoryBytes: 1 << 30},
		commands: &containerCommands{command: func(ctx context.Context, _ []string) ([]byte, error) {
			seenContext = ctx
			return nil, nil
		}},
	}
	if err := runtime.PrepareWorkerHome(nil); err != nil {
		t.Fatalf("PrepareWorkerHome: %v", err)
	}
	if seenContext == nil {
		t.Fatal("the preparation must use a non-nil context")
	}
	if _, hasDeadline := seenContext.Deadline(); !hasDeadline {
		t.Fatal("the preparation must be bounded by a deadline")
	}
}
