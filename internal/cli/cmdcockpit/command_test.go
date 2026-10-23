package cmdcockpit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"net/url"
	"slices"
	"strings"
	"testing"
)

type testCodedError struct {
	code    int
	message string
}

func (e *testCodedError) Error() string { return e.message }
func testRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return &testCodedError{code, message} }}
}
func testExitCode(err error) int {
	var e *testCodedError
	if errors.As(err, &e) {
		return e.code
	}
	if err != nil {
		return 1
	}
	return 0
}
func cockpitTestDependencies(t *testing.T, opened *[]string, mints *int) Dependencies {
	t.Helper()
	return Dependencies{Open: func(target string) error { *opened = append(*opened, target); return nil }, IsTerminal: func(any) bool { return true }, HostedURL: func() (string, error) { return "https://cockpit.example.test/wb/", nil }, Local: func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		session := cockpitrun.LocalSession{Listen: "127.0.0.1:8766"}
		if request.Mint {
			*mints++
			session.Code, session.Path, session.Key = "abc123", cockpit.LoginPath, "k3y_-K"
		}
		return session, nil
	}}
}
func runCockpit(t *testing.T, flags *shared.Flags, deps Dependencies, args ...string) (string, string, error) {
	t.Helper()
	command := New(testRuntime(flags), deps)
	command.SetArgs(args)
	command.SilenceUsage, command.SilenceErrors = true, true
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

type cockpitFailingWriter struct{}

func (cockpitFailingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }
func TestCockpitLocalOpensTheLoginURL(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	stdout, _, err := runCockpit(t, &shared.Flags{ProjectsRoot: "/root"}, cockpitTestDependencies(t, &opened, &mints))
	if err != nil {
		t.Fatal(err)
	}
	// The session key is in the fragment, which no server is sent
	// (cockpit#ac:session-key-reaches-the-page-in-the-fragment).
	want := "http://127.0.0.1:8766/cockpit/session/login?code=abc123#key=k3y_-K"
	if len(opened) != 1 || opened[0] != want || mints != 1 || stdout != "cockpit: "+want+"\n" {
		t.Fatalf("opened = %v, mints = %d, stdout = %q", opened, mints, stdout)
	}
	if parsed, err := url.Parse(want); err != nil || parsed.RawQuery != "code=abc123" || parsed.Fragment != "key=k3y_-K" || strings.Contains(parsed.RequestURI(), "k3y_-K") {
		t.Fatalf("the login URL %q sends the key to the server: %+v (%v)", want, parsed, err)
	}
}

func TestCockpitPrintsTheLoginURLOnlyOnATerminalOrWhenAsked(t *testing.T) {
	t.Parallel()
	const plain, login = "http://127.0.0.1:8766/cockpit/", "http://127.0.0.1:8766/cockpit/session/login?code=abc123#key=k3y_-K"
	for name, test := range map[string]struct {
		terminal   bool
		args       []string
		mints      int
		stdout     string
		hint       bool
		wantOpened int
	}{
		"a terminal":                    {true, nil, 1, "cockpit: " + login + "\n", false, 1},
		"a pipe":                        {false, nil, 0, "cockpit: " + plain + "\n", true, 0},
		"a pipe with --print-url":       {false, []string{"--print-url"}, 1, "cockpit: " + login + "\n", false, 0},
		"a terminal with --print-url":   {true, []string{"--print-url"}, 1, "cockpit: " + login + "\n", false, 1},
		"JSON":                          {true, []string{"--json"}, 0, `{"url":"` + plain + `","scope":"local","opened":false}` + "\n", false, 0},
		"JSON to a pipe":                {false, []string{"--format=json"}, 0, `{"url":"` + plain + `","scope":"local","opened":false}` + "\n", false, 0},
		"JSON with --print-url":         {false, []string{"--json", "--print-url"}, 1, `{"url":"` + plain + `","login_url":"` + login + `","scope":"local","opened":false}` + "\n", false, 0},
		"JSON on a terminal, asked for": {true, []string{"--json", "--print-url"}, 1, `{"url":"` + plain + `","login_url":"` + login + `","scope":"local","opened":false}` + "\n", false, 0},
	} {
		var opened []string
		var mints int
		deps := cockpitTestDependencies(t, &opened, &mints)
		deps.IsTerminal = func(any) bool { return test.terminal }
		stdout, stderr, err := runCockpit(t, &shared.Flags{}, deps, test.args...)
		if err != nil || mints != test.mints || stdout != test.stdout || len(opened) != test.wantOpened {
			t.Errorf("%s: err = %v, mints = %d, opened = %v, stdout = %q, want %d mints and %q", name, err, mints, opened, stdout, test.mints, test.stdout)
		}
		if hinted := strings.Contains(stderr, "--print-url"); hinted != test.hint || strings.Contains(stderr, "abc123") || strings.Contains(stderr, "k3y_-K") {
			t.Errorf("%s: stderr = %q, want the --print-url hint: %v, and never the credential", name, stderr, test.hint)
		}
	}
	// Hosted has no login URL to print.
	var opened []string
	_, _, err := runCockpit(t, &shared.Flags{}, cockpitTestDependencies(t, &opened, new(int)), "--hosted", "--print-url")
	if err == nil || testExitCode(err) != 2 || !strings.Contains(err.Error(), "--hosted") || len(opened) != 0 {
		t.Fatalf("--hosted --print-url: err = %v, want a usage error", err)
	}
}

func TestCockpitPrintsAStartWarningToStderr(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	inner := deps.Local
	deps.Local = func(ctx context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		session, err := inner(ctx, request)
		session.Warning = "provenance mismatch"
		return session, err
	}
	_, stderr, err := runCockpit(t, &shared.Flags{}, deps)
	if err != nil || !strings.Contains(stderr, "provenance mismatch") {
		t.Fatalf("err = %v, stderr = %q", err, stderr)
	}
}

func TestCockpitBrowserOpensOnlyForAnInteractiveTerminal(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		inv      *shared.Flags
		terminal bool
		args     []string
		want     int
	}{
		"terminal and interactive":    {&shared.Flags{}, true, nil, 1},
		"stdout is not a terminal":    {&shared.Flags{}, false, nil, 0},
		"not a terminal, URL asked":   {&shared.Flags{}, false, []string{"--print-url"}, 0},
		"non-interactive":             {&shared.Flags{NonInteractive: true}, true, nil, 0},
		"hosted terminal interactive": {&shared.Flags{}, true, []string{"--hosted"}, 1},
		"hosted non-interactive":      {&shared.Flags{NonInteractive: true}, true, []string{"--hosted"}, 0},
		"hosted stdout is not a tty":  {&shared.Flags{}, false, []string{"--hosted"}, 0},
	} {
		var opened []string
		var mints int
		deps := cockpitTestDependencies(t, &opened, &mints)
		deps.IsTerminal = func(any) bool { return test.terminal }
		if slices.Contains(test.args, "--hosted") {
			deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
				return cockpitrun.LocalSession{}, errors.New("unexpected daemon contact")
			}
		}
		stdout, _, err := runCockpit(t, test.inv, deps, test.args...)
		if err != nil || len(opened) != test.want || !strings.HasPrefix(stdout, "cockpit: http") {
			t.Fatalf("%s: err = %v, opened = %v, stdout = %q", name, err, opened, stdout)
		}
	}
}

