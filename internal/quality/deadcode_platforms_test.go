package quality

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func deadFunctionJSON(pkg, name string) string {
	return `{"Name":"p","Path":"` + pkg + `","Funcs":[{"Name":"` + name + `","Position":{"File":"f.go","Line":1,"Col":1}}]}`
}

// expectPlatformRun scripts the analyzer's answer for one GOOS, and checks the
// call carried the fixed analysis environment.
func expectPlatformRun(fake *runnertest.Fake, program, platform, stdout string) {
	fake.Expect(func(call runnertest.Call) bool {
		return call.Name == program && slices.Contains(call.Opts.Env, "GOOS="+platform) &&
			slices.Contains(call.Opts.Env, "GOARCH=amd64") && slices.Contains(call.Opts.Env, "CGO_ENABLED=0") &&
			slices.Equal(call.Args, []string{"-json", "./..."})
	}, runner.Result{Stdout: stdout}, nil)
}

// TestDeadcodeReportsOnlyFunctionsDeadOnEveryPlatform proves the verdict no
// longer depends on the host: a function dead on one platform but reachable
// on another (a Linux-only procfs reader) is not a finding anywhere.
func TestDeadcodeReportsOnlyFunctionsDeadOnEveryPlatform(t *testing.T) {
	t.Parallel()
	shared := deadFunctionJSON("example.com/m/shared", "Dead")
	darwinOnly := deadFunctionJSON("example.com/m/procfs", "Read")
	fake := runnertest.New(t)
	expectPlatformRun(fake, "analyzer", "linux", "["+shared+"]")
	expectPlatformRun(fake, "analyzer", "darwin", "["+shared+","+darwinOnly+"]")
	expectPlatformRun(fake, "analyzer", "windows", "["+shared+"]")

	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool: []string{"analyzer"}, Platforms: []string{"linux", "darwin", "windows"}, Runner: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Identity != "example.com/m/shared.Dead" {
		t.Fatalf("findings = %#v, want only example.com/m/shared.Dead", report.Findings)
	}
	if got := strings.Join(report.Platforms, ","); got != "linux,darwin,windows" {
		t.Fatalf("report platforms = %q", got)
	}
}

// TestDeadcodeReportsDuplicateIdentityOnceAndKeepsNothingWhenPlatformsDisagree
// covers the two edges of the intersection: a repeated record is one finding,
// and disjoint platform sets leave nothing dead.
func TestDeadcodeReportsDuplicateIdentityOnceAndKeepsNothingWhenPlatformsDisagree(t *testing.T) {
	t.Parallel()
	dead := deadFunctionJSON("example.com/m/a", "F")
	fake := runnertest.New(t)
	expectPlatformRun(fake, "analyzer", "linux", "["+dead+","+dead+"]")
	expectPlatformRun(fake, "analyzer", "darwin", "["+dead+"]")
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Tool: []string{"analyzer"}, Platforms: []string{"linux", "darwin"}, Runner: fake})
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("duplicate identity = %#v, %v; want one finding", report.Findings, err)
	}

	fake = runnertest.New(t)
	expectPlatformRun(fake, "analyzer", "linux", "["+dead+"]")
	expectPlatformRun(fake, "analyzer", "darwin", "["+deadFunctionJSON("example.com/m/b", "G")+"]")
	report, err = Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Tool: []string{"analyzer"}, Platforms: []string{"linux", "darwin"}, Runner: fake})
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("disjoint platforms = %#v, %v; want no findings", report.Findings, err)
	}
}

// TestDeadcodeNamesThePlatformWhoseAnalysisFailed keeps a failing platform a
// failed gate instead of a silently smaller intersection.
func TestDeadcodeNamesThePlatformWhoseAnalysisFailed(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	expectPlatformRun(fake, "analyzer", "linux", "[]")
	fake.Expect(func(call runnertest.Call) bool { return slices.Contains(call.Opts.Env, "GOOS=plan9") },
		runner.Result{Stderr: "build constraints exclude every file"}, errors.New("exit status 1"))
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Tool: []string{"analyzer"}, Platforms: []string{"linux", "plan9"}, Runner: fake})
	if err == nil || !strings.Contains(err.Error(), "platform plan9") || !strings.Contains(err.Error(), "build constraints exclude every file") {
		t.Fatalf("error = %v, want it to name platform plan9 and the analyzer's complaint", err)
	}
}

func isGoInstall(call runnertest.Call) bool {
	return call.Name == "go" && slices.Equal(call.Args, []string{"install", "golang.org/x/tools/cmd/deadcode@v0.50.0"})
}

