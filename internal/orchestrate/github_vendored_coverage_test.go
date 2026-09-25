package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// orchCovGHState is a scripted `gh` whose answers live in files, so a test can
// change one fact without rebuilding the whole script. It is deliberately
// independent of the other fixtures in this package: those install exactly the
// endpoints one verb needs, and the vendored reads below are shared by several.
type orchCovGHState struct {
	dir string
}

// orchCovInstallGH writes a `gh` shell script onto PATH and returns the state
// directory its answers are read from.
func orchCovInstallGH(t *testing.T, script string) orchCovGHState {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return orchCovGHState{dir: state}
}

// answer records the body `gh` will print for one endpoint slot.
func (state orchCovGHState) answer(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state.dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// orchCovNotFound is the `gh api --include` shape of an authoritative 404: a
// status line, headers, and a body, with a non-zero exit status.
const orchCovNotFound = `#!/bin/sh
printf 'HTTP/2.0 404 Not Found\nContent-Type: application/json\n\n{"message":"Not Found"}\n'
exit 1
`

// orchCovVendoredGHScript routes every endpoint the vendored reads use.
const orchCovVendoredGHScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  case "$2" in
    */rules/branches/*) cat "$S/rules" ;;
    */branches/*) cat "$S/branch" ;;
    */pulls/*) cat "$S/pull" ;;
    */check-runs*) cat "$S/check-runs" ;;
    */status?per_page=100*) cat "$S/statuses" ;;
    *) echo "unexpected gh args: $*" >&2; exit 30 ;;
  esac
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func TestOrchCovPullRequestNumberAcceptsEverySelectorSpelling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "7", want: "7"},
		{input: "#12", want: "12"},
		{input: "acme/app#12", want: "12"},
		{input: "https://github.com/acme/app/pull/12", want: "12"},
		{input: "https://github.com/acme/app/pull/12/files", want: "12"},
		{input: "  /12/  ", want: "12"},
	} {
		got, err := PullRequestNumber(test.input)
		if err != nil {
			t.Fatalf("PullRequestNumber(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("PullRequestNumber(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestOrchCovPullRequestNumberRejectsSelectorsWithoutANumber(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "   ", "abc", "/pull/", "https://github.com/acme/app/pull/12abc", "acme/app"} {
		if got, err := PullRequestNumber(input); err == nil {
			t.Fatalf("PullRequestNumber(%q) = %q, want an error", input, got)
		}
	}
}

func TestOrchCovRepositoryFromPullRequestURLNamesTheRepository(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "https://github.com/acme/app/pull/12", want: "acme/app"},
		{input: "https://github.com/acme/app/pull/12/files", want: "acme/app"},
		{input: "  https://github.com/acme/app/pull/12  ", want: "acme/app"},
	} {
		got, err := RepositoryFromPullRequestURL(test.input)
		if err != nil {
			t.Fatalf("RepositoryFromPullRequestURL(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("RepositoryFromPullRequestURL(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	for _, input := range []string{"https://github.com/acme/app", "/pull/12", "  "} {
		if got, err := RepositoryFromPullRequestURL(input); err == nil {
			t.Fatalf("RepositoryFromPullRequestURL(%q) = %q, want an error", input, got)
		}
	}
}

func TestOrchCovReadPullRequestReturnsTheIdentityItRead(t *testing.T) {
	state := orchCovInstallGH(t, orchCovVendoredGHScript)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)
	state.answer(t, "pull", `{"number":7,"state":"open","title":"feat: the change","html_url":"https://example.test/acme/app/pull/7",`+
		`"head":{"ref":"candidate","sha":"1111111111111111111111111111111111111111"},`+
		`"base":{"ref":"main","sha":"2222222222222222222222222222222222222222"},"mergeable":true}`)

	view, err := ReadPullRequest(context.Background(), "acme/app", "https://example.test/acme/app/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if view.Number != 7 || view.Head.Ref != "candidate" || view.Base.Ref != "main" || view.Mergeable == nil || !*view.Mergeable {
		t.Fatalf("pull request view = %+v", view)
	}
}

func TestOrchCovReadPullRequestRefusesIdentityItCannotProve(t *testing.T) {
	state := orchCovInstallGH(t, orchCovVendoredGHScript)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)

	state.answer(t, "pull", `{"state":"open"}`)
	if _, err := ReadPullRequest(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "no identity") {
		t.Fatalf("identity-less pull request error = %v", err)
	}

	state.answer(t, "pull", `not json`)
	if _, err := ReadPullRequest(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "decode pull request") {
		t.Fatalf("malformed pull request error = %v", err)
	}
}

func TestOrchCovReadPullRequestRefusesAnUnaddressableRequest(t *testing.T) {
	t.Parallel()
	if _, err := ReadPullRequest(context.Background(), "acme/app", "not-a-number"); err == nil {
		t.Fatal("unaddressable selector was accepted")
	}
	if _, err := ReadPullRequest(context.Background(), "  ", "7"); err == nil ||
		!strings.Contains(err.Error(), "repository is required") {
		t.Fatalf("empty repository error = %v", err)
	}
}

func TestOrchCovReadPullRequestReportsAnAuthoritativeReadFailure(t *testing.T) {
	orchCovInstallGH(t, orchCovNotFound)
	_, err := ReadPullRequest(context.Background(), "acme/app", "7")
	if err == nil || !strings.Contains(err.Error(), "read pull request acme/app#7") ||
		!strings.Contains(err.Error(), "404") {
		t.Fatalf("read failure = %v", err)
	}
}

func TestOrchCovActiveBranchRulesReadsRulesAndToleratesAnUnruledBranch(t *testing.T) {
	state := orchCovInstallGH(t, orchCovVendoredGHScript)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)

	state.answer(t, "rules", `[{"type":"required_status_checks","ruleset_id":7,"parameters":{"required_status_checks":[{"context":"CI","integration_id":42}]}}]`)
	pages, err := activeBranchRules(context.Background(), "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || len(pages[0]) != 1 || pages[0][0].Type != "required_status_checks" || pages[0][0].RulesetID != 7 {
		t.Fatalf("active branch rules = %+v", pages)
	}

	// An unruled branch answers with an empty array, and that must not read as
	// a decode failure or as a missing receipt.
	state.answer(t, "rules", `[]`)
	pages, err = activeBranchRules(context.Background(), "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || len(pages[0]) != 0 {
		t.Fatalf("unruled branch pages = %+v", pages)
	}
}

func TestOrchCovActiveBranchRulesFollowsTheLinkHeader(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ] && [ "$2" = "repos/acme/app/rules/branches/main?per_page=100" ]; then
  cat "$S/page1"
  exit 0
fi
if [ "$1" = api ] && [ "$2" = "https://api.github.test/repos/acme/app/rules/branches/main?page=2" ]; then
  cat "$S/page2"
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCHCOV_GH_STATE", state)
	page1 := "HTTP/2.0 200 OK\nlink: <https://api.github.test/repos/acme/app/rules/branches/main?page=2>; rel=\"next\"\n\n" +
		`[{"type":"creation"}]`
	page2 := `[{"type":"update"}]`
	if err := os.WriteFile(filepath.Join(state, "page1"), []byte(page1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "page2"), []byte(page2), 0o644); err != nil {
		t.Fatal(err)
	}

	pages, err := activeBranchRules(context.Background(), "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0][0].Type != "creation" || pages[1][0].Type != "update" {
		t.Fatalf("paginated rules = %+v", pages)
	}
}

func TestOrchCovActiveBranchRulesFailsClosedOnAnUndecodablePage(t *testing.T) {
	state := orchCovInstallGH(t, orchCovVendoredGHScript)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)
	state.answer(t, "rules", `{"not":"an array"}`)
	_, err := activeBranchRules(context.Background(), "acme/app", "main")
	if err == nil || !strings.Contains(err.Error(), "decode active branch rules for main") {
		t.Fatalf("undecodable rules error = %v", err)
	}
}

