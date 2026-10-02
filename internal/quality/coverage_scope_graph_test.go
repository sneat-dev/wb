package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAffectedCoverageScopeGraphs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		native    bool
		fail      string
		wantError string
	}{
		{name: "unit"}, {name: "native", native: true},
		{name: "temp failure", fail: "temp", wantError: "temp"},
		{name: "checkout failure", fail: "add", wantError: "prepare coverage scope"},
		{name: "graph failure", fail: "list", wantError: "read coverage scope graph"},
		{name: "head identity failure", fail: "head", wantError: "resolve coverage head revision"},
		{name: "build identity failure", fail: "env", wantError: "read coverage build identity"},
		{name: "build mismatch", fail: "mismatch", wantError: "different Go versions or build flags"},
		{name: "parse failure", fail: "parse", wantError: "decode coverage scope graph"},
		{name: "cleanup failure", fail: "remove", wantError: "coverage scope cleanup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			directory := filepath.Join(t.TempDir(), "scope")
			command := func(ctx context.Context, root, name string, args ...string) (string, error) {
				calls = append(calls, strings.Join(append([]string{name}, args...), " "))
				if name == "git" {
					if args[0] == "rev-parse" {
						if test.fail == "head" {
							return "", errors.New("head")
						}
						return "head-sha\n", nil
					}
					if args[1] == test.fail {
						return "", errors.New(test.fail)
					}
					if args[1] == "remove" {
						if _, ok := ctx.Deadline(); !ok {
							t.Fatal("cleanup must be bounded")
						}
					}
					return "", nil
				}
				if args[0] == "env" {
					if test.fail == "env" {
						return "", errors.New("env")
					}
					if test.fail == "mismatch" && root == filepath.Join(directory, "base") {
						return "different", nil
					}
					return `{ "GOVERSION": "go1.27" }`, nil
				}
				if test.fail == "list" {
					return "", errors.New("list")
				}
				if test.fail == "parse" {
					return "{", nil
				}
				return fmt.Sprintf(`{"Dir":%q,"ImportPath":"m/core"} {"Dir":%q,"ImportPath":"m/consumer","XTestImports":["m/core"]}`, filepath.Join(root, "core"), filepath.Join(root, "consumer")), nil
			}
			temp := func(string, string) (string, error) {
				if test.fail == "temp" {
					return "", errors.New("temp")
				}
				return directory, nil
			}
			got, err := affectedCoverageScope(t.Context(), t.TempDir(), "base-sha", map[string]bool{"core/testdata/input.txt": true}, test.native, command, temp)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %s", err, test.wantError)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.Packages, []string{"./consumer", "./core"}) || !reflect.DeepEqual(got.ChangedPackages, []string{"./core"}) {
				t.Fatalf("scope = %+v, error=%v", got, err)
			}
			if got.Identity == nil || got.Identity.HeadSHA != "head-sha" || got.Identity.BaseSHA != "base-sha" || got.Identity.IncludeE2E != test.native || len(got.Identity.BuildSHA256) != 64 {
				t.Fatalf("identity=%+v", got.Identity)
			}
			wantCalls := 7
			if test.native {
				wantCalls = 9
			}
			if len(calls) != wantCalls {
				t.Fatalf("calls=%v", calls)
			}
		})
	}
}

