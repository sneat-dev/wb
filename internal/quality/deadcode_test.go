package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// stubAnalyzer writes an executable that ignores its arguments and emits the
// given stdout, exiting with code. Tests drive the real command path — argument
// assembly, exit-code handling, JSON parsing — without reaching the network for
// golang.org/x/tools, which would make the suite non-hermetic.
func stubAnalyzer(t *testing.T, stdout string, code int) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "deadcode-stub")
	body := "#!/bin/sh\ncat <<'DEADCODE_EOF'\n" + stdout + "\nDEADCODE_EOF\n"
	if code != 0 {
		body += "echo 'analyzer failed' >&2\n"
	}
	body += "exit " + itoaTest(code) + "\n"
	if err := testenv.WriteExecutableFile(script, []byte(body), 0o755); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	return []string{script}
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

const twoFindings = `[
  {"Name":"githubapp","Path":"github.com/acme/app/api/githubapp","Funcs":[
    {"Name":"Reader.Refresh","Generated":false,"Marker":false,"Position":{"File":"api/githubapp/a.go","Line":64,"Col":42}}
  ]},
  {"Name":"tui","Path":"github.com/acme/app/internal/tui","Funcs":[
    {"Name":"Model.View","Generated":false,"Marker":false,"Position":{"File":"internal/tui/m.go","Line":12,"Col":1}}
  ]}
]`

func TestDeadcodeSortsFindingsAndBuildsIdentityFromPackageAndFunction(t *testing.T) {
	t.Parallel()
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool: stubAnalyzer(t, twoFindings, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %#v, want 2", report.Findings)
	}
	// Sorted by identity, so a report and a written baseline are byte-stable
	// however the analyzer happened to order packages.
	if got := report.Findings[0].Identity; got != "github.com/acme/app/api/githubapp.Reader.Refresh" {
		t.Fatalf("first identity = %q", got)
	}
	if got := report.Findings[1].Identity; got != "github.com/acme/app/internal/tui.Model.View" {
		t.Fatalf("second identity = %q", got)
	}
	if got := report.Findings[0].Line; got != 64 {
		t.Fatalf("line = %d, want 64", got)
	}
	// With no baseline configured the run reports but never gates.
	if report.New != nil || report.BaselinePath != "" {
		t.Fatalf("unbaselined run gated: %#v", report)
	}
}

func TestDeadcodeGatesOnlyFindingsMissingFromBaseline(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	baseline := filepath.Join(repository, "baseline.txt")
	// Comments and blank lines are ignored, so the file can explain itself.
	content := "# tolerated\n\ngithub.com/acme/app/api/githubapp.Reader.Refresh\n"
	if err := os.WriteFile(baseline, []byte(content), 0o644); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}

	report, err := Deadcode(context.Background(), repository, DeadcodeOptions{
		Tool:         stubAnalyzer(t, twoFindings, 0),
		BaselinePath: "baseline.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.New) != 1 || report.New[0].Identity != "github.com/acme/app/internal/tui.Model.View" {
		t.Fatalf("new = %#v, want only the unbaselined finding", report.New)
	}
	if len(report.Fixed) != 0 {
		t.Fatalf("fixed = %#v, want none", report.Fixed)
	}
	if report.BaselineMissing {
		t.Fatal("baseline reported missing although it exists")
	}
}

func TestDeadcodeReportsBaselineEntriesThatAreReachableAgainWithoutFailing(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	// Two entries recorded, only one still dead: the other was wired up or
	// deleted. That must never fail the gate — the ratchet only tightens.
	content := "github.com/acme/app/api/githubapp.Reader.Refresh\ngithub.com/acme/app/internal/tui.Model.View\ngithub.com/acme/app/gone.Helper\n"
	if err := os.WriteFile(filepath.Join(repository, "baseline.txt"), []byte(content), 0o644); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	report, err := Deadcode(context.Background(), repository, DeadcodeOptions{
		Tool:         stubAnalyzer(t, twoFindings, 0),
		BaselinePath: "baseline.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.New) != 0 {
		t.Fatalf("new = %#v, want none", report.New)
	}
	if len(report.Fixed) != 1 || report.Fixed[0] != "github.com/acme/app/gone.Helper" {
		t.Fatalf("fixed = %#v", report.Fixed)
	}
}

func TestDeadcodeTreatsAMissingBaselineAsEveryFindingNew(t *testing.T) {
	t.Parallel()
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:         stubAnalyzer(t, twoFindings, 0),
		BaselinePath: "absent.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.BaselineMissing {
		t.Fatal("missing baseline not reported as missing")
	}
	if len(report.New) != 2 {
		t.Fatalf("new = %#v, want every finding", report.New)
	}
}

