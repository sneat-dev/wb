package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const redBaseFailingPackageOutput = `--- FAIL: TestBroken (0.00s)
    a_test.go:4: broken at base
    --- FAIL: TestBroken/case (0.00s)
FAIL
coverage: 50.0% of statements
FAIL	example.test/red/a	0.123s
`

func TestRedBaseFailedTestsAcceptsOnlyPackagesThatWroteCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		output string
		want   []string
		ok     bool
	}{
		{
			name:   "failed tests with a coverage line are named per package",
			output: "ok  \texample.test/red/b\t0.1s\tcoverage: 100.0% of statements\n" + redBaseFailingPackageOutput + "?   \texample.test/red/c\t[no test files]\nFAIL\n",
			want:   []string{"example.test/red/a.TestBroken", "example.test/red/a.TestBroken/case"},
			ok:     true,
		},
		{
			name:   "windows line endings",
			output: strings.ReplaceAll(redBaseFailingPackageOutput, "\n", "\r\n"),
			want:   []string{"example.test/red/a.TestBroken", "example.test/red/a.TestBroken/case"},
			ok:     true,
		},
		{
			name:   "a build failure is never tolerated",
			output: redBaseFailingPackageOutput + "# example.test/red/b\n./b.go:3:1: syntax error\nFAIL\texample.test/red/b [build failed]\nFAIL\n",
		},
		{
			name:   "a test binary that died before writing coverage is never tolerated",
			output: "--- FAIL: TestPanics (0.00s)\npanic: boom\nFAIL\texample.test/red/a\t0.1s\nFAIL\n",
		},
		{
			name:   "a failed package that names no failed test is never tolerated",
			output: "FAIL\ncoverage: 10.0% of statements\nFAIL\texample.test/red/a\t0.1s\nFAIL\n",
		},
		{
			name:   "a passing package does not lend its coverage line to the next one",
			output: "coverage: 100.0% of statements\nok  \texample.test/red/b\t0.1s\n--- FAIL: TestBroken (0.00s)\nFAIL\texample.test/red/a\t0.1s\n",
		},
		{
			name:   "a package without tests does not lend its coverage line to the next one",
			output: "coverage: 0.0% of statements\n?   \texample.test/red/c\t[no test files]\n--- FAIL: TestBroken (0.00s)\nFAIL\texample.test/red/a\t0.1s\n",
		},
		{
			name:   "output without any failed package",
			output: "ok  \texample.test/red/b\t0.1s\tcoverage: 100.0% of statements\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := redBaseFailedTests(tc.output)
			if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("redBaseFailedTests = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRedBaseRecorderAcceptsOnlyAPlainTestFailureWithAReadableProfile(t *testing.T) {
	t.Parallel()
	profile := filepath.Join(t.TempDir(), "base.cov")
	writeCoverageFixture(t, profile, "mode: set\nexample.test/red/a/a.go:1.1,2.2 1 1\n")
	missing := filepath.Join(t.TempDir(), "missing.cov")

	var disabled *redBaseRecorder
	if disabled.accept(1, redBaseFailingPackageOutput, profile) {
		t.Fatal("a measurement without a recorder (the PR head) tolerated a failing test")
	}
	recorder := &redBaseRecorder{}
	if recorder.accept(-1, redBaseFailingPackageOutput, profile) {
		t.Fatal("a timed out or killed command was tolerated")
	}
	if recorder.accept(1, "FAIL\texample.test/red/a [build failed]\n", profile) {
		t.Fatal("a build failure was tolerated")
	}
	if recorder.accept(1, redBaseFailingPackageOutput, missing) {
		t.Fatal("a failure that produced no coverage profile was tolerated")
	}
	if got := recorder.baseline("abc123"); got != nil {
		t.Fatalf("baseline = %+v, want nil while nothing was tolerated", got)
	}
	for range 2 {
		if !recorder.accept(1, redBaseFailingPackageOutput, profile) {
			t.Fatal("a plain test failure with a coverage profile was rejected")
		}
	}
	want := &RedBaseline{SHA: "abc123", FailedTests: []string{"example.test/red/a.TestBroken", "example.test/red/a.TestBroken/case"}}
	if got := recorder.baseline("abc123"); !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline = %+v, want %+v (sorted, each test once)", got, want)
	}
}

func TestGoTestExitCodeTreatsANonExitErrorAsNotAPlainFailure(t *testing.T) {
	t.Parallel()
	if got := goTestExitCode(errors.New("timed out after 1s")); got != -1 {
		t.Fatalf("goTestExitCode = %d, want -1", got)
	}
}

const redBaseFixtureSource = `package a

func Covered() int { return 1 }

func OnlyReachedByTheFailingTest() int { return 2 }
`

const redBaseFixtureTests = `package a

import "testing"

func TestCovered(t *testing.T) {
	if Covered() != 1 {
		t.Fatal("covered")
	}
}

func TestBrokenAtBase(t *testing.T) { t.Fatal("broken at base") }
`

func writeRedBaseFixtureModule(t *testing.T) string {
	t.Helper()
	module := t.TempDir()
	writeCoverageFixture(t, filepath.Join(module, "go.mod"), "module example.test/red\n\ngo 1.24\n")
	writeGoShardFixturePackage(t, module, "a", redBaseFixtureSource, redBaseFixtureTests)
	writeGoShardFixturePackage(t, module, "b", "package b\n\nfunc Value() int { return 1 }\n", "package b\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 1 {\n\t\tt.Fatal(\"value\")\n\t}\n}\n")
	return module
}

func TestRunCoverageMeasuresARedBaseButStillFailsARedHead(t *testing.T) {
	t.Parallel()
	module := writeRedBaseFixtureModule(t)

	headProfile := filepath.Join(t.TempDir(), "head.cov")
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{}, module, headProfile); err == nil {
		t.Fatal("a failing test at the PR head did not fail the coverage measurement")
	}

	recorder := &redBaseRecorder{}
	baseProfile := filepath.Join(t.TempDir(), "base.cov")
	output, _, err := runCoverageWithOptions(context.Background(), RunOptions{redBase: recorder}, module, baseProfile)
	if err != nil {
		t.Fatalf("red base was not measured: %v\n%s", err, output)
	}
	want := &RedBaseline{SHA: "base", FailedTests: []string{"example.test/red/a.TestBrokenAtBase"}}
	if got := recorder.baseline("base"); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded = %+v, want %+v\n%s", got, want, output)
	}
	blocks, err := ParseCoverageProfile(baseProfile)
	if err != nil {
		t.Fatal(err)
	}
	baseline := BaselineFromProfile(blocks, "example.test/red", "base")
	if baseline.Packages["a"] != 1 || baseline.Packages["b"] != 0 {
		t.Fatalf("packages = %v, want a's one statement only the failing test would reach counted as uncovered and b fully covered", baseline.Packages)
	}
}