func TestCockpitOpenFailureStillPrintsTheURL(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.Open = func(string) error { return errors.New("no display") }
	stdout, stderr, err := runCockpit(t, &shared.Flags{}, deps)
	if err != nil || !strings.HasPrefix(stdout, "cockpit: http://127.0.0.1:8766/cockpit/session/login?code=abc123") || !strings.Contains(stderr, "no display") {
		t.Fatalf("err = %v, stdout = %q, stderr = %q", err, stdout, stderr)
	}
}

func TestCockpitUsesTheCanonicalIPv6Origin(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		return cockpitrun.LocalSession{Listen: "[::1]:9000", Code: "c", Path: cockpit.LoginPath, Key: "k"}, nil
	}
	if _, _, err := runCockpit(t, &shared.Flags{}, deps); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "http://[::1]:9000/cockpit/session/login?code=c#key=k" {
		t.Fatalf("opened = %v", opened)
	}
}

func TestCockpitLoginURLUsesTheAddressTheDaemonListensOn(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		return cockpitrun.LocalSession{Listen: "127.0.0.2:9000", Code: "c", Key: "k", Path: cockpit.LoginPath}, nil
	}
	if _, _, err := runCockpit(t, &shared.Flags{}, deps); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "http://127.0.0.2:9000/cockpit/session/login?code=c#key=k" {
		t.Fatalf("opened = %v", opened)
	}
}

func TestCockpitJSONCarriesNoCodeAndOpensNothing(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--format=json", "--json"} {
		var opened []string
		var mints int
		stdout, _, err := runCockpit(t, &shared.Flags{}, cockpitTestDependencies(t, &opened, &mints), flag)
		if err != nil {
			t.Fatal(err)
		}
		var result cockpitOpenResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatal(err)
		}
		if len(opened) != 0 || mints != 0 || result.Opened || result.Scope != "local" || result.URL != "http://127.0.0.1:8766/cockpit/" ||
			strings.Contains(stdout, "?") || strings.Contains(stdout, "code") || strings.Contains(stdout, "#") || strings.Contains(stdout, "login_url") {
			t.Fatalf("opened = %v, mints = %d, stdout = %q", opened, mints, stdout)
		}
	}
}

