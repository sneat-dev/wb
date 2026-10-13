package quality

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNativeShardingSelectsExactCompiledSetAndAggregatesFailures(t *testing.T) {
	if !qualityFixtureChild(t) {
		t.Parallel()
		runQualityFixtureChild(t)
		return
	}
	for _, fail := range []bool{false, true} {
		//nolint:paralleltest // Private reexec child owns its fake-Go environment.
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			module := t.TempDir()
			dqCovFakeGo(t, module)
			log := filepath.Join(module, "go.log")
			dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LOG": log, "DQCOV_GO_LIST_MAIN": "./ordinary\n./serial", "DQCOV_GO_LIST_OTHER": "@pattern", "DQCOV_GO_TEST_LIST": "TestUnit\nTestE2EZulu\nTestContractAlpha\nTestE2EBravo\nTestE2ECharlie\nTestE2EDelta\nTestE2ECafé\nTestContract日本語٢\nExampleSkipped", "DQCOV_GO_WRITE_PROFILE": "1"})
			if fail {
				t.Setenv("DQCOV_GO_TEST_FAIL", "1")
				t.Setenv("DQCOV_GO_TEST_FAIL_OUT", "--- FAIL: TestE2EFailure (0.00s)\n")
			}
			profile := filepath.Join(module, "merged.cov")
			output, attempts, err := runCoverageWithOptions(t.Context(), RunOptions{NativeOnly: true, Retry: 3, Timeout: 10 * time.Second, GoTestShards: 4, GoShardPackages: []string{"./serial"}}, module, profile)
			if (err != nil) != fail || attempts != 1 {
				t.Fatalf("result=%v attempts=%d output=%s", err, attempts, output)
			}
			raw, e := os.ReadFile(log)
			if e != nil {
				t.Fatal(e)
			}
			names := map[string]int{}
			executions := 0
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if strings.HasPrefix(line, "list ") {
					if !strings.Contains(line, "-tags=e2e") {
						t.Fatalf("untagged package discovery: %s", line)
					}
					continue
				}
				if !strings.Contains(line, "-tags=e2e") || !strings.Contains(line, "-count=1") {
					t.Fatalf("native argv lost: %s", line)
				}
				if strings.Contains(line, "-list ") {
					if !strings.HasSuffix(line, NativeGoTestSelector) {
						t.Fatalf("discovery selector: %s", line)
					}
					continue
				}
				executions++
				if !strings.Contains(line, "-timeout 10s") {
					t.Fatalf("attempt timeout: %s", line)
				}
				fields := strings.Fields(line)
				selector := ""
				for i, f := range fields {
					if f == "-run" {
						selector = fields[i+1]
					}
				}
				if strings.Contains(line, "./ordinary") {
					if selector != NativeGoTestSelector {
						t.Fatalf("unconfigured selector: %s", line)
					}
					continue
				}
				for _, name := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(selector, "^("), ")$"), "|") {
					names[name]++
				}
			}
			want := map[string]int{"TestContractAlpha": 1, "TestE2EBravo": 1, "TestE2ECharlie": 1, "TestE2EDelta": 1, "TestE2EZulu": 1, "TestE2ECafé": 1, "TestContract日本語٢": 1}
			if executions != 5 || !reflect.DeepEqual(names, want) {
				t.Fatalf("executions=%d partitions=%v want=%v", executions, names, want)
			}
			if fail {
				for _, label := range []string{"unsharded packages", "./serial shard 1/4", "./serial shard 2/4", "./serial shard 3/4", "./serial shard 4/4"} {
					if !strings.Contains(output, label) {
						t.Fatalf("missing failure %s: %s", label, output)
					}
				}
				if _, e := os.Stat(profile); !os.IsNotExist(e) {
					t.Fatalf("failed native run published profile: %v", e)
				}
			} else if _, _, e := readCoverageProfile(profile); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestNativeShardingKeepsEmptyConfiguredPackageAndOutsidePolicy(t *testing.T) {
	if !qualityFixtureChild(t) {
		t.Parallel()
		runQualityFixtureChild(t)
		return
	}
	module := t.TempDir()
	dqCovFakeGo(t, module)
	log := filepath.Join(module, "go.log")
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LOG": log, "DQCOV_GO_LIST_MAIN": "./empty", "DQCOV_GO_LIST_OTHER": "@pattern", "DQCOV_GO_TEST_LIST": "TestUnit", "DQCOV_GO_WRITE_PROFILE": "1"})
	options := RunOptions{NativeOnly: true, GoTestShards: 4, GoShardPackages: []string{"./outside", "./empty"}, configGoShardPackages: true}
	if out, _, err := runCoverageWithOptions(t.Context(), options, module, filepath.Join(module, "merged.cov")); err != nil {
		t.Fatalf("empty native package: %v %s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "-coverprofile=") {
			executions++
			if !strings.Contains(line, "./empty") || !strings.Contains(line, NativeGoTestSelector) {
				t.Fatalf("empty argv=%s", line)
			}
		}
	}
	if executions != 1 {
		t.Fatalf("empty package executions=%d log=%s", executions, raw)
	}
	options.ExplicitGoTestSharding = true
	if _, _, err := runCoverageWithOptions(t.Context(), options, module, filepath.Join(module, "explicit.cov")); err == nil || !strings.Contains(err.Error(), "outside selected package scope") {
		t.Fatalf("explicit outside policy=%v", err)
	}
}

func TestNativeShardArgumentModesAndLegacySelection(t *testing.T) {
	t.Parallel()
	native := nativeCoverageArguments("p", time.Minute, "atomic", NativeGoTestSelector, "./one")
	if !strings.Contains(strings.Join(native, " "), "-covermode=atomic") {
		t.Fatalf("coverage mode=%v", native)
	}
	legacy := goTestShardArguments("p", time.Minute, RunOptions{}, "./one", "^TestUnit$")
	if regexp.MustCompile(`e2e|count=1`).MatchString(strings.Join(legacy, " ")) {
		t.Fatalf("legacy changed=%v", legacy)
	}
}

func TestNativeVerificationSkipsNodeAndUsesSharedRunner(t *testing.T) {
	if !qualityFixtureChild(t) {
		t.Parallel()
		runQualityFixtureChild(t)
		return
	}
	module := t.TempDir()
	dqCovFakeGo(t, module)
	writeQualityFile(t, filepath.Join(module, "go.mod"), "module example.test/nativeverify\n\ngo 1.24\n")
	writeQualityFile(t, filepath.Join(module, "package.json"), `{"scripts":{"test":"false"}}`)
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_WRITE_PROFILE": "1"})
	report := VerifyWithOptions(t.Context(), "example/native", module, []Check{CheckTest}, RunOptions{NativeOnly: true})
	if report.Status != StatusPassed || len(report.Results) != 1 || report.Results[0].Language != "go" || !strings.Contains(report.Results[0].Command, NativeGoTestSelector) {
		t.Fatalf("native verification=%+v", report)
	}
}
