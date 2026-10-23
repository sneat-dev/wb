package orchestrate

import (
	"context"
	"strings"
	"testing"
)

func TestProveGoDependencyUpgradePair(t *testing.T) {
	const sourceMod = `module example.test/app

go 1.22

require example.test/a v1.0.0
`
	const sourceSum = `example.test/a v1.0.0 h1:old
example.test/a v1.0.0/go.mod h1:oldmod
`
	const targetMod = `module example.test/app

go 1.22

require example.test/a v1.1.0
`
	const targetSum = `example.test/a v1.1.0 h1:new
example.test/a v1.1.0/go.mod h1:newmod
`

	tests := []struct {
		name                 string
		sourceMod, sourceSum string
		targetMod, targetSum string
		wantCount            int
		wantError            string
	}{
		{name: "valid strict upgrade", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum, wantCount: 1},
		{name: "downgrade", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: strings.Replace(targetMod, "v1.1.0", "v0.9.0", 1), targetSum: targetSum, wantError: "downgrade"},
		{name: "removed require", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: "module example.test/app\n\ngo 1.22\n", targetSum: targetSum, wantError: "require path set changed"},
		{name: "added require", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: strings.Replace(targetMod, "require example.test/a v1.1.0", "require (\n\texample.test/a v1.1.0\n\texample.test/b v1.0.0\n)", 1), targetSum: targetSum, wantError: "require path set changed"},
		{name: "other go.mod edit", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: strings.Replace(targetMod, "go 1.22", "go 1.23", 1), targetSum: targetSum, wantError: "differs outside require dependency versions"},
		{name: "unrelated go.sum change", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum + "example.test/other v1.0.0 h1:other\n", wantError: "outside upgraded dependencies"},
		{name: "added checksum for old version", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum + "example.test/a v1.0.0 h1:tampered\n", wantError: "outside upgraded dependency versions"},
		{name: "removed checksum for new version", sourceMod: sourceMod, sourceSum: sourceSum + "example.test/a v1.1.0 h1:new\n", targetMod: targetMod, targetSum: sourceSum, wantError: "outside upgraded dependency versions"},
		{name: "missing source sum", sourceMod: sourceMod, sourceSum: "", targetMod: targetMod, targetSum: targetSum, wantError: "requires go.mod and go.sum"},
		{name: "missing target mod", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: "", targetSum: targetSum, wantError: "requires go.mod and go.sum"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEngineFixture(t)
			sourceSHA, targetSHA := commitGoDependencyPair(t, fixture, test.sourceMod, test.sourceSum, test.targetMod, test.targetSum)
			gotCount, err := proveGoDependencyUpgradePair(context.Background(), fixture.canonical, sourceSHA, targetSHA)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("proveGoDependencyUpgradePair error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotCount != test.wantCount {
				t.Fatalf("upgrade count = %d, want %d", gotCount, test.wantCount)
			}
		})
	}
}

func TestAbsorbedConflictProvesDivergedGoDependencyPairAndRejectsOtherContent(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-go-upgrade", "feature/go-upgrade", "main", "absorbed.txt", "same\n")
	writeGoDependencyPairFiles(t, source.WorktreeDir, "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.0.0\n", "example.test/a v1.0.0 h1:old\n")
	runEngineGit(t, source.WorktreeDir, "add", "go.mod", "go.sum")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: use initial dependency")
	sourceSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	writeEngineFile(t, fixture.canonical+"/absorbed.txt", "same\n")
	writeGoDependencyPairFiles(t, fixture.canonical, "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.1.0\n", "example.test/a v1.1.0 h1:new\n")
	runEngineGit(t, fixture.canonical, "add", "absorbed.txt", "go.mod", "go.sum")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: independently absorb with upgraded dependency")
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	proof, err := proveAbsorbedConflictSource(context.Background(), fixture.canonical, targetSHA, WorktreeMergeSource{SHA: sourceSHA}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proof.method != "content_absorbed" || proof.pathCount != 3 {
		t.Fatalf("unexpected diverged-history proof: %+v", proof)
	}
	for _, path := range []string{"go.mod", "go.sum"} {
		found := false
		for _, pathProof := range proof.pathProofs {
			if pathProof.Path == path && pathProof.Method == "go_dependency_upgrade" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing paired Go dependency proof for %s: %+v", path, proof.pathProofs)
		}
	}
	writeEngineFile(t, fixture.canonical+"/absorbed.txt", "unrelated\n")
	runEngineGit(t, fixture.canonical, "add", "absorbed.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: alter unrelated source content")
	changedTargetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	if _, err := proveAbsorbedConflictSource(context.Background(), fixture.canonical, changedTargetSHA, WorktreeMergeSource{SHA: sourceSHA}, nil); err == nil || !strings.Contains(err.Error(), "not content-absorbed") {
		t.Fatalf("unrelated path divergence not refused: %v", err)
	}
}

func commitGoDependencyPair(t *testing.T, fixture engineFixture, sourceMod, sourceSum, targetMod, targetSum string) (string, string) {
	t.Helper()
	writeGoDependencyPairFiles(t, fixture.canonical, sourceMod, sourceSum)
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: source dependencies")
	sourceSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	writeGoDependencyPairFiles(t, fixture.canonical, targetMod, targetSum)
	runEngineGit(t, fixture.canonical, "add", "-A")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: target dependencies")
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	return sourceSHA, targetSHA
}

func writeGoDependencyPairFiles(t *testing.T, root, mod, sum string) {
	t.Helper()
	if mod == "" {
		runEngineGit(t, root, "rm", "-f", "--ignore-unmatch", "go.mod")
	} else {
		writeEngineFile(t, root+"/go.mod", mod)
	}
	if sum == "" {
		runEngineGit(t, root, "rm", "-f", "--ignore-unmatch", "go.sum")
	} else {
		writeEngineFile(t, root+"/go.sum", sum)
	}
}