func TestRunCoverageMeasuresARedBaseAcrossShardsAndTheNativeTier(t *testing.T) {
	t.Parallel()
	module := writeRedBaseFixtureModule(t)
	writeCoverageFixture(t, filepath.Join(module, "a", "a_native_test.go"), "//go:build e2e\n\npackage a\n\nimport \"testing\"\n\nfunc TestE2EBrokenAtBase(t *testing.T) { t.Fatal(\"native journey broken at base\") }\n")
	options := RunOptions{IncludeE2E: true, GoTestShards: 2, GoShardPackages: []string{"./a"}}

	if _, _, err := runCoverageWithOptions(context.Background(), options, module, filepath.Join(t.TempDir(), "head.cov")); err == nil {
		t.Fatal("a failing shard at the PR head did not fail the coverage measurement")
	}

	recorder := &redBaseRecorder{}
	options.redBase = recorder
	profile := filepath.Join(t.TempDir(), "base.cov")
	output, _, err := runCoverageWithOptions(context.Background(), options, module, profile)
	if err != nil {
		t.Fatalf("red base was not measured: %v\n%s", err, output)
	}
	want := &RedBaseline{SHA: "base", FailedTests: []string{"example.test/red/a.TestBrokenAtBase", "example.test/red/a.TestE2EBrokenAtBase"}}
	if got := recorder.baseline("base"); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded = %+v, want %+v\n%s", got, want, output)
	}
	if _, err := ParseCoverageProfile(profile); err != nil {
		t.Fatalf("merged base profile: %v", err)
	}
}

func TestRunCoverageStillFailsABaseThatCannotBeMeasured(t *testing.T) {
	t.Parallel()
	for name, brokenTests := range map[string]string{
		"test binary does not build":           "package b\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { undefined() }\n",
		"test binary exits before its profile": "package b\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestValue(t *testing.T) { os.Exit(1) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			module := writeRedBaseFixtureModule(t)
			if err := os.WriteFile(filepath.Join(module, "b", "b_test.go"), []byte(brokenTests), 0o600); err != nil {
				t.Fatal(err)
			}
			recorder := &redBaseRecorder{}
			output, _, err := runCoverageWithOptions(context.Background(), RunOptions{redBase: recorder}, module, filepath.Join(t.TempDir(), "base.cov"))
			if err == nil {
				t.Fatalf("an unmeasurable base passed:\n%s", output)
			}
			if got := recorder.baseline("base"); got != nil {
				t.Fatalf("recorded = %+v, want nothing tolerated", got)
			}
		})
	}
}