func TestCockpitHostedUsesTheConfiguredURLAndStartsNoDaemon(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.HostedURL = func() (string, error) { return "https://cockpit.example.test/wb/", nil }
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		return cockpitrun.LocalSession{}, errors.New("unexpected daemon start")
	}
	stdout, _, err := runCockpit(t, &shared.Flags{}, deps, "--hosted")
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "https://cockpit.example.test/wb/" || stdout != "cockpit: https://cockpit.example.test/wb/\n" {
		t.Fatalf("opened = %v, stdout = %q", opened, stdout)
	}
	stdout, _, err = runCockpit(t, &shared.Flags{}, deps, "--hosted", "--json")
	var result cockpitOpenResult
	if err != nil || json.Unmarshal([]byte(stdout), &result) != nil {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	if result != (cockpitOpenResult{URL: "https://cockpit.example.test/wb/", Scope: "hosted"}) || len(opened) != 1 {
		t.Fatalf("result = %+v, opened = %v", result, opened)
	}
}

func TestCockpitHostedRejectsABadConfiguration(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.HostedURL = func() (string, error) { return "", errors.New("hosted_url is not a URL") }
	stdout, _, err := runCockpit(t, &shared.Flags{}, deps, "--hosted")
	if err == nil || !strings.Contains(err.Error(), "cockpit configuration") || stdout != "" || len(opened) != 0 {
		t.Fatalf("err = %v, stdout = %q, opened = %v", err, stdout, opened)
	}
}

func TestCockpitErrorsPrintNoPartialURL(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		return cockpitrun.LocalSession{}, errors.New("daemon down")
	}
	stdout, _, err := runCockpit(t, &shared.Flags{}, deps)
	if err == nil || !strings.Contains(err.Error(), "daemon down") || stdout != "" || len(opened) != 0 {
		t.Fatalf("err = %v, stdout = %q, opened = %v", err, stdout, opened)
	}
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		return cockpitrun.LocalSession{Listen: "no-port"}, nil
	}
	stdout, _, err = runCockpit(t, &shared.Flags{}, deps)
	if err == nil || !strings.Contains(err.Error(), "no-port") || stdout != "" {
		t.Fatalf("err = %v, stdout = %q", err, stdout)
	}
}

func TestCockpitRejectsAConflictingFormat(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	_, _, err := runCockpit(t, &shared.Flags{}, cockpitTestDependencies(t, &opened, &mints), "--json", "--format=yaml")
	if err == nil || !strings.Contains(err.Error(), "--json") || mints != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestCockpitReportsAStdoutWriteFailure(t *testing.T) {
	t.Parallel()
	var opened []string
	var mints int
	command := New(testRuntime(&shared.Flags{}), cockpitTestDependencies(t, &opened, &mints))
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetOut(cockpitFailingWriter{})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "stdout closed") || len(opened) != 0 {
		t.Fatalf("err = %v, opened = %v", err, opened)
	}
}

func TestCockpitListenFlag(t *testing.T) {
	t.Parallel()
	// Default unchanged: the stock address reaches the daemon start.
	var opened []string
	var mints int
	deps := cockpitTestDependencies(t, &opened, &mints)
	var gotListen string
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		gotListen = request.Listen
		if request.Listen == "" {
			request.Listen = daemonruntime.DefaultListen
		}
		return cockpitrun.LocalSession{Listen: request.Listen, Code: map[bool]string{true: "c1"}[request.Mint], Key: map[bool]string{true: "k1"}[request.Mint], Path: cockpit.LoginPath}, nil
	}
	// No flag: the command leaves the choice to the daemon lookup (empty), which defaults below.
	if _, _, err := runCockpit(t, &shared.Flags{}, deps, "--json"); err != nil || gotListen != "" {
		t.Fatalf("unset listen = %q, err = %v", gotListen, err)
	}
	// A custom loopback address reaches Start and builds the printed URL.
	stdout, _, err := runCockpit(t, &shared.Flags{}, deps, "--listen", "127.0.0.1:43211")
	if err != nil || gotListen != "127.0.0.1:43211" || stdout != "cockpit: http://127.0.0.1:43211/cockpit/session/login?code=c1#key=k1\n" {
		t.Fatalf("listen = %q, stdout = %q, err = %v", gotListen, stdout, err)
	}
	// JSON output carries the right origin.
	stdout, _, err = runCockpit(t, &shared.Flags{}, deps, "--listen=127.0.0.1:43211", "--json")
	var result cockpitOpenResult
	if jsonErr := json.Unmarshal([]byte(stdout), &result); err != nil || jsonErr != nil || result.URL != "http://127.0.0.1:43211/cockpit/" {
		t.Fatalf("stdout = %q, err = %v, %v", stdout, err, jsonErr)
	}
	// A non-loopback address is refused before anything starts.
	gotListen = ""
	deps.Local = func(_ context.Context, request cockpitrun.LocalRequest) (cockpitrun.LocalSession, error) {
		t.Fatal("daemon contacted for a non-loopback address")
		return cockpitrun.LocalSession{}, nil
	}
	for _, bad := range []string{"0.0.0.0:9000", "example.com:80", "nope", ""} {
		if _, _, err := runCockpit(t, &shared.Flags{}, deps, "--listen", bad); err == nil || !strings.Contains(err.Error(), "--listen") {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}
