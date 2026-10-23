//go:build e2e

package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // the executable shim is selected through process-wide PATH.
func TestE2ECombinedCoverageMeasuresBothTiersAndPublishesOnlySuccess(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
 list)
  if [ "$#" -ne 4 ] || [ "$2" != -f ] || [ "$3" != '{{.ImportPath}}' ] || [ "$4" != './...' ]; then exit 2; fi
  printf 'example.test/app\n'
  exit 0;;
 test) ;;
 *) exit 2;;
esac
profile=''
native=false
for argument in "$@"; do
 case "$argument" in
  -coverprofile=*) profile="${argument#-coverprofile=}";;
  -tags=e2e) native=true;;
 esac
done
if [ "$WB_TEST_SCENARIO" = unit-failure ] && ! $native; then exit 1; fi
if [ "$WB_TEST_SCENARIO" = native-failure ] && $native; then exit 1; fi
if [ "$WB_TEST_SCENARIO" = native-timeout ] && $native; then exec sleep 60; fi
if [ "$WB_TEST_SCENARIO" = unit-timeout ] && ! $native; then exec sleep 60; fi
if [ "$WB_TEST_SCENARIO" = missing-profile ] && ! $native; then exit 0; fi
if [ "$WB_TEST_SCENARIO" = invalid-profile ] && ! $native; then printf 'invalid\n' > "$profile"; exit 0; fi
mode=atomic
if [ "$WB_TEST_SCENARIO" = mode-mismatch ] && $native; then mode=set; fi
count=0
if $native; then count=1; fi
printf 'mode: %%s\nexample.test/app/app.go:2.1,2.30 1 1\nexample.test/app/app.go:3.1,3.30 1 %%s\n' "$mode" "$count" > "$profile"
`, log)
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, scenario := range []string{"success", "unit-failure", "native-failure", "missing-profile", "invalid-profile", "mode-mismatch", "native-timeout", "unit-timeout", "caller-deadline"} {
		t.Run(scenario, func(t *testing.T) { //nolint:paralleltest // scenarios share the serial parent's PATH-selected shim and call log.
			profile := filepath.Join(root, scenario+".cov")
			original := []byte("previous complete generation")
			if err := os.WriteFile(profile, original, 0600); err != nil {
				t.Fatal(err)
			}
			options := RunOptions{IncludeE2E: true, Timeout: 30 * time.Second, Env: []string{"WB_TEST_SCENARIO=" + scenario}}
			ctx := context.Background()
			if strings.HasSuffix(scenario, "-timeout") {
				options.Timeout = 50 * time.Millisecond
			}
			if scenario == "caller-deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				options.Env = []string{"WB_TEST_SCENARIO=native-timeout"}
			}
			_, attempts, err := runCoverageWithOptions(ctx, options, root, profile)
			if scenario != "success" {
				if err == nil {
					t.Fatal("failed tier or profile accepted")
				}
				if (strings.HasSuffix(scenario, "-timeout") || scenario == "caller-deadline") && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost combined deadline: %v", err)
				}
				raw, readErr := os.ReadFile(profile)
				if readErr != nil || string(raw) != string(original) {
					t.Fatalf("published failed result: %q %v", raw, readErr)
				}
				return
			}
			if err != nil || attempts != 1 {
				t.Fatalf("combined coverage: attempts=%d err=%v", attempts, err)
			}
			statements, covered, err := profileTotals(profile)
			if err != nil || statements != 2 || covered != 2 {
				t.Fatalf("coverage=%d/%d err=%v", covered, statements, err)
			}
			blocks, err := ParseCoverageProfile(profile)
			if err != nil {
				t.Fatal(err)
			}
			baseline := BaselineFromProfile(blocks, "example.test/app", "same-checkout-sha")
			results, _ := EvaluateRatchet(blocks, ChangedLines{"app.go": {3: true}}, map[string]bool{"app.go": true}, nil, baseline, "example.test/app", nil)
			if len(results) != 1 || !results[0].Pass {
				t.Fatalf("actual profile union failed changed-line ratchet: %+v", results)
			}
		})
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "list ") {
			if line != "list -f {{.ImportPath}} ./..." {
				t.Fatalf("package resolution differs: %s", line)
			}
			continue
		}
		if !strings.HasPrefix(line, "test ") {
			t.Fatalf("unexpected Go operation: %s", line)
		}
		if !strings.Contains(line, "-coverpkg=./...") {
			t.Fatalf("instrumentation differs: %s", line)
		}
		if strings.Contains(line, "-tags=e2e") && (!strings.Contains(line, "-covermode=atomic") || !strings.Contains(line, "-run=^Test(E2E|Contract)")) {
			t.Fatalf("native mode/scope differs: %s", line)
		}
	}
	// Default measurement retains the original unit-only behavior.
	unitProfile := filepath.Join(root, "default.cov")
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{}, root, unitProfile); err != nil {
		t.Fatal(err)
	}
	statements, covered, err := profileTotals(unitProfile)
	if err != nil || statements != 2 || covered != 1 {
		t.Fatalf("default coverage=%d/%d err=%v", covered, statements, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runCoverageWithOptions(ctx, RunOptions{IncludeE2E: true}, root, filepath.Join(root, "canceled.cov")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestE2ECombinedCoverageUsesRealShardedProfilesAndMatchingBaselineTier(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("pkg/app.go", "package pkg\nfunc Unit() int { return 1 }\nfunc Native() int { return 2 }\n")
	repo.writeFile("pkg/app_test.go", "//go:build !e2e\n\npackage pkg\nimport \"testing\"\nfunc TestUnitOne(t *testing.T) { if Unit()!=1 {t.Fatal(\"unit\")} }\nfunc TestUnitTwo(t *testing.T) { if Unit()!=1 {t.Fatal(\"unit\")} }\n")
	repo.writeFile("pkg/native_test.go", "//go:build e2e\n\npackage pkg\nimport (\"fmt\"; \"os\"; \"os/exec\"; \"testing\")\nfunc recordNative(t *testing.T, name string) { t.Helper(); f,err:=os.OpenFile(os.Getenv(\"WB_NATIVE_TEST_LOG\"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if err!=nil {t.Fatal(err)}; if _,err=fmt.Fprintf(f,\"pkg/%s %d\\n\",name,os.Getpid());err!=nil {t.Fatal(err)}; if err=f.Close();err!=nil {t.Fatal(err)} }\nfunc TestE2ENativeOne(t *testing.T) { recordNative(t,\"TestE2ENativeOne\"); if err:=exec.Command(\"git\",\"--version\").Run();err!=nil {t.Fatal(err)}; if Native()!=2 {t.Fatal(\"native\")} }\nfunc TestContractNativeTwo(t *testing.T) { recordNative(t,\"TestContractNativeTwo\"); if Native()!=2 {t.Fatal(\"native\")} }\nfunc TestOrdinaryNative(t *testing.T) { recordNative(t,\"TestOrdinaryNative\") }\n")
	repo.writeFile("extra/app.go", "package extra\nfunc Extra() int { return 3 }\n")
	repo.writeFile("extra/app_test.go", "package extra\nimport \"testing\"\nfunc TestExtra(t *testing.T) { if Extra()!=3 {t.Fatal(\"extra\")} }\n")
	repo.writeFile("extra/native_test.go", "//go:build e2e\n\npackage extra\nimport (\"fmt\"; \"os\"; \"testing\")\nfunc TestE2EUnsharded(t *testing.T) { f,err:=os.OpenFile(os.Getenv(\"WB_NATIVE_TEST_LOG\"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if err!=nil {t.Fatal(err)}; if _,err=fmt.Fprintf(f,\"extra/TestE2EUnsharded %d\\n\",os.Getpid());err!=nil {t.Fatal(err)}; if err=f.Close();err!=nil {t.Fatal(err)}; if Extra()!=3 {t.Fatal(\"extra\")} }\n")
	repo.writeFile("nativeonly/native.go", "//go:build e2e\n\npackage nativeonly\nfunc Value() int { return 5 }\n")
	repo.writeFile("nativeonly/native_test.go", "//go:build e2e\n\npackage nativeonly\nimport (\"fmt\"; \"os\"; \"testing\")\nfunc TestE2EOnlyPackage(t *testing.T) { f,err:=os.OpenFile(os.Getenv(\"WB_NATIVE_TEST_LOG\"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if err!=nil {t.Fatal(err)}; if _,err=fmt.Fprintf(f,\"nativeonly/TestE2EOnlyPackage %d\\n\",os.Getpid());err!=nil {t.Fatal(err)}; if err=f.Close();err!=nil {t.Fatal(err)}; if err=os.WriteFile(os.Getenv(\"WB_NATIVE_SCOPE_LOG\"),[]byte(os.Getenv(\"WB_TEST_NATIVE_COVERPKG\")),0600);err!=nil {t.Fatal(err)}; if Value()!=5 {t.Fatal(\"native only\")} }\n")
	repo.writeFile("empty/app.go", "package empty\nfunc Empty() int { return 4 }\n")
	repo.writeFile("empty/app_test.go", "package empty\nimport \"testing\"\nfunc TestEmpty(t *testing.T) { if Empty()!=4 {t.Fatal(\"empty\")} }\n")
	repo.commitAll("combined coverage fixture")
	profile := filepath.Join(t.TempDir(), "combined.cov")
	nativeLog := filepath.Join(t.TempDir(), "native-tests.log")
	nativeScopeLog := filepath.Join(t.TempDir(), "native-coverpkg.txt")
	options := RunOptions{IncludeE2E: true, GoTestPackages: []string{"./..."}, GoShardPackages: []string{"./pkg", "./empty"}, GoTestShards: 2, Timeout: time.Minute, ShardAttemptTimeout: 45 * time.Second, CheckTimeout: 2 * time.Minute, Env: []string{"GOFLAGS=-coverpkg=example.invalid/foreign", "WB_NATIVE_TEST_LOG=" + nativeLog, "WB_NATIVE_SCOPE_LOG=" + nativeScopeLog}}
	if output, _, err := runCoverageWithOptions(context.Background(), options, repo.dir, profile); err != nil {
		t.Fatalf("real combined run: %v\n%s", err, output)
	}
	entries, err := os.ReadFile(nativeLog)
	if err != nil {
		t.Fatalf("read native test execution log: %v", err)
	}
	counts := map[string]int{}
	packagePIDs := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(entries)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed native test execution record %q", line)
		}
		counts[fields[0]]++
		if strings.HasPrefix(fields[0], "pkg/") {
			packagePIDs[fields[1]] = true
		}
	}
	for _, name := range []string{"pkg/TestE2ENativeOne", "pkg/TestContractNativeTwo", "extra/TestE2EUnsharded", "nativeonly/TestE2EOnlyPackage"} {
		if counts[name] != 1 {
			t.Errorf("native test %s ran %d times, want exactly once", name, counts[name])
		}
	}
	if counts["pkg/TestOrdinaryNative"] != 0 {
		t.Errorf("ordinary e2e-tagged test ran in native tier %d times", counts["pkg/TestOrdinaryNative"])
	}
	if len(packagePIDs) != 2 {
		t.Errorf("configured native tests used %d test processes, want two shards: %v", len(packagePIDs), packagePIDs)
	}
	nativeScope, err := os.ReadFile(nativeScopeLog)
	if err != nil {
		t.Fatalf("read native instrumentation package scope: %v", err)
	}
	if strings.Contains(string(nativeScope), "fixture.test/app/nativeonly") {
		t.Errorf("native child-process instrumentation scope %q includes an e2e-only package", nativeScope)
	}
	statements, covered, err := profileTotals(profile)
	if err != nil || statements != 5 || covered != 5 {
		t.Fatalf("real combined coverage=%d/%d: %v", covered, statements, err)
	}
	mode, _, err := readCoverageProfile(profile)
	if err != nil || mode != "set" {
		t.Fatalf("real combined coverage mode=%q: %v", mode, err)
	}
	baseline, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", 2*time.Minute, options)
	if err != nil || !baseline.IncludeE2E {
		t.Fatalf("combined merge-base measurement: %+v %v", baseline, err)
	}
	for name, uncovered := range baseline.Packages {
		if uncovered != 0 {
			t.Fatalf("baseline package %s lost native coverage: %d", name, uncovered)
		}
	}
	requestedShardLog := filepath.Join(t.TempDir(), "requested-native-shard.log")
	requestedShardScopeLog := filepath.Join(t.TempDir(), "requested-native-shard-coverpkg.txt")
	requestedShardProfile := filepath.Join(t.TempDir(), "requested-native-shard.cov")
	if output, _, err := runShardedCoverageWithTestOptions(context.Background(), nil, repo.dir, requestedShardProfile, []string{"./nativeonly"}, 2, "", "", time.Minute, 45*time.Second, 0, nil, []string{"WB_NATIVE_TEST_LOG=" + requestedShardLog, "WB_NATIVE_SCOPE_LOG=" + requestedShardScopeLog}, false, coverageTestOptions{
		tags: []string{"-tags=e2e"}, selector: NativeGoTestSelector, coverMode: mode, nativeOnly: true, countOnce: true,
	}, []string{"./nativeonly"}, []string{"./..."}); err != nil {
		t.Fatalf("requested e2e-only native shard: %v\n%s", err, output)
	}
	requestedShardEntries, err := os.ReadFile(requestedShardLog)
	if err != nil {
		t.Fatalf("read requested native shard execution log: %v", err)
	}
	if got := strings.TrimSpace(string(requestedShardEntries)); got == "" || len(strings.Split(got, "\n")) != 1 || !strings.HasPrefix(got, "nativeonly/TestE2EOnlyPackage ") {
		t.Fatalf("requested e2e-only native shard executions = %q, want its test exactly once", got)
	}
	requestedShardScope, err := os.ReadFile(requestedShardScopeLog)
	if err != nil {
		t.Fatalf("read requested native shard instrumentation scope: %v", err)
	}
	if strings.Contains(string(requestedShardScope), "fixture.test/app/nativeonly") {
		t.Errorf("requested native shard child-process instrumentation scope %q includes an e2e-only package", requestedShardScope)
	}
}

func TestE2ENativeCoverageRejectsUnavailableInstrumentationBeforeRunningTests(t *testing.T) {
	t.Parallel()
	module := nativeModule(t)
	if err := os.Mkdir(filepath.Join(module, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	nativeWrite(t, filepath.Join(module, "pkg", "app.go"), "package pkg\nfunc Value() int { return 1 }\n")
	nativeWrite(t, filepath.Join(module, "pkg", "native_test.go"), "//go:build e2e\n\npackage pkg\nimport (\"os\"; \"testing\")\nfunc TestE2EShouldNotRun(t *testing.T) { if err:=os.WriteFile(os.Getenv(\"WB_NATIVE_SCOPE_MARKER\"),[]byte(\"ran\"),0600);err!=nil {t.Fatal(err)} }\n")
	profile := filepath.Join(t.TempDir(), "native.cov")
	marker := filepath.Join(t.TempDir(), "native-ran")
	_, attempts, err := runShardedCoverageWithTestOptions(context.Background(), nil, module, profile, []string{"./pkg"}, 2, "", "", time.Minute, time.Minute, 0, nil, []string{"WB_NATIVE_SCOPE_MARKER=" + marker}, false, coverageTestOptions{
		tags: []string{"-tags=e2e"}, selector: NativeGoTestSelector, coverMode: "count", nativeOnly: true, countOnce: true,
	}, []string{"./..."}, []string{"./missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unavailable instrumentation scope error = %v, want missing-package diagnostic", err)
	}
	if attempts != 0 {
		t.Fatalf("unavailable instrumentation scope started %d test attempts", attempts)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("rejected instrumentation scope published a profile: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native tests ran before instrumentation scope was established: %v", err)
	}
}
