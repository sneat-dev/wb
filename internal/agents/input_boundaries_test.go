package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCredentialFailuresRetainBoundaryAndDoNotReadAfterInspectionFailure(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"inspection", "read"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			path := writeCredentialFile(t, 0600, "private-value")
			failure := errors.New("credential became unavailable")
			reads := 0
			inspect := os.Lstat
			if operation == "inspection" {
				inspect = func(got string) (os.FileInfo, error) {
					if got != path {
						t.Fatalf("path=%q", got)
					}
					return nil, failure
				}
			}
			value, err := readCredentialFileWith(path, inspect, func(got string) ([]byte, error) {
				reads++
				if got != path {
					t.Fatalf("path=%q", got)
				}
				return nil, failure
			})
			if value != "" || !errors.Is(err, failure) || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("value=%q error=%v", value, err)
			}
			wantReads := 1
			if operation == "inspection" {
				wantReads = 0
			}
			if reads != wantReads {
				t.Fatalf("reads=%d", reads)
			}
		})
	}
}

func TestHomeFallbackUsesPerCallEnvironmentAndResolver(t *testing.T) {
	t.Parallel()
	for _, home := range []string{"", "  ", " /private/home "} {
		t.Run(home, func(t *testing.T) {
			t.Parallel()
			calls := 0
			got := homeDirWith(func(name string) string {
				if name != "HOME" {
					t.Fatalf("name=%q", name)
				}
				return home
			}, func() (string, error) { calls++; return "/fallback/home", nil })
			want := strings.TrimSpace(home)
			wantCalls := 0
			if want == "" {
				want = "/fallback/home"
				wantCalls = 1
			}
			if got != want || calls != wantCalls {
				t.Fatalf("home=%q calls=%d", got, calls)
			}
		})
	}
	if got := homeDirWith(func(string) string { return "" }, func() (string, error) { return "", errors.New("home unavailable") }); got != "" {
		t.Fatalf("failed resolver home=%q", got)
	}
}

func TestHarnessEncodingKeepsControlAndUnicodeValuesQuoted(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"plain", "line\nquote\"\\\x00", "λ☃", "invalid\xffbyte"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			var decoded string
			if err := json.Unmarshal([]byte(encodeConfigValue(value)), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded != strings.ToValidUTF8(value, "�") {
				t.Fatalf("quoted=%q decoded=%q", value, decoded)
			}
			options := codexOptions()
			options.Reasoning = value
			argv, err := CodexArgv(options)
			if err != nil {
				t.Fatal(err)
			}
			values := configValues(t, argv)
			var patterns []string
			if err := json.Unmarshal([]byte(values["shell_environment_policy.exclude"]), &patterns); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(patterns, []string{"*KEY*", "*TOKEN*", "*SECRET*"}) {
				t.Fatalf("exclusions=%v", patterns)
			}
		})
	}
}

func TestRemoteLookupFailureStopsBeforeSSH(t *testing.T) {
	t.Parallel()
	failure := errors.New("ssh unavailable")
	runner := &fakeSSH{}
	request := RemoteRequest{SchemaVersion: 1, Operation: RemoteList}
	_, err := CallRemote(context.Background(), testTarget(), request, RemoteDeps{LookPath: func(string) (string, error) { return "", failure }, Runner: runner})
	if !errors.Is(err, failure) || runner.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, runner.calls)
	}
	if got := StripAgentID("  :agt-invalid "); got != ":agt-invalid" {
		t.Fatalf("invalid reference=%q", got)
	}
}

type deadlineBoundarySSH struct{}

func (deadlineBoundarySSH) Run(ctx context.Context, _ string, _ []string, _ []byte, _, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRemoteDeadlineIsReportedAsTransportDeadline(t *testing.T) {
	t.Parallel()
	deps := fakeRemoteDeps(t, &fakeSSH{})
	deps.Runner = deadlineBoundarySSH{}
	deps.Timeout = time.Nanosecond
	_, err := CallRemote(context.Background(), testTarget(), RemoteRequest{SchemaVersion: 1, Operation: RemoteList}, deps)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("deadline=%v", err)
	}
}