func TestParseCoverageScopeGraph(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, test := range []struct {
		name, root, output string
		invalid            bool
	}{
		{name: "empty", root: root},
		{name: "module root", root: root, output: fmt.Sprintf(`{"Dir":%q,"ImportPath":"m"}`, root)},
		{name: "decode", root: root, output: "{", invalid: true},
		{name: "parent", root: root, output: fmt.Sprintf(`{"Dir":%q,"ImportPath":"m"}`, filepath.Dir(root)), invalid: true},
		{name: "outside", root: root, output: fmt.Sprintf(`{"Dir":%q,"ImportPath":"m"}`, filepath.Join(filepath.Dir(root), "other", "pkg")), invalid: true},
		{name: "missing import", root: root, output: fmt.Sprintf(`{"Dir":%q}`, root), invalid: true},
		{name: "relative directory", root: root, output: `{"Dir":"relative","ImportPath":"m"}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCoverageScopeGraph(test.root, test.output)
			if (err != nil) != test.invalid {
				t.Fatalf("got=%+v error=%v", got, err)
			}
		})
	}
}

func TestSelectedCoverageOptionsAndExistingPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "new"), 0700); err != nil {
		t.Fatal(err)
	}
	writeCoverageFixture(t, filepath.Join(root, "new", "new.go"), "package new\n")
	selected, err := ExistingCoveragePackages(root, []string{"./new", "./deleted", "./...", "./nested/..."})
	if err != nil || !reflect.DeepEqual(selected, []string{"./new", "./...", "./nested/..."}) {
		t.Fatal(selected)
	}
	options := RunOptions{GoTestShards: 4, GoShardPackages: []string{"./heavy", "./other"}}
	full := SelectedCoverageOptions(options, []string{"./..."})
	if !reflect.DeepEqual(full.GoShardPackages, options.GoShardPackages) {
		t.Fatal(full)
	}
	partial := SelectedCoverageOptions(options, []string{"./heavy", "./light"})
	if partial.GoTestShards != 4 || !reflect.DeepEqual(partial.GoShardPackages, []string{"./heavy"}) {
		t.Fatal(partial)
	}
	serial := SelectedCoverageOptions(options, []string{"./light"})
	if serial.GoTestShards != 1 || len(serial.GoShardPackages) != 0 {
		t.Fatal(serial)
	}
	if _, err := os.Stat(filepath.Join(root, "new", "new.go")); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageScopeFixtureOwnershipRatchet(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{{File: "m/core/core.go", StartLine: 2, EndLine: 2, Statements: 1}}
	baseline := BaselineFromProfile([]CoverageBlock{{File: "m/core/core.go", StartLine: 2, EndLine: 2, Statements: 1, Count: 1}}, "m", "base")
	results, _ := EvaluateRatchet(blocks, nil, map[string]bool{"core/testdata/input.json": true}, nil, baseline, "m", []string{"./core"})
	if len(results) != 1 || !results[0].Changed || !results[0].Rose {
		t.Fatalf("fixture ratchet=%+v", results)
	}
}

func TestExistingCoveragePackagesErrorsAndIgnoredFiles(t *testing.T) {
	t.Parallel()
	if _, err := existingCoveragePackages("/module", []string{"./pkg"}, func(string) ([]os.DirEntry, error) { return nil, os.ErrPermission }); err == nil {
		t.Fatal("permission error was treated as absent")
	}
	root := t.TempDir()
	for _, name := range []string{"_ignored.go", ".ignored.go", "asset.json"} {
		writeCoverageFixture(t, filepath.Join(root, name), "ignored")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.go"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := ExistingCoveragePackages(root, []string{"."})
	if err != nil || len(got) != 0 {
		t.Fatalf("ignored scope=%v error=%v", got, err)
	}
}

func TestCoverageTierMismatch(t *testing.T) {
	t.Parallel()
	unit := []CoverageScopePackage{{Pattern: "./unit"}, {Pattern: "./both"}}
	native := []CoverageScopePackage{{Pattern: "./native"}, {Pattern: "./both"}}
	for _, test := range []struct {
		selected []string
		want     bool
	}{
		{selected: []string{"./unit"}, want: true},
		{selected: []string{"./native"}, want: true},
		{selected: []string{"./both", "./absent"}},
	} {
		if got := coverageTierMismatch(test.selected, unit, native); got != test.want {
			t.Fatalf("mismatch(%v)=%t", test.selected, got)
		}
	}
}

func TestParseCoverageScopeGraphPreservesEmbedRelationships(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	graph, err := parseCoverageScopeGraph(root, fmt.Sprintf(`{"Dir":%q,"ImportPath":"m/parent","EmbedFiles":["child/data.txt"],"TestEmbedFiles":["child/test.txt"],"XTestEmbedFiles":["child/external.txt"]}`, filepath.Join(root, "parent")))
	if err != nil || len(graph) != 1 {
		t.Fatalf("graph=%+v error=%v", graph, err)
	}
	if !reflect.DeepEqual(graph[0].EmbedFiles, []string{"child/data.txt"}) || !reflect.DeepEqual(graph[0].TestEmbedFiles, []string{"child/test.txt"}) || !reflect.DeepEqual(graph[0].XTestEmbedFiles, []string{"child/external.txt"}) {
		t.Fatalf("embedding metadata=%+v", graph[0])
	}
}

func TestParseCoverageScopeGraphIgnoresSyntheticTestOwners(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	output := fmt.Sprintf(`{"Dir":%q,"ImportPath":"m/legitimate.test","Name":"main","GoFiles":["real.go"]}
{"Dir":%q,"ImportPath":"m/legitimate.test [m/legitimate.test.test]","ForTest":"m/legitimate.test","GoFiles":["real.go","real_test.go"]}
{"Dir":%q,"ImportPath":"m/legitimate.test.test","Name":"main","GoFiles":["_testmain.go"]}
{"Dir":%q,"ImportPath":"m/legitimate.test.test","Name":"main","GoFiles":["/cache/generated-testmain-d"]}`, root, root, root, root)
	graph, err := parseCoverageScopeGraph(root, output)
	if err != nil || len(graph) != 1 || graph[0].ImportPath != "m/legitimate.test" {
		t.Fatalf("ordinary/synthetic graph=%+v error=%v", graph, err)
	}
}
