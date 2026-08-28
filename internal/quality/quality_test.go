package quality

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCoverAggregatesGoStatements(t *testing.T) {
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/coverage\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(repository, "coverage.go"), "package coverage\n\nfunc Covered() int { return 1 }\nfunc Uncovered() int { return 2 }\n")
	writeQualityFile(t, filepath.Join(repository, "coverage_test.go"), "package coverage\n\nimport \"testing\"\n\nfunc TestCovered(t *testing.T) { if Covered() != 1 { t.Fatal(\"unexpected\") } }\n")

	var progress []Progress
	report := CoverWithOptions(context.Background(), "example/coverage", repository, RunOptions{Progress: func(event Progress) {
		progress = append(progress, event)
	}})
	if report.Status != StatusPassed {
		t.Fatalf("status = %s: %s", report.Status, report.Error)
	}
	if len(report.Modules) != 1 || report.Statements == 0 || report.Covered == 0 || report.Covered >= report.Statements {
		t.Fatalf("coverage = %+v", report)
	}
	combined := NewCoverageReport([]RepositoryCoverage{report})
	if combined.Statements != report.Statements || combined.Percentage != report.Percentage {
		t.Fatalf("combined report = %+v", combined)
	}
	if len(progress) != 2 || progress[0].State != ProgressStarted || progress[1].State != ProgressCompleted || progress[1].Status != StatusPassed {
		t.Fatalf("coverage progress = %+v", progress)
	}
}

func TestProfileTotals(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "coverage.out")
	writeQualityFile(t, profile, "mode: set\nexample.go:1.1,1.2 3 1\nexample.go:2.1,2.2 2 0\n")
	statements, covered, err := profileTotals(profile)
	if err != nil {
		t.Fatal(err)
	}
	if statements != 5 || covered != 3 || percent(covered, statements) != 60 {
		t.Fatalf("totals = %d/%d (%.2f%%)", covered, statements, percent(covered, statements))
	}
}

func TestVerifyRunsNodeScriptsWithDetectedPackageManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"lint":"x","test":"x","build":"x"}}`)
	writeQualityFile(t, filepath.Join(repository, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var progress []Progress
	report := VerifyWithOptions(context.Background(), "example/node", repository, []Check{CheckLint, CheckTest, CheckBuild}, RunOptions{Progress: func(event Progress) {
		progress = append(progress, event)
	}})
	if report.Status != StatusPassed || len(report.Results) != 3 {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(contents)), "run lint\nrun test\nrun build"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if len(progress) != 6 {
		t.Fatalf("verification progress events = %d, want 6: %+v", len(progress), progress)
	}
	for index, event := range progress {
		want := ProgressStarted
		if index%2 == 1 {
			want = ProgressCompleted
		}
		if event.State != want {
			t.Fatalf("verification progress event %d state = %s, want %s", index, event.State, want)
		}
	}
}

func TestVerifySpecScoreConfiguration(t *testing.T) {
	t.Run("configured missing root fails closed", func(t *testing.T) {
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "specscore.yaml"), "project:\n  slug: example\n")

		report := Verify(context.Background(), "example/configured-spec", repository, []Check{CheckSpec})
		if report.Status != StatusFailed || len(report.Results) != 1 {
			t.Fatalf("report = %+v", report)
		}
		result := report.Results[0]
		if result.Status != StatusFailed || !strings.Contains(result.Detail, "specscore.yaml") || !strings.Contains(result.Detail, "spec") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("unconfigured missing root remains non-applicable", func(t *testing.T) {
		repository := t.TempDir()

		report := Verify(context.Background(), "example/no-spec", repository, []Check{CheckSpec})
		if report.Status != StatusPassed || len(report.Results) != 1 {
			t.Fatalf("report = %+v", report)
		}
		if result := report.Results[0]; result.Status != StatusSkipped {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("existing root runs lint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "specscore.yaml"), "project:\n  slug: example\n")
		if err := os.MkdirAll(filepath.Join(repository, "spec"), 0o755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(repository, "commands.log")
		writeQualityFile(t, filepath.Join(bin, "specscore"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
		if err := os.Chmod(filepath.Join(bin, "specscore"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		report := Verify(context.Background(), "example/spec", repository, []Check{CheckSpec})
		if report.Status != StatusPassed || len(report.Results) != 1 || report.Results[0].Status != StatusPassed {
			t.Fatalf("report = %+v", report)
		}
		contents, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.TrimSpace(string(contents)), "spec lint"; got != want {
			t.Fatalf("command = %q, want %q", got, want)
		}
	})
}

func TestParseChecks(t *testing.T) {
	checks, err := ParseChecks("test,lint,test")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(checkStrings(checks), ","), "test,lint"; got != want {
		t.Fatalf("checks = %s, want %s", got, want)
	}
	if _, err := ParseChecks("format"); err == nil {
		t.Fatal("unknown check should fail")
	}
}

func TestCommandErrorRetainsFailureTailWhenOutputIsLong(t *testing.T) {
	prefix := "setup context\n"
	middle := strings.Repeat("passing package output\n", 100)
	failure := "--- FAIL: TestImportantJourney (15.14s)\n    journey_test.go:42: exact failure\nFAIL"

	detail := commandError("go test ./...", prefix+middle+failure, context.DeadlineExceeded)

	if !strings.Contains(detail, prefix) {
		t.Fatalf("detail lost initial command context: %q", detail)
	}
	if !strings.Contains(detail, failure) {
		t.Fatalf("detail lost terminal failure: %q", detail)
	}
	if !strings.Contains(detail, "truncated") {
		t.Fatalf("detail does not disclose truncation: %q", detail)
	}
}

func TestRunWithOptionsRetriesAndTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	dir := t.TempDir()
	countPath := filepath.Join(dir, "attempts")
	retryTool := filepath.Join(dir, "retry-tool")
	writeQualityFile(t, retryTool, "#!/bin/sh\ncount=0\nif [ -f \""+countPath+"\" ]; then count=$(cat \""+countPath+"\"); fi\ncount=$((count + 1))\nprintf '%s' \"$count\" > \""+countPath+"\"\nif [ \"$count\" -lt 2 ]; then exit 1; fi\n")
	if err := os.Chmod(retryTool, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, attempts, err := runWithOptions(context.Background(), RunOptions{Retry: 1}, dir, retryTool); err != nil || attempts != 2 {
		t.Fatalf("retry result = err %v, attempts %d", err, attempts)
	}
	timeoutTool := filepath.Join(dir, "timeout-tool")
	writeQualityFile(t, timeoutTool, "#!/bin/sh\nsleep 1\n")
	if err := os.Chmod(timeoutTool, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, attempts, err := runWithOptions(context.Background(), RunOptions{Timeout: 10 * time.Millisecond}, dir, timeoutTool); err == nil || attempts != 1 || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout result = err %v, attempts %d", err, attempts)
	}
}

func checkStrings(checks []Check) []string {
	values := make([]string, len(checks))
	for index, check := range checks {
		values[index] = string(check)
	}
	return values
}

func writeQualityFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