func TestOwnerSignalFailurePreservesEvidenceAndDoesNotEscalate(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.WorkerPID = os.Getpid()
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := stopRunWithSignal(store, record.AgentID, DefaultOwnerDeps(), func(pid int, signal syscall.Signal) error {
		calls++
		if pid != record.WorkerPID || signal != terminationSignal() {
			t.Fatalf("signal=%d/%v", pid, signal)
		}
		return syscall.EPERM
	})
	if !errors.Is(err, syscall.EPERM) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	loaded, err := store.Load(record.AgentID)
	if err != nil || loaded.State != record.State || loaded.WorkerPID != record.WorkerPID {
		t.Fatalf("evidence=%+v %v", loaded, err)
	}
	if err := terminateOwnerWithSignal(1, terminationSignal(), func(int, syscall.Signal) error { return syscall.EPERM }); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("signal error=%v", err)
	}
	if err := terminateOwnerWithSignal(1, terminationSignal(), func(int, syscall.Signal) error { return syscall.ESRCH }); err != nil {
		t.Fatalf("already gone=%v", err)
	}
}

func TestOwnerNullOpenFailureStopsBeforeStartingProcess(t *testing.T) {
	t.Parallel()
	failure := errors.New("null device unavailable")
	opened := 0
	pid, err := spawnOwnerWithNull(t.TempDir(), os.Executable, func() (*os.File, error) { opened++; return nil, failure })
	if pid != 0 || !errors.Is(err, failure) || opened != 1 {
		t.Fatalf("pid=%d error=%v opens=%d", pid, err, opened)
	}
}

func TestStoreRefusesInvalidTimestampBeforeOverwritingEvidence(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path := store.RecordPath(record.AgentID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record.FinishedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Save(record); err == nil || !strings.Contains(err.Error(), "encode agent run record") {
		t.Fatalf("invalid timestamp accepted: %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Fatalf("prior evidence changed: %v", err)
	}
}

func TestDispatchRefusesBlankTaskAndUnsupportedModeWithoutLaunching(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"blank task", "unsupported mode"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			credential := writeCredentialFile(t, 0600, "local-test-credential")
			loads := 0
			deps := DispatchDeps{Home: home, ProjectsRoot: t.TempDir(), LoadConfig: func() (Config, error) {
				loads++
				return Config{Providers: map[string]Provider{"private": {BaseURL: "https://example.invalid", WireAPI: WireAPIResponses, CredentialFile: credential}}, Profiles: map[string]Profile{"p": {Harness: HarnessCodex, Provider: "private", Model: "m"}}}, nil
			}, SpawnOwner: func(string) (int, error) { t.Fatal("refused request started owner"); return 0, nil }}
			request := DispatchRequest{Mode: "unsupported", Worktree: "boundary", Profile: "p", Task: "exact task", Timeout: time.Second}
			if phase == "blank task" {
				request.Task = " \t\n"
			}
			_, err := Dispatch(context.Background(), request, deps)
			want := "unsupported worktree mode"
			wantLoads := 1
			if phase == "blank task" {
				want = "--task or --task-file"
				wantLoads = 0
			}
			if err == nil || !strings.Contains(err.Error(), want) || loads != wantLoads {
				t.Fatalf("error=%v loads=%d", err, loads)
			}
		})
	}
}

func TestRemoteTargetConfigurationRefusesInvalidSSHBeforeResolution(t *testing.T) {
	t.Parallel()
	path := writeRemoteConfig(t, "session_move:\n  targets:\n    private:\n      default_courier: ssh\n      ssh:\n        host: example.invalid\n        wb_path: relative/wb\n")
	if _, err := ResolveRemoteTarget(path, "private"); err == nil || !strings.Contains(err.Error(), "clean absolute") {
		t.Fatalf("invalid transport configuration accepted: %v", err)
	}
}

func TestDefaultRemoteExecutableLookupUsesNativeBoundary(t *testing.T) {
	t.Parallel()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultRemoteDeps().LookPath(path)
	if err != nil || got != path {
		t.Fatalf("lookup=%q %v", got, err)
	}
	if _, err := DefaultRemoteDeps().LookPath(filepath.Join(t.TempDir(), "missing-ssh")); err == nil {
		t.Fatal("missing executable accepted")
	}
}
