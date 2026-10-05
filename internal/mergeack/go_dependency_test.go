package mergeack

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

func TestGoDependencyOwnerModuleContracts(t *testing.T) {
	t.Parallel()
	const source = "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.0.0\n"
	target := strings.Replace(source, "v1.0.0", "v1.1.0", 1)
	for _, row := range []struct{ name, source, target, want string }{
		{"source syntax", "module (", target, "parse source go.mod"},
		{"target syntax", source, "module (", "parse target go.mod"},
		{"source invalid version", strings.Replace(source, "v1.0.0", "banana", 1), target, "parse source go.mod"},
		{"target invalid version", source, strings.Replace(target, "v1.1.0", "banana", 1), "parse target go.mod"},
		{"source duplicate", source + "require example.test/a v1.0.0\n", target, "repeats require path"},
		{"target duplicate", source, target + "require example.test/a v1.1.0\n", "repeats require path"},
		{"removed", source, "module example.test/app\n\ngo 1.22\n", "require path set changed"},
		{"different same size", source, strings.Replace(target, "require example.test/a ", "require example.test/b ", 1), "path set or directness changed"},
		{"indirect", source, strings.Replace(target, "v1.1.0", "v1.1.0 // indirect", 1), "path set or directness changed"},
		{"downgrade", source, strings.Replace(target, "v1.1.0", "v0.9.0", 1), "downgrade"},
		{"equal", source, source, "no dependency version upgrade"},
		{"go directive", source, strings.Replace(target, "go 1.22", "go 1.23", 1), "differs outside"},
		{"module identity", source, strings.Replace(target, "example.test/app", "example.test/other", 1), "differs outside"},
		{"comment", source, target + "// new comment\n", "differs outside"},
		{"replace", source, target + "replace example.test/a => example.test/a v1.1.0\n", "differs outside"},
		{"toolchain", source, target + "toolchain go1.23.0\n", "differs outside"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, err := goModDependencyUpgrades(row.source, row.target)
			if err == nil || !strings.Contains(err.Error(), row.want) || got != nil {
				t.Fatalf("upgrades=%v error=%v; want nil and %q", got, err, row.want)
			}
		})
	}
	for _, row := range []struct {
		name, source, target string
		want                 map[string][2]string
	}{
		{"one", source, target, map[string][2]string{"example.test/a": {"v1.0.0", "v1.1.0"}}},
		{"two plus unchanged", source + "require example.test/b v1.0.0\nrequire example.test/c v1.0.0\n", target + "require example.test/b v1.2.0\nrequire example.test/c v1.0.0\n", map[string][2]string{"example.test/a": {"v1.0.0", "v1.1.0"}, "example.test/b": {"v1.0.0", "v1.2.0"}}},
		{"prerelease", strings.Replace(source, "v1.0.0", "v1.1.0-beta.1", 1), target, map[string][2]string{"example.test/a": {"v1.1.0-beta.1", "v1.1.0"}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, err := goModDependencyUpgrades(row.source, row.target)
			if err != nil || !reflect.DeepEqual(got, row.want) {
				t.Fatalf("upgrades=%v error=%v; want %v", got, err, row.want)
			}
		})
	}
}

func TestGoDependencyOwnerRequirementShapes(t *testing.T) {
	t.Parallel()
	got, err := goRequirements(&modfile.File{})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty requirements=%v/%v", got, err)
	}
	file := &modfile.File{Require: []*modfile.Require{{Mod: module.Version{Path: "example.test/a", Version: "v1.0.0"}, Indirect: true}}}
	got, err = goRequirements(file)
	if err != nil || !reflect.DeepEqual(got, map[string]goRequirement{"example.test/a": {version: "v1.0.0", indirect: true}}) {
		t.Fatalf("static requirement=%v/%v", got, err)
	}
	file.Require = append(file.Require, file.Require[0])
	if _, err = goRequirements(file); err == nil || !strings.Contains(err.Error(), "repeats require path") {
		t.Fatalf("duplicate=%v", err)
	}
}

func TestGoDependencyOwnerChecksumContracts(t *testing.T) {
	t.Parallel()
	upgrades := map[string][2]string{"example.test/a": {"v1.0.0", "v1.1.0"}}
	old := "example.test/a v1.0.0 h1:old\n"
	next := "example.test/a v1.1.0 h1:new\n"
	for _, row := range []struct{ name, source, target, want string }{
		{"empty", "", "", ""}, {"unchanged unrelated", "other v2 h1:x\n", "other v2 h1:x\n", ""},
		{"upgrade", old, next, ""}, {"module sums", old + "example.test/a v1.0.0/go.mod h1:mod\n", next + "example.test/a v1.1.0/go.mod h1:newmod\n", ""},
		{"duplicate multiplicity", old + old, next + next, ""},
		{"removed malformed", "bad\n", "", "malformed line"}, {"added malformed", "", "bad\n", "malformed line"},
		{"removed unrelated", "other v1 h1:x\n", "", "outside upgraded dependencies"}, {"added unrelated", "", "other v1 h1:x\n", "outside upgraded dependencies"},
		{"removed new", next, "", "outside upgraded dependency versions"}, {"added old", "", old, "outside upgraded dependency versions"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			err := goSumChangesAreOnlyDependencyUpgrades(row.source, row.target, upgrades)
			if row.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("error=%v want %q", err, row.want)
			}
		})
	}
	got := goSumLineCounts(" \n  a v1 h1:x  \n\na v1 h1:x\n b v2 h1:y\n")
	want := map[string]int{"a v1 h1:x": 2, "b v2 h1:y": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts=%v want %v", got, want)
	}
}

func TestProveGoDependencyUpgradeComposition(t *testing.T) {
	t.Parallel()
	const sourceMod = "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.0.0\n"
	const sourceSum = "example.test/a v1.0.0 h1:old\n"
	targetMod := strings.Replace(sourceMod, "v1.0.0", "v1.1.0", 1)
	const targetSum = "example.test/a v1.1.0 h1:new\n"
	for _, row := range []struct {
		name, sourceMod, targetMod, sourceSum, targetSum, wantError string
		wantCount                                                   int
	}{
		{name: "module refusal precedes checksum refusal", sourceMod: "module (", targetMod: targetMod, sourceSum: "bad", targetSum: "different bad", wantError: "parse source go.mod"},
		{name: "checksum refusal", sourceMod: sourceMod, targetMod: targetMod, sourceSum: sourceSum, targetSum: "unrelated v1 h1:x\n", wantError: "outside upgraded dependencies"},
		{name: "one upgrade", sourceMod: sourceMod, targetMod: targetMod, sourceSum: sourceSum, targetSum: targetSum, wantCount: 1},
		{name: "two upgrades", sourceMod: sourceMod + "require example.test/b v1.0.0\n", targetMod: targetMod + "require example.test/b v1.2.0\n", sourceSum: sourceSum, targetSum: targetSum, wantCount: 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			count, err := ProveGoDependencyUpgrade(row.sourceMod, row.targetMod, row.sourceSum, row.targetSum)
			if row.wantError != "" {
				if count != 0 || err == nil || !strings.Contains(err.Error(), row.wantError) {
					t.Fatalf("proof=%d/%v want zero/%q", count, err, row.wantError)
				}
			} else if err != nil || count != row.wantCount {
				t.Fatalf("proof=%d/%v want %d", count, err, row.wantCount)
			}
		})
	}
}
