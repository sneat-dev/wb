package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidationKeepsTheLegacyHostEnvironmentAndWorkdir(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "super-secret")
	worktree := t.TempDir()
	var command *exec.Cmd
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: worktree,
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "host", Argv: []string{"true"}}},
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			command = exec.CommandContext(ctx, "/usr/bin/true")
			return command
		},
		Now: time.Now,
	})
	if record.Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	if command == nil {
		t.Fatal("the legacy command seam was not used")
	}
	if want := WorkerEnvironment(Credential{}); !reflect.DeepEqual(command.Env, want) {
		t.Fatalf("legacy environment = %#v, want %#v", command.Env, want)
	}
	if command.Dir != worktree {
		t.Fatalf("legacy workdir = %q, want %q", command.Dir, worktree)
	}
	for _, entry := range command.Env {
		if strings.Contains(entry, "super-secret") {
			t.Fatalf("legacy validation must not carry the provider credential: %#v", command.Env)
		}
	}
}

func TestValidationPreservesTheContainedDockerClientTransport(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "super-secret")
	transportEnv := []string{
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"DOCKER_CONFIG=/private/docker-config",
		"PATH=/usr/bin:/bin",
	}
	preservedDir := t.TempDir()
	var command *exec.Cmd
	var gotArgv []string
	hostFallback := false
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "container", Argv: []string{"go", "test", "./..."}, Timeout: "1m"}},
		Transport: func(ctx context.Context, argv []string) (*exec.Cmd, error) {
			gotArgv = append([]string(nil), argv...)
			command = exec.CommandContext(ctx, "/usr/bin/true")
			command.Env = append([]string(nil), transportEnv...)
			command.Dir = preservedDir
			return command, nil
		},
		Command: func(context.Context, string, ...string) *exec.Cmd {
			hostFallback = true
			return exec.CommandContext(context.Background(), "/usr/bin/true")
		},
		Now: time.Now,
	})
	if record.Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	if hostFallback {
		t.Fatal("a contained transport must never fall back to the host command seam")
	}
	if !reflect.DeepEqual(gotArgv, []string{"go", "test", "./..."}) {
		t.Fatalf("transport argv = %#v, want the step argv alone", gotArgv)
	}
	if !reflect.DeepEqual(command.Env, transportEnv) {
		t.Fatalf("Docker client environment = %#v, want the factory's %#v", command.Env, transportEnv)
	}
	if command.Dir != preservedDir {
		t.Fatalf("Docker client workdir = %q, want the factory's %q", command.Dir, preservedDir)
	}
	for _, entry := range command.Env {
		if strings.Contains(entry, "super-secret") || strings.HasPrefix(entry, "DEEPSEEK_API_KEY=") {
			t.Fatalf("the Docker client transport must stay credential-free: %#v", command.Env)
		}
	}
}

func TestValidationContainedTransportFailureNeverFallsBackToTheHost(t *testing.T) {
	t.Parallel()
	hostFallback := false
	hostCommand := func(context.Context, string, ...string) *exec.Cmd {
		hostFallback = true
		return exec.CommandContext(context.Background(), "/usr/bin/true")
	}
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "container", Argv: []string{"true"}}},
		Transport: func(context.Context, []string) (*exec.Cmd, error) {
			return nil, errors.New("runtime unavailable")
		},
		Command: hostCommand,
		Now:     time.Now,
	})
	if record.Status != ValidationFailed || len(record.Steps) != 1 || record.Steps[0].Status != ValidationFailed {
		t.Fatalf("validation = %#v", record)
	}
	if hostFallback {
		t.Fatal("a transport failure must not fall back to the host command seam")
	}

	// A factory that returns no command and no error is a failure, not a panic.
	record = RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "container", Argv: []string{"true"}}},
		Transport: func(context.Context, []string) (*exec.Cmd, error) {
			return nil, nil
		},
		Command: hostCommand,
		Now:     time.Now,
	})
	if record.Status != ValidationFailed || hostFallback {
		t.Fatalf("validation = %#v, hostFallback = %t", record, hostFallback)
	}

	// The legacy seam keeps its historical shape too: a nil command is a failed
	// step rather than a panic.
	record = RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "host", Argv: []string{"true"}}},
		Command: func(context.Context, string, ...string) *exec.Cmd {
			return nil
		},
		Now: time.Now,
	})
	if record.Status != ValidationFailed {
		t.Fatalf("a nil legacy command must fail the step: %#v", record)
	}
}

func TestValidationContainedTransportHonoursTheStepTimeout(t *testing.T) {
	t.Parallel()
	started := time.Now()
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "slow", Argv: []string{"sleep", "5"}, Timeout: "50ms"}},
		Transport: func(ctx context.Context, _ []string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sleep", "5"), nil
		},
		Now: time.Now,
	})
	if record.Status != ValidationFailed {
		t.Fatalf("a timed-out contained step must fail validation: %#v", record)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("the contained step timeout was not enforced")
	}
}

