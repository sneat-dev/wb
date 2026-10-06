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
	repo.writeFile("pkg/native_test.go", "//go:build e2e\n\npackage pkg\nimport (\"os/exec\"; \"testing\")\nfunc TestE2ENative(t *testing.T) { if err:=exec.Command(\"git\",\"--version\").Run();err!=nil {t.Fatal(err)}; if Native()!=2 {t.Fatal(\"native\")} }\n")
	repo.writeFile("extra/app.go", "package extra\nfunc Extra() int { return 3 }\n")
	repo.writeFile("extra/app_test.go", "package extra\nimport \"testing\"\nfunc TestExtra(t *testing.T) { if Extra()!=3 {t.Fatal(\"extra\")} }\n")
	repo.commitAll("combined coverage fixture")
	profile := filepath.Join(t.TempDir(), "combined.cov")
	options := RunOptions{IncludeE2E: true, GoTestPackages: []string{"./..."}, GoShardPackages: []string{"./pkg"}, GoTestShards: 2, Timeout: time.Minute, CheckTimeout: 2 * time.Minute, Env: []string{"GOFLAGS=-coverpkg=example.invalid/foreign"}}
	if output, _, err := runCoverageWithOptions(context.Background(), options, repo.dir, profile); err != nil {
		t.Fatalf("real combined run: %v\n%s", err, output)
	}
	statements, covered, err := profileTotals(profile)
	if err != nil || statements != 3 || covered != 3 {
		t.Fatalf("real combined coverage=%d/%d: %v", covered, statements, err)
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
}