// orchCovPullRequestHeadChecksScript answers every read PullRequestHeadChecks
// makes: the pull request, its head's check runs and statuses, and the target's
// required-check policy.
const orchCovPullRequestHeadChecksScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  case "$2" in
    */pulls/7) cat "$S/pull" ;;
    */check-runs*) cat "$S/check-runs" ;;
    */status?per_page=100*) cat "$S/statuses" ;;
    */rules/branches/*) cat "$S/rules" ;;
    */branches/*) cat "$S/branch" ;;
    *) echo "unexpected gh args: $*" >&2; exit 30 ;;
  esac
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func orchCovHeadChecksState(t *testing.T, branch, rules string) orchCovGHState {
	t.Helper()
	state := orchCovInstallGH(t, orchCovPullRequestHeadChecksScript)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)
	state.answer(t, "pull", `{"number":7,"state":"open",`+
		`"head":{"ref":"candidate","sha":"1111111111111111111111111111111111111111"},`+
		`"base":{"ref":"main","sha":"2222222222222222222222222222222222222222"}}`)
	state.answer(t, "branch", branch)
	state.answer(t, "rules", rules)
	state.answer(t, "statuses", `{"total_count":0,"statuses":[]}`)
	return state
}

func TestOrchCovPullRequestHeadChecksReportsGreenOnlyWhenRequiredChecksPass(t *testing.T) {
	state := orchCovHeadChecksState(t,
		`{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}`, `[]`)
	state.answer(t, "check-runs", `{"total_count":1,"check_runs":[`+
		`{"id":11,"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}`)

	checks, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if !green {
		t.Fatalf("green = %t for checks %+v", green, checks)
	}
	if !reflect.DeepEqual(checks, []HeadCheck{{Name: "check-run:CI", Bucket: "pass"}}) {
		t.Fatalf("head checks = %+v", checks)
	}
}

func TestOrchCovPullRequestHeadChecksRefusesAnUnsatisfiedRequiredCheck(t *testing.T) {
	state := orchCovHeadChecksState(t,
		`{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}`, `[]`)
	// A green observation from a different producer does not satisfy a
	// producer-pinned expectation.
	state.answer(t, "check-runs", `{"total_count":1,"check_runs":[`+
		`{"id":11,"name":"CI","status":"completed","conclusion":"success","app":{"id":7}}]}`)

	checks, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if green {
		t.Fatalf("green = %t for checks %+v", green, checks)
	}
}

func TestOrchCovPullRequestHeadChecksWeighsAnEmptyObservationAgainstTheRequiredSet(t *testing.T) {
	// A head CI has not run on yet is reported as unproven when the target
	// requires something, and as green when the target requires nothing: the
	// requirement is what an empty observation is measured against.
	t.Run("required check missing", func(t *testing.T) {
		state := orchCovHeadChecksState(t,
			`{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}`, `[]`)
		state.answer(t, "check-runs", `{"total_count":0,"check_runs":[]}`)

		checks, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
		if err != nil {
			t.Fatal(err)
		}
		if green || len(checks) != 0 {
			t.Fatalf("unobserved required check reported checks=%+v green=%t", checks, green)
		}
	})
	t.Run("nothing required", func(t *testing.T) {
		state := orchCovHeadChecksState(t, `{"protected":true,"protection":{}}`, `[]`)
		state.answer(t, "check-runs", `{"total_count":0,"check_runs":[]}`)

		checks, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
		if err != nil {
			t.Fatal(err)
		}
		if !green || len(checks) != 0 {
			t.Fatalf("unrequired empty observation reported checks=%+v green=%t", checks, green)
		}
	})
}

func TestOrchCovPullRequestHeadChecksReportsAFailedAndASkippedObservation(t *testing.T) {
	state := orchCovHeadChecksState(t, `{"protected":true,"protection":{}}`, `[]`)
	state.answer(t, "check-runs", `{"total_count":2,"check_runs":[`+
		`{"id":11,"name":"CI","status":"completed","conclusion":"failure","app":{"id":42}},`+
		`{"id":12,"name":"Docs","status":"completed","conclusion":"skipped","app":{"id":42}}]}`)

	checks, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if green {
		t.Fatalf("a failed check reported green: %+v", checks)
	}
	if !reflect.DeepEqual(checks, []HeadCheck{
		{Name: "check-run:CI", Bucket: "fail"},
		{Name: "check-run:Docs", Bucket: "skipping"},
	}) {
		t.Fatalf("head checks = %+v", checks)
	}
}

func TestOrchCovPullRequestHeadChecksSurfacesAReadFailure(t *testing.T) {
	if _, _, err := PullRequestHeadChecks(context.Background(), "acme/app", "not-a-number"); err == nil {
		t.Fatal("unaddressable selector was accepted")
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, _, err := PullRequestHeadChecks(context.Background(), "acme/app", "7"); err == nil {
		t.Fatal("unreadable pull request was accepted")
	}
}

func TestOrchCovActiveBranchRulesReportsAReadFailure(t *testing.T) {
	orchCovInstallGH(t, orchCovNotFound)
	if pages, err := activeBranchRules(context.Background(), "acme/app", "main"); err == nil {
		t.Fatalf("unreadable branch rules = %+v, want an error", pages)
	}
}

func TestOrchCovPullRequestHeadChecksReportsEveryUnreadableObservation(t *testing.T) {
	for _, test := range []struct {
		name    string
		slot    string
		body    string
		wantErr string
	}{
		{name: "check runs", slot: "check-runs", body: "not json", wantErr: "decode GitHub check runs"},
		{name: "commit statuses", slot: "statuses", body: "not json", wantErr: "decode GitHub commit statuses"},
		{name: "required checks", slot: "branch", body: "not json", wantErr: "read required checks for main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := orchCovHeadChecksState(t, `{"protected":true,"protection":{}}`, `[]`)
			if test.slot != "check-runs" {
				state.answer(t, "check-runs", `{"total_count":0,"check_runs":[]}`)
			}
			state.answer(t, test.slot, test.body)
			_, green, err := PullRequestHeadChecks(context.Background(), "acme/app", "7")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("unreadable %s error = %v, want %q", test.name, err, test.wantErr)
			}
			if green {
				t.Fatalf("unreadable %s reported green", test.name)
			}
		})
	}
}

func TestOrchCovObservedSatisfiesMatchesNameProducerAndBucket(t *testing.T) {
	t.Parallel()
	observed := []RemoteCheck{
		{Name: "status:legacy", Bucket: "pass", AppID: 0},
		{Name: "check-run:CI", Bucket: "fail", AppID: 42},
		{Name: "check-run:CI", Bucket: "pass", AppID: 43},
	}
	for _, test := range []struct {
		name        string
		expectation RequiredRemoteCheck
		want        bool
	}{
		{name: "unpinned pass", expectation: RequiredRemoteCheck{Name: "legacy"}, want: true},
		{name: "pinned producer has a passing observation", expectation: RequiredRemoteCheck{Name: "CI", IntegrationID: 43}, want: true},
		{name: "pinned producer has no passing observation", expectation: RequiredRemoteCheck{Name: "CI", IntegrationID: 44}, want: false},
		{name: "unknown name", expectation: RequiredRemoteCheck{Name: "Deploy"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := observedSatisfies(observed, test.expectation); got != test.want {
				t.Fatalf("observedSatisfies(%+v) = %t, want %t", test.expectation, got, test.want)
			}
		})
	}
}
