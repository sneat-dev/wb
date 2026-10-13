//go:build e2e

package quality

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestE2ENativeShardingRealTaggedScopeAndCombinedProfile(t *testing.T) {
	if !qualityFixtureChild(t) {
		t.Parallel()
		runQualityFixtureChild(t)
		return
	}
	for _, tc := range []struct{ combined, fail bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		//nolint:paralleltest // Private child owns log path; each row gets a separate native module.
		t.Run(fmt.Sprintf("combined=%t/fail=%t", tc.combined, tc.fail), func(t *testing.T) {
			combined := tc.combined
			t.Setenv("WB_NATIVE_UNICODE_FAIL", fmt.Sprint(tc.fail))
			module := t.TempDir()
			log := filepath.Join(module, "runs.log")
			t.Setenv("WB_NATIVE_SHARD_LOG", log)
			writeQualityFile(t, filepath.Join(module, "go.mod"), "module example.test/native\n\ngo 1.24\n")
			record := `func record(name string) { f,e:=os.OpenFile(os.Getenv("WB_NATIVE_SHARD_LOG"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if e!=nil { panic(e) }; defer f.Close(); if _,e=f.WriteString(name+"\n"); e!=nil { panic(e) } }
`
			for _, pkg := range []string{"serial", "ordinary", "empty"} {
				writeQualityFile(t, filepath.Join(module, pkg, "source.go"), "package "+pkg+"\nfunc Value() int { return 1 }\n")
			}
			writeQualityFile(t, filepath.Join(module, "serial", "unit_test.go"), "package serial\nimport (\"os\";\"testing\")\nfunc TestUnit(t *testing.T) { record(\"unit\"); if Value()!=1 { t.Fatal(\"unit\") } }\n"+record)
			native := "//go:build e2e\npackage serial\nimport (\"os\";\"testing\")\n"
			for _, name := range []string{"TestE2EAlpha", "TestE2EBravo", "TestContractCharlie", "TestE2EDelta", "TestE2ECafé", "TestContract日本語٢"} {
				body := "if Value()!=1 { t.Fatal(\"native\") }"
				if name == "TestE2ECafé" {
					body += "; if os.Getenv(\"WB_NATIVE_UNICODE_FAIL\")==\"true\" { t.Fatal(\"Unicode native failure\") }"
				}
				native += "func " + name + "(t *testing.T) { record(\"" + name + "\"); " + body + " }\n"
			}
			writeQualityFile(t, filepath.Join(module, "serial", "native_test.go"), native)
			for _, pkg := range []string{"ordinary", "tagonly"} {
				writeQualityFile(t, filepath.Join(module, pkg, "native_test.go"), "//go:build e2e\npackage "+pkg+"\nimport (\"os\";\"testing\")\nfunc TestE2EWhole(t *testing.T) { record(\""+pkg+"\") }\n"+record)
			}
			writeQualityFile(t, filepath.Join(module, "empty", "native_test.go"), "//go:build e2e\npackage empty\nimport (\"os\";\"testing\")\nfunc TestMain(m *testing.M) { record(\"empty-main\"); os.Exit(m.Run()) }\n"+record)
			writeQualityFile(t, filepath.Join(module, "package.json"), `{"scripts":{"test":"false"}}`)
			options := RunOptions{NativeOnly: !combined, IncludeE2E: combined, GoTestShards: 4, GoShardPackages: []string{"./serial", "./empty"}, Timeout: time.Minute}
			profile := filepath.Join(module, "merged.cov")
			if combined {
				options.GoShardPackages = []string{"./serial"}
			}
			output, attempts, err := runCoverageWithOptions(t.Context(), options, module, profile)
			if (err != nil) != tc.fail || attempts != 1 || (tc.fail && !strings.Contains(output, "TestE2ECafé")) {
				t.Fatalf("native combined=%t: %v\n%s", combined, err, output)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Fields(string(raw))
			sort.Strings(got)
			want := []string{"TestContractCharlie", "TestE2EAlpha", "TestE2EBravo", "TestE2EDelta", "TestE2ECafé", "TestContract日本語٢", "ordinary", "tagonly"}
			if !combined {
				want = append(want, "empty-main", "empty-main")
			} else {
				want = append(want, "empty-main", "unit")
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("native set=%v want=%v", got, want)
			}
			if tc.fail {
				if _, e := os.Stat(profile); !os.IsNotExist(e) {
					t.Fatalf("failed Unicode native test published profile: %v", e)
				}
			} else if _, blocks, e := readCoverageProfile(profile); e != nil || len(blocks) == 0 {
				t.Fatalf("merged profile=%v %v", blocks, e)
			}
		})
	}
}