func TestContainerValidationTransportIsCredentialFreeAndRemapsArgv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "super-secret")
	t.Setenv("DOCKER_HOST", "unix:///test-docker.sock")
	runtime := &ContainerRuntime{
		containerID:  strings.Repeat("a", 64),
		ownerUser:    "501:20",
		dockerPath:   "/usr/bin/true",
		transportEnv: containerTransportEnvironment(os.LookupEnv),
		workspace:    containerWorkspacePlan{worktreeDir: "/fleet/worktree"},
	}
	factory := runtime.ValidationTransport()
	var command *exec.Cmd
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps:       []ValidationStep{{Name: "contained", Argv: []string{"go", "test", "./..."}}},
		Transport: func(ctx context.Context, argv []string) (*exec.Cmd, error) {
			built, err := factory(ctx, argv)
			command = built
			return built, err
		},
		Now: time.Now,
	})
	if record.Status != ValidationPassed {
		t.Fatalf("validation = %#v", record)
	}
	if command == nil {
		t.Fatal("the runtime validation factory returned no command")
	}
	wantPrefix := []string{"/usr/bin/true", "exec", "--user", "501:20", "--workdir", "/fleet/worktree"}
	if len(command.Args) < len(wantPrefix) || !reflect.DeepEqual(command.Args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("Docker argv prefix = %#v, want %#v", command.Args, wantPrefix)
	}
	tail := command.Args[len(command.Args)-3:]
	if !reflect.DeepEqual(tail, []string{"go", "test", "./..."}) {
		t.Fatalf("Docker argv tail = %#v, want the validation argv", tail)
	}
	joinedArgs := strings.Join(command.Args, " ")
	if strings.Contains(joinedArgs, "DEEPSEEK_API_KEY") || strings.Contains(joinedArgs, "super-secret") {
		t.Fatalf("the Docker argv must not name or carry the provider credential: %#v", command.Args)
	}
	for index, argument := range command.Args {
		if argument == "--env" && index+1 < len(command.Args) {
			next := command.Args[index+1]
			if strings.HasPrefix(next, "DEEPSEEK_API_KEY") || strings.Contains(next, "super-secret") {
				t.Fatalf("the Docker argv must not inject the provider credential: %#v", command.Args)
			}
		}
	}
	joinedEnv := strings.Join(command.Env, "\n")
	if !strings.Contains(joinedEnv, "DOCKER_HOST=unix:///test-docker.sock") {
		t.Fatalf("the factory's Docker transport was not preserved: %#v", command.Env)
	}
	if strings.Contains(joinedEnv, "DEEPSEEK_API_KEY") || strings.Contains(joinedEnv, "super-secret") {
		t.Fatalf("a contained validation command must stay credential-free: %#v", command.Env)
	}

	var nilRuntime *ContainerRuntime
	if _, err := nilRuntime.ValidationTransport()(context.Background(), []string{"true"}); err == nil {
		t.Fatal("a nil runtime must return an error instead of panicking")
	}
}

func TestContainedValidationFailFastStopsAfterTheFirstFailedStep(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		timeout string
		fail    bool
	}{
		"timed out": {timeout: "50ms"},
		"failed":    {fail: true},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			record := RunValidation(context.Background(), ValidationOptions{
				WorktreeDir: t.TempDir(),
				RunDir:      t.TempDir(),
				Steps: []ValidationStep{
					{Name: "first", Argv: []string{"go", "test", "./..."}, Timeout: testCase.timeout},
					{Name: "second", Argv: []string{"go", "vet", "./..."}},
				},
				FailFast: true,
				Transport: func(ctx context.Context, _ []string) (*exec.Cmd, error) {
					calls++
					if testCase.fail {
						return nil, errors.New("the contained transport refused the step")
					}
					return exec.CommandContext(ctx, "/bin/sleep", "5"), nil
				},
				Now: time.Now,
			})
			if record.Status != ValidationFailed {
				t.Fatalf("validation = %#v, want an overall failure", record)
			}
			if calls != 1 {
				t.Fatalf("transport calls = %d, want the first step only", calls)
			}
			if len(record.Steps) != 1 || record.Steps[0].Status != ValidationFailed {
				t.Fatalf("steps = %#v, want only the failed first step", record.Steps)
			}
		})
	}
}

func TestLegacyValidationStillReportsEveryConfiguredStep(t *testing.T) {
	t.Parallel()
	calls := 0
	record := RunValidation(context.Background(), ValidationOptions{
		WorktreeDir: t.TempDir(),
		RunDir:      t.TempDir(),
		Steps: []ValidationStep{
			{Name: "first", Argv: []string{"false"}},
			{Name: "second", Argv: []string{"false"}},
		},
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			calls++
			return exec.CommandContext(ctx, "/bin/false")
		},
		Now: time.Now,
	})
	if calls != 2 {
		t.Fatalf("legacy validation ran %d steps, want every configured step", calls)
	}
	if record.Status != ValidationFailed || len(record.Steps) != 2 {
		t.Fatalf("validation = %#v, want both steps reported", record)
	}
}