// TestDeadcodeInstallsThePinnedAnalyzerForTheHostThenRunsItPerDefaultPlatform
// covers the production path: no Tool override means the pinned analyzer is
// installed for the host (never for the analysis platform) and the installed
// binary runs for linux, darwin and windows.
func TestDeadcodeInstallsThePinnedAnalyzerForTheHostThenRunsItPerDefaultPlatform(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		return isGoInstall(call) && slices.Contains(call.Opts.Env, "GOOS="+runtime.GOOS) && slices.Contains(call.Opts.Env, "GOARCH="+runtime.GOARCH) &&
			slices.ContainsFunc(call.Opts.Env, func(entry string) bool { return strings.HasPrefix(entry, "GOBIN=") }) && call.Opts.CaptureCombined
	}, runner.Result{}, nil)
	for _, platform := range DefaultDeadcodePlatforms {
		fake.Expect(func(call runnertest.Call) bool {
			return strings.HasPrefix(filepath.Base(call.Name), "deadcode") && slices.Contains(call.Opts.Env, "GOOS="+platform)
		}, runner.Result{Stdout: "[]"}, nil)
	}
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Runner: fake})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(report.Platforms, ","), strings.Join(DefaultDeadcodePlatforms, ","); got != want {
		t.Fatalf("platforms = %q, want %q", got, want)
	}
	if got := fake.CallCount(); got != 1+len(DefaultDeadcodePlatforms) {
		t.Fatalf("calls = %d, want one install plus one analysis per platform", got)
	}
}

func TestDeadcodeFailsWhenTheAnalyzerCannotBeInstalled(t *testing.T) {
	t.Parallel()
	for name, result := range map[string]runner.Result{
		"combined output": {CombinedOutput: "module lookup failed\n"},
		"split streams":   {Stdout: "module ", Stderr: "lookup failed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			fake.Expect(isGoInstall, result, errors.New("exit status 1"))
			_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Runner: fake})
			if err == nil || !strings.Contains(err.Error(), "install deadcode analyzer") || !strings.Contains(err.Error(), "lookup failed") {
				t.Fatalf("error = %v, want the install failure with the tool's output", err)
			}
		})
	}
}

func TestDeadcodeUsesTheNamedGoCommandToInstall(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool { return call.Name == "/opt/go/bin/go" && call.Args[0] == "install" }, runner.Result{}, errors.New("stop here"))
	if _, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{Runner: fake, GoCommand: "/opt/go/bin/go"}); err == nil {
		t.Fatal("want the scripted install failure")
	}
}

func TestInstallDeadcodeToolReportsAnUnusableInstallDirectory(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	_, _, err := installDeadcodeTool(context.Background(), runnertest.New(t), t.TempDir(), "", missing)
	if err == nil || !strings.Contains(err.Error(), "create deadcode analyzer directory") {
		t.Fatalf("error = %v, want the install directory failure", err)
	}
}

func TestDeadcodeBinaryNameCarriesTheWindowsSuffix(t *testing.T) {
	t.Parallel()
	if got := deadcodeBinaryName("windows"); got != "deadcode.exe" {
		t.Fatalf("windows binary = %q", got)
	}
	if got := deadcodeBinaryName("linux"); got != "deadcode" {
		t.Fatalf("linux binary = %q", got)
	}
}

// A function newly dead on every supported platform and absent from the
// baseline fails the gate; dead on only two of three, it does not.
func TestDeadcodeGateFailsOnANewFunctionDeadOnAllThreePlatformsOnly(t *testing.T) {
	t.Parallel()
	known := deadFunctionJSON("example.com/m/old", "Known")
	fresh := deadFunctionJSON("example.com/m/new", "Fresh")
	baseline := filepath.Join(t.TempDir(), "baseline.txt")
	if err := WriteDeadcodeBaseline(baseline, []DeadcodeFinding{{Identity: "example.com/m/old.Known"}}); err != nil {
		t.Fatal(err)
	}
	run := func(linux, darwin, windows string) DeadcodeReport {
		t.Helper()
		fake := runnertest.New(t)
		expectPlatformRun(fake, "analyzer", "linux", linux)
		expectPlatformRun(fake, "analyzer", "darwin", darwin)
		expectPlatformRun(fake, "analyzer", "windows", windows)
		report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
			Tool: []string{"analyzer"}, Platforms: DefaultDeadcodePlatforms, Runner: fake, BaselinePath: baseline,
		})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	all := "[" + known + "," + fresh + "]"
	if report := run(all, all, all); len(report.New) != 1 || report.New[0].Identity != "example.com/m/new.Fresh" {
		t.Fatalf("dead on all three: new = %#v, want example.com/m/new.Fresh", report.New)
	}
	two := "[" + known + "]"
	if report := run(all, all, two); len(report.New) != 0 {
		t.Fatalf("dead on two of three: new = %#v, want none", report.New)
	}
}
