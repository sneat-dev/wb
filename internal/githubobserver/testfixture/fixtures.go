package testfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/testenv"
)

// Reporter is the fatal error contract used by a concrete fixture. *testing.T
// implements it directly; an error observer can verify failing fixture inputs.
// Fatal must stop the current call, as testing.T.Fatal does.
type Reporter interface {
	Helper()
	Fatal(...any)
}

// Environment supplies a fixture's private paths and process environment.
type Environment interface {
	Reporter
	TempDir() string
	Setenv(string, string)
}

// State is a scripted `gh` whose answers live in files, so a test can
// change one fact without rebuilding the whole script. It is deliberately
// independent of the other fixtures in this package: those install exactly the
// endpoints one verb needs, and the vendored reads below are shared by several.
type State struct {
	Dir string
}

// InstallGH writes a `gh` shell script onto PATH and returns the state
// directory its answers are read from.
func InstallGH(t Environment, script string) State {
	t.Helper()
	// Every caller of this fixture reaches a migrated runCommand call site
	// (spec/plans/coverage-to-100 task-17) whose production runner is
	// task-24's guarded runner.Real, even though the process it starts is
	// this fixture's own fake `gh` on PATH, not a real one. One
	// AllowRealProcess here covers every test that installs its script
	// through this helper.
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(WithEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return State{Dir: state}
}

// Answer records the body `gh` will print for one endpoint slot.
func (state State) Answer(t Reporter, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state.Dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// orchCovNotFound is the `gh api --include` shape of an authoritative 404: a
// status line, headers, and a body, with a non-zero exit status.
const NotFound = `#!/bin/sh
printf 'HTTP/2.0 404 Not Found\nContent-Type: application/json\n\n{"message":"Not Found"}\n'
exit 1
`

func WithEmptyActionsRuns(contents string) string {
	if strings.Contains(contents, "/actions/runs?head_sha=") {
		return contents
	}
	const response = `if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
`
	return strings.Replace(contents, "#!/bin/sh\n", "#!/bin/sh\n"+response, 1)
}

// ScriptState writes a gh script whose answers are read from files and
// returns the state directory.
func ScriptState(t Environment, script string) State {
	t.Helper()
	state := InstallGH(t, script)
	t.Setenv("ORCHCOV_GH_STATE", state.Dir)
	return state
}

const OneEndpointScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
cat "$S/body"
exit "$(cat "$S/exit")"
`

func PullRequestView[T any](t Reporter, head string) T {
	t.Helper()
	var view T
	data := `{"number":7,"state":"open","head":{"ref":"feature","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":"target","repo":{"full_name":"acme/app"}}}`
	if err := json.Unmarshal([]byte(data), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// InstallTransientReadGH puts a fake gh on PATH and pins the observer
// state dir so retries stay hermetic per test, without the reread-specific
// observation counter installRereadTestGH also wires up.
func InstallTransientReadGH(t Environment, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(WithEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const DirectCIHead = "0123456789012345678901234567890123456789"

func InstallDirectCIGH(t Environment) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  repos/acme/app/branches/integration) echo '{"protected":false,"protection":{}}' ;;
  'repos/acme/app/rules/branches/integration?per_page=100') echo '[]' ;;
  repos/acme/app/pulls/17) echo "$WB_TEST_PR" ;;
  repos/acme/app/git/ref/heads/integration) echo '{"object":{"sha":"` + DirectCIHead + `"}}' ;;
  repos/acme/app/branches/main) echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["Required checks passed"]}}}' ;;
  repos/acme/app/branches/main/protection/required_status_checks) echo '{"strict":true,"contexts":["Required checks passed"],"checks":[]}' ;;
  'repos/acme/app/rules/branches/main?per_page=100') echo '[]' ;;
  repos/acme/app/actions/workflows/go-ci.yml)
    if [ "$WB_TEST_WORKFLOW_ERROR" = 1 ]; then echo 'workflow unavailable' >&2; exit 1; fi
    echo "$WB_TEST_WORKFLOW" ;;
  'repos/acme/app/actions/runs?head_sha=` + DirectCIHead + `&per_page=100') echo "$WB_TEST_RUNS" ;;
  'repos/acme/app/commits/` + DirectCIHead + `/check-runs?per_page=100') echo "$WB_TEST_CHECK_RUNS" ;;
  'repos/acme/app/commits/` + DirectCIHead + `/status?per_page=100') echo '{"total_count":0,"statuses":[]}' ;;
  *) echo "unexpected gh request: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_TEST_PR", `{"number":17,"state":"open","head":{"ref":"integration","sha":"`+DirectCIHead+`","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	t.Setenv("WB_TEST_WORKFLOW", `{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}`)
	t.Setenv("WB_TEST_RUNS", `{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"`+DirectCIHead+`","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}]}`)
	t.Setenv("WB_TEST_CHECK_RUNS", `{"total_count":0,"check_runs":[]}`)
}
