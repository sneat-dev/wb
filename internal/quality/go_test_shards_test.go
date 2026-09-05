package quality

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRunShardedCoverageRunsEveryTestOnceAndMergesProfile(t *testing.T) {
	module := t.TempDir()
	logPath := filepath.Join(module, "runs.log")
	t.Setenv("WB_SHARD_TEST_LOG", logPath)
	writeCoverageFixture(t, filepath.Join(module, "go.mod"), "module example.test/shards\n\ngo 1.24\n")
	writeGoShardFixturePackage(t, module, "ordinary", `package ordinary
func Covered() int { return 1 }
`, `package ordinary
import ("os"; "testing")
func TestOrdinary(t *testing.T) { if Covered() != 1 { t.Fatal("covered") }; record(t, "ordinary") }
func record(t *testing.T, name string) { t.Helper(); f, err := os.OpenFile(os.Getenv("WB_SHARD_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if err != nil { t.Fatal(err) }; defer f.Close(); if _, err := f.WriteString(name+"\n"); err != nil { t.Fatal(err) } }
`)
	writeGoShardFixturePackage(t, module, "serial", `package serial
func Alpha() int { return 1 }
func Beta() int { return 2 }
`, `package serial
import ("os"; "testing")
func TestAlpha(t *testing.T) { if Alpha() != 1 { t.Fatal("alpha") }; record(t, "alpha") }
func TestBeta(t *testing.T) { if Beta() != 2 { t.Fatal("beta") }; record(t, "beta") }
func record(t *testing.T, name string) { t.Helper(); f, err := os.OpenFile(os.Getenv("WB_SHARD_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if err != nil { t.Fatal(err) }; defer f.Close(); if _, err := f.WriteString(name+"\n"); err != nil { t.Fatal(err) } }
`)
	profile := filepath.Join(module, "merged.cov")
	if output, err := runShardedCoverage(context.Background(), module, profile, []string{"./serial"}, 2); err != nil {
		t.Fatalf("run sharded coverage: %v\n%s", err, output)
	}
	rawLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	runs := strings.Fields(string(rawLog))
	sort.Strings(runs)
	wantRuns := []string{"alpha", "beta", "ordinary"}
	if !reflect.DeepEqual(runs, wantRuns) {
		t.Fatalf("test runs = %v, want every test exactly once: %v", runs, wantRuns)
	}
	statements, covered, err := profileTotals(profile)
	if err != nil {
		t.Fatal(err)
	}
	if statements == 0 || covered == 0 {
		t.Fatalf("merged coverage totals = %d/%d, want non-zero union", covered, statements)
	}
}