func TestDeadcodeFailsWhenTheAnalyzerItselfFails(t *testing.T) {
	t.Parallel()
	// deadcode exits 0 even when it reports findings, so a non-zero exit is a
	// build error or a missing module. Reading it as "nothing is dead" would
	// turn a broken analysis into a passing gate.
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool: stubAnalyzer(t, "", 2),
	})
	if err == nil {
		t.Fatal("analyzer failure did not fail the run")
	}
	if !strings.Contains(err.Error(), "analyzer failed") {
		t.Fatalf("error lost the analyzer diagnostic: %v", err)
	}
}

func TestDeadcodeRejectsOutputThatIsNotAnalyzerJSON(t *testing.T) {
	t.Parallel()
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool: stubAnalyzer(t, "not json at all", 0),
	})
	if err == nil || !strings.Contains(err.Error(), "parse deadcode JSON") {
		t.Fatalf("error = %v, want a parse failure", err)
	}
}

func TestDeadcodeAcceptsAnEmptyAnalysisAsClean(t *testing.T) {
	t.Parallel()
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:         stubAnalyzer(t, "", 0),
		BaselinePath: "absent.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 || len(report.New) != 0 {
		t.Fatalf("empty analysis reported findings: %#v", report)
	}
}

func TestDeadcodeHonoursItsTimeout(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "slow")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:    []string{script},
		Timeout: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("slow analyzer was not bounded")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want the timeout", err)
	}
}

func TestFormatDeadcodeBaselineSortsAndDeduplicates(t *testing.T) {
	t.Parallel()
	rendered := FormatDeadcodeBaseline([]DeadcodeFinding{
		{Identity: "z/pkg.B"},
		{Identity: "a/pkg.A"},
		{Identity: "a/pkg.A"},
	})
	var entries []string
	for _, line := range strings.Split(rendered, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	if len(entries) != 2 || entries[0] != "a/pkg.A" || entries[1] != "z/pkg.B" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestWriteDeadcodeBaselineRoundTripsThroughLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", DefaultDeadcodeBaseline)
	findings := []DeadcodeFinding{{Identity: "a/pkg.A"}, {Identity: "b/pkg.B"}}
	if err := WriteDeadcodeBaseline(path, findings); err != nil {
		t.Fatal(err)
	}
	entries, missing, err := LoadDeadcodeBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("written baseline reported missing")
	}
	if len(entries) != 2 || !entries["a/pkg.A"] || !entries["b/pkg.B"] {
		t.Fatalf("entries = %#v", entries)
	}
}

// commandError previously preferred a failing command's stdout over the error
// that described why it failed. When both carry distinct information — a
// coverage run whose tests pass and whose profile merge then fails — the cause
// was discarded and only the successful-looking output survived.
func TestCommandErrorKeepsTheUnderlyingErrorAlongsideOutput(t *testing.T) {
	t.Parallel()
	detail := commandError("wb coverage .", "ok  \tgithub.com/acme/app\t0.4s\tcoverage: 91.2% of statements",
		errors.New("merge coverage profiles: inconsistent mode line"))
	if !strings.Contains(detail, "coverage: 91.2%") {
		t.Fatalf("command output was dropped: %q", detail)
	}
	if !strings.Contains(detail, "inconsistent mode line") {
		t.Fatalf("underlying error was dropped, which is the defect: %q", detail)
	}
}

func TestCommandErrorDoesNotRepeatAnErrorAlreadyInTheOutput(t *testing.T) {
	t.Parallel()
	detail := commandError("go test ./...", "FAIL\nexit status 1", errors.New("exit status 1"))
	if got := strings.Count(detail, "exit status 1"); got != 1 {
		t.Fatalf("error duplicated %d times: %q", got, detail)
	}
}

func TestCommandErrorFallsBackToTheErrorWhenOutputIsEmpty(t *testing.T) {
	t.Parallel()
	detail := commandError("go build ./...", "   \n  ", errors.New("no space left on device"))
	if detail != "no space left on device" {
		t.Fatalf("detail = %q", detail)
	}
}

// The truncation keeps a head and a tail. The appended error must survive it,
// which is why it is appended rather than prefixed.
func TestCommandErrorSurvivesTruncationOfLargeOutput(t *testing.T) {
	t.Parallel()
	detail := commandError("wb coverage .", strings.Repeat("noise line that says nothing useful\n", 200),
		errors.New("merge coverage profiles: inconsistent mode line"))
	if len(detail) > 1000 {
		t.Fatalf("detail not truncated: %d bytes", len(detail))
	}
	if !strings.Contains(detail, "inconsistent mode line") {
		t.Fatalf("truncation dropped the underlying error: %q", detail)
	}
}
