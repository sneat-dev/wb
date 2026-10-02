package quality

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// platformAnalyzer is a shell analyzer that reports a different dead set per
// GOOS, the way the real analyzer does when a file is built for one platform.
func platformAnalyzer(t *testing.T, bodyByPlatform map[string]string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell analyzer fixture requires POSIX")
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\ncase \"$GOOS/$GOARCH/$CGO_ENABLED\" in\n")
	for platform, body := range bodyByPlatform {
		script.WriteString(platform + "/amd64/0) cat <<'EOF'\n" + body + "\nEOF\n;;\n")
	}
	script.WriteString("*) echo \"unexpected environment $GOOS/$GOARCH/$CGO_ENABLED\" >&2; exit 2;;\nesac\n")
	path := filepath.Join(t.TempDir(), "platform-analyzer")
	if err := testenv.WriteExecutableFile(path, []byte(script.String()), 0o755); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	return []string{path}
}

func deadFunctionJSON(pkg, name string) string {
	return `{"Name":"p","Path":"` + pkg + `","Funcs":[{"Name":"` + name + `","Position":{"File":"f.go","Line":1,"Col":1}}]}`
}

// TestDeadcodeReportsOnlyFunctionsDeadOnEveryPlatform proves the verdict no
// longer depends on the host: a function dead on one platform but reachable
// on another (a Linux-only procfs reader) is not a finding anywhere.
func TestDeadcodeReportsOnlyFunctionsDeadOnEveryPlatform(t *testing.T) {
	t.Parallel()
	shared := deadFunctionJSON("example.com/m/shared", "Dead")
	linuxOnlyCaller := deadFunctionJSON("example.com/m/procfs", "Read")
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:      platformAnalyzer(t, map[string]string{"linux": "[" + shared + "]", "darwin": "[" + shared + "," + linuxOnlyCaller + "]", "windows": "[" + shared + "]"}),
		Platforms: []string{"linux", "darwin", "windows"},
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
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:      platformAnalyzer(t, map[string]string{"linux": "[" + dead + "," + dead + "]", "darwin": "[" + dead + "]"}),
		Platforms: []string{"linux", "darwin"},
	})
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("duplicate identity = %#v, %v; want one finding", report.Findings, err)
	}
	report, err = Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:      platformAnalyzer(t, map[string]string{"linux": "[" + dead + "]", "darwin": "[" + deadFunctionJSON("example.com/m/b", "G") + "]"}),
		Platforms: []string{"linux", "darwin"},
	})
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("disjoint platforms = %#v, %v; want no findings", report.Findings, err)
	}
}

// TestDeadcodeNamesThePlatformWhoseAnalysisFailed keeps a failing platform a
// failed gate instead of a silently smaller intersection.
func TestDeadcodeNamesThePlatformWhoseAnalysisFailed(t *testing.T) {
	t.Parallel()
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		Tool:      platformAnalyzer(t, map[string]string{"linux": "[]"}),
		Platforms: []string{"linux", "plan9"},
	})
	if err == nil || !strings.Contains(err.Error(), "platform plan9") || !strings.Contains(err.Error(), "unexpected environment plan9/amd64/0") {
		t.Fatalf("error = %v, want it to name platform plan9 and the analyzer's complaint", err)
	}
}

// stubGoInstaller writes a fake `go` that implements only `go install`: it
// checks the host pinning and writes the analyzer binary to GOBIN.
func stubGoInstaller(t *testing.T, analyzerJSON string, installExit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell go fixture requires POSIX")
	}
	script := "#!/bin/sh\n" +
		"[ \"$1\" = install ] || { echo \"unexpected go subcommand $1\" >&2; exit 2; }\n" +
		"[ \"$2\" = golang.org/x/tools/cmd/deadcode@v0.50.0 ] || { echo \"unexpected package $2\" >&2; exit 2; }\n" +
		"[ \"$GOOS\" = \"" + runtime.GOOS + "\" ] || { echo \"install not pinned to the host: $GOOS\" >&2; exit 2; }\n" +
		"[ -n \"$GOBIN\" ] || { echo missing GOBIN >&2; exit 2; }\n" +
		"[ " + itoaTest(installExit) + " -eq 0 ] || { echo 'install failed' >&2; exit " + itoaTest(installExit) + "; }\n" +
		"printf '#!/bin/sh\\ncat <<'\"'\"'EOF'\"'\"'\\n%s\\nEOF\\n' '" + analyzerJSON + "' > \"$GOBIN/deadcode\"\n" +
		"chmod +x \"$GOBIN/deadcode\"\n"
	path := filepath.Join(t.TempDir(), "go")
	if err := testenv.WriteExecutableFile(path, []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	return path
}

// TestDeadcodeInstallsThePinnedAnalyzerOnceAndRunsItPerDefaultPlatform covers
// the production path: no Tool override means the pinned analyzer is installed
// for the host and run for linux, darwin and windows.
func TestDeadcodeInstallsThePinnedAnalyzerOnceAndRunsItPerDefaultPlatform(t *testing.T) {
	t.Parallel()
	report, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{
		GoCommand: stubGoInstaller(t, "[]", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(report.Platforms, ","), strings.Join(DefaultDeadcodePlatforms, ","); got != want {
		t.Fatalf("platforms = %q, want %q", got, want)
	}
}

func TestDeadcodeFailsWhenTheAnalyzerCannotBeInstalled(t *testing.T) {
	t.Parallel()
	_, err := Deadcode(context.Background(), t.TempDir(), DeadcodeOptions{GoCommand: stubGoInstaller(t, "[]", 3)})
	if err == nil || !strings.Contains(err.Error(), "install deadcode analyzer") || !strings.Contains(err.Error(), "install failed") {
		t.Fatalf("error = %v, want the install failure with the tool's output", err)
	}
}

func TestInstallDeadcodeToolReportsAnUnusableInstallDirectory(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	_, _, err := installDeadcodeTool(context.Background(), t.TempDir(), "", missing)
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