func TestVerifyWithRepositoryPolicyUsesShardedGoTest(t *testing.T) {
	module := t.TempDir()
	writeCoverageFixture(t, filepath.Join(module, "go.mod"), "module example.test/verify-shards\n\ngo 1.24\n")
	writeGoShardFixturePackage(t, module, "serial", `package serial
func Value() int { return 1 }
`, `package serial
import "testing"
func TestAlpha(t *testing.T) { if Value() != 1 { t.Fatal("value") } }
func TestBeta(t *testing.T) { if Value() != 1 { t.Fatal("value") } }
`)
	policyPath := filepath.Join(module, repositoryQualityConfigPath)
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCoverageFixture(t, policyPath, "version: 1\ngo_test:\n  shards: 2\n  packages: [./serial]\n")
	options, err := RepositoryRunOptions(module, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var progress []Progress
	options.Progress = func(event Progress) { progress = append(progress, event) }
	report := VerifyWithOptions(context.Background(), "verify-shards", module, []Check{CheckTest}, options)
	if report.Status != StatusPassed || len(report.Results) != 1 {
		t.Fatalf("sharded verification = %+v", report)
	}
	if !strings.Contains(report.Results[0].Command, "2 process-isolated shards") {
		t.Fatalf("verification command did not record sharding: %+v", report.Results[0])
	}
	jobEvents := make([]Progress, 0, len(progress))
	for _, event := range progress {
		if event.Total > 0 {
			jobEvents = append(jobEvents, event)
		}
	}
	if len(jobEvents) != 4 {
		t.Fatalf("job progress events = %+v, want start and completion for two shards", jobEvents)
	}
	completed := 0
	for _, event := range jobEvents {
		if event.Detail == "" || event.Total != 2 {
			t.Fatalf("incomplete job progress = %+v", event)
		}
		if event.State == ProgressCompleted {
			completed++
			if event.Status != StatusPassed || event.Completed < 1 {
				t.Fatalf("completed job progress = %+v", event)
			}
		}
	}
	if completed != 2 {
		t.Fatalf("completed job events = %d, want 2", completed)
	}
}

func TestRunShardedCoverageRetainsOnlyFailedShardDiagnostic(t *testing.T) {
	module := t.TempDir()
	writeCoverageFixture(t, filepath.Join(module, "go.mod"), "module example.test/failure\n\ngo 1.24\n")
	writeGoShardFixturePackage(t, module, "serial", `package serial
func Value() int { return 1 }
`, `package serial
import "testing"
func TestAlphaPasses(t *testing.T) { if Value() != 1 { t.Fatal("value") } }
func TestBetaFails(t *testing.T) { t.Fatal("terminal-shard-diagnostic") }
`)
	output, err := runShardedCoverage(context.Background(), module, filepath.Join(module, "unused.cov"), []string{"./serial"}, 2)
	if err == nil {
		t.Fatal("failing shard was accepted")
	}
	if !strings.Contains(output, "terminal-shard-diagnostic") {
		t.Fatalf("failed shard output lost its terminal diagnostic:\n%s", output)
	}
	if strings.Contains(output, "PASS: TestAlphaPasses") || strings.Contains(output, "ok  \texample.test/failure/serial") {
		t.Fatalf("successful shard output displaced the failure diagnostic:\n%s", output)
	}
}

func TestPlanGoTestShardsIsDeterministicCompleteAndUnique(t *testing.T) {
	tests := []string{"TestZulu", "TestAlpha", "ExampleUsage", "FuzzDecode", "TestMiddle"}
	want := [][]string{
		{"ExampleUsage", "TestMiddle"},
		{"FuzzDecode", "TestZulu"},
		{"TestAlpha"},
	}

	first, err := planGoTestShards(tests, 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planGoTestShards([]string{"TestMiddle", "FuzzDecode", "TestAlpha", "TestZulu", "ExampleUsage"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("shards = %#v, want %#v", first, want)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("same names in another discovery order planned differently: %#v != %#v", second, first)
	}

	seen := map[string]int{}
	for _, shard := range first {
		for _, name := range shard {
			seen[name]++
		}
	}
	for _, name := range tests {
		if seen[name] != 1 {
			t.Fatalf("%s appeared %d times, want exactly once", name, seen[name])
		}
	}
}

func TestPlanGoTestShardsRejectsUnsafeInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		tests  []string
		shards int
		want   string
	}{
		{name: "no shards", tests: []string{"TestOne"}, shards: 0, want: "at least 1"},
		{name: "no tests", shards: 2, want: "no tests"},
		{name: "duplicate", tests: []string{"TestOne", "TestOne"}, shards: 2, want: "duplicate"},
		{name: "invalid name", tests: []string{"TestOne", "not-a-test"}, shards: 2, want: "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := planGoTestShards(test.tests, test.shards)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMergeCoverageProfilesPreservesUnionAndStatementIdentity(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.cov")
	second := filepath.Join(directory, "second.cov")
	output := filepath.Join(directory, "merged.cov")
	writeCoverageFixture(t, first, `mode: set
example/a.go:1.1,2.2 2 1
example/a.go:4.1,5.2 3 0
`)
	writeCoverageFixture(t, second, `mode: set
example/a.go:1.1,2.2 2 0
example/a.go:4.1,5.2 3 1
`)

	if err := mergeCoverageProfiles([]string{second, first}, output); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := `mode: set
example/a.go:1.1,2.2 2 1
example/a.go:4.1,5.2 3 1
`
	if string(contents) != want {
		t.Fatalf("merged profile:\n%s\nwant:\n%s", contents, want)
	}
	statements, covered, err := profileTotals(output)
	if err != nil {
		t.Fatal(err)
	}
	if statements != 5 || covered != 5 {
		t.Fatalf("merged totals = %d/%d, want 5/5", covered, statements)
	}
}

func TestMergeCoverageProfilesSumsCountMode(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.cov")
	second := filepath.Join(directory, "second.cov")
	output := filepath.Join(directory, "merged.cov")
	writeCoverageFixture(t, first, "mode: count\nexample/a.go:1.1,2.2 2 3\n")
	writeCoverageFixture(t, second, "mode: count\nexample/a.go:1.1,2.2 2 4\n")

	if err := mergeCoverageProfiles([]string{first, second}, output); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := "mode: count\nexample/a.go:1.1,2.2 2 7\n"
	if string(contents) != want {
		t.Fatalf("merged profile:\n%s\nwant:\n%s", contents, want)
	}
}

func TestMergeCoverageProfilesRejectsModeOrBlockMismatch(t *testing.T) {
	directory := t.TempDir()
	setProfile := filepath.Join(directory, "set.cov")
	countProfile := filepath.Join(directory, "count.cov")
	mismatchedBlock := filepath.Join(directory, "mismatched.cov")
	writeCoverageFixture(t, setProfile, "mode: set\nexample/a.go:1.1,2.2 2 1\n")
	writeCoverageFixture(t, countProfile, "mode: count\nexample/a.go:1.1,2.2 2 1\n")
	writeCoverageFixture(t, mismatchedBlock, "mode: set\nexample/a.go:1.1,2.2 3 1\n")

	for _, test := range []struct {
		name     string
		profiles []string
		want     string
	}{
		{name: "mode", profiles: []string{setProfile, countProfile}, want: "mode"},
		{name: "block", profiles: []string{setProfile, mismatchedBlock}, want: "block"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := mergeCoverageProfiles(test.profiles, filepath.Join(directory, test.name+".cov"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func writeCoverageFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGoShardFixturePackage(t *testing.T, module, name, source, tests string) {
	t.Helper()
	directory := filepath.Join(module, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCoverageFixture(t, filepath.Join(directory, name+".go"), source)
	writeCoverageFixture(t, filepath.Join(directory, name+"_test.go"), tests)
}
