package worktrees

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeproof"
)

func TestWtLogCovDependencyDeltasForValidation(t *testing.T) {
	t.Parallel()
	valid := SupersessionDependencyDelta{
		SourcePR: "https://github.com/acme/app/pull/17", SourceHead: "source", Consumer: "acme/app",
		Ecosystem: "npm", Package: "nx", Manifest: "package.json", Selector: "dependencies.nx",
		Before: "22.6.4", RequestedAfter: "22.7.7", CandidateAfter: "22.7.7", Reviewed: true,
	}
	receipt := SupersessionReceipt{
		OriginalPR: valid.SourcePR, OriginalHead: valid.SourceHead,
		DependencyDeltasComplete: true, DependencyDeltas: []SupersessionDependencyDelta{valid},
	}
	entry := ListResult{Repository: valid.Consumer, HeadSHA: valid.SourceHead}
	if deltas, rejection := worktreebranches.DependencyDeltasForValidation(receipt, supersessionEntry(entry)); rejection != "" || len(deltas) != 1 {
		t.Fatalf("valid deltas = %#v, %q", deltas, rejection)
	}

	for name, mutate := range map[string]func(*SupersessionReceipt, *ListResult){
		"incomplete":    func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltasComplete = false },
		"empty":         func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas = nil },
		"source PR":     func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas[0].SourcePR = "other" },
		"source head":   func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas[0].SourceHead = "other" },
		"consumer":      func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas[0].Consumer = "acme/other" },
		"missing proof": func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas[0].Manifest = "" },
		"unreviewed":    func(receipt *SupersessionReceipt, _ *ListResult) { receipt.DependencyDeltas[0].Reviewed = false },
		"version mismatch": func(receipt *SupersessionReceipt, _ *ListResult) {
			receipt.DependencyDeltas[0].CandidateAfter = "23.0.0"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := receipt
			candidate.DependencyDeltas = append([]SupersessionDependencyDelta(nil), receipt.DependencyDeltas...)
			candidateEntry := entry
			mutate(&candidate, &candidateEntry)
			if _, rejection := worktreebranches.DependencyDeltasForValidation(candidate, supersessionEntry(candidateEntry)); rejection == "" {
				t.Fatal("invalid dependency deltas were accepted")
			}
		})
	}

	second := valid
	second.Manifest = "a.json"
	receipt.DependencyDeltas = []SupersessionDependencyDelta{valid, second}
	deltas, rejection := worktreebranches.DependencyDeltasForValidation(receipt, supersessionEntry(entry))
	if rejection != "" || deltas[0].Manifest != "a.json" || receipt.DependencyDeltas[0].Manifest != "package.json" {
		t.Fatalf("sorted validation deltas = %#v, original = %#v, rejection = %q", deltas, receipt.DependencyDeltas, rejection)
	}
}

func TestWtLogCovSelectorPackageFromLockfileSelector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		selector string
		want     string
	}{
		{"packages|node_modules/nx|version", "nx"},
		{"packages|node_modules/@nx/js|version", "@nx/js"},
		{"snapshots|/nx@22.7.7|version", "nx"},
		{"snapshots|/@nx/js@22.7.7|version", "@nx/js"},
		{"snapshots|/nx|version", ""},
		{"importers|.|version", ""},
		{"packages|node_modules/nx", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := worktreebranches.SelectorPackageFromLockfileSelector(tc.selector); got != tc.want {
			t.Errorf("selectorPackageFromLockfileSelector(%q) = %q, want %q", tc.selector, got, tc.want)
		}
	}
}

func TestWtLogCovParseLockfileSelector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		ecosystem   string
		lockfile    string
		selector    string
		packageName string
		wantOK      bool
	}{
		{"package-lock exact", "npm", "package-lock.json", "packages|node_modules/nx|version", "nx", true},
		{"package-lock wrong node path", "npm", "package-lock.json", "packages|node_modules/other|version", "nx", false},
		{"package-lock wrong root", "npm", "package-lock.json", "snapshots|node_modules/nx|version", "nx", false},
		{"pnpm exact", "npm", "pnpm-lock.yaml", "snapshots|/nx@22.7.7|version", "nx", true},
		{"pnpm bare package token", "npm", "pnpm-lock.yaml", "snapshots|/nx@|version", "nx", false},
		{"pnpm scoped prefix only", "npm", "pnpm-lock.yaml", "snapshots|/nxfoo@1.0.0|version", "nx", false},
		{"yarn unsupported", "npm", "yarn.lock", "packages|node_modules/nx|version", "nx", false},
		{"non-npm ecosystem", "go", "go.sum", "packages|node_modules/nx|version", "nx", false},
		{"empty package", "npm", "package-lock.json", "packages|node_modules/nx|version", "", false},
		{"wrong leaf field", "npm", "package-lock.json", "packages|node_modules/nx|resolved", "nx", false},
		{"too few segments", "npm", "package-lock.json", "packages|node_modules/nx", "nx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			segments, ok := worktreebranches.ParseLockfileSelector(tc.ecosystem, tc.lockfile, tc.selector, tc.packageName)
			if ok != tc.wantOK {
				t.Fatalf("parseLockfileSelector(%q,%q,%q,%q) ok = %t, want %t", tc.ecosystem, tc.lockfile, tc.selector, tc.packageName, ok, tc.wantOK)
			}
			if ok && len(segments) != 3 {
				t.Fatalf("accepted selector returned %#v", segments)
			}
		})
	}
}

func TestWtLogCovLockfileEntryContainsVersion(t *testing.T) {
	t.Parallel()
	goSum := "example.com/nx v1.2.3 h1:aaa=\nexample.com/nx v1.2.3/go.mod h1:bbb=\nexample.com/other v9.9.9 h1:ccc=\n"
	if !worktreebranches.LockfileEntryContainsVersion("go", "go.sum", goSum, "example.com/nx", "v1.2.3") {
		t.Fatal("go.sum exact module at version was not proven")
	}
	if worktreebranches.LockfileEntryContainsVersion("go", "go.sum", goSum, "example.com/nx", "v1.2.4") {
		t.Fatal("go.sum accepted the wrong version")
	}
	if worktreebranches.LockfileEntryContainsVersion("go", "go.sum", "shortline\n", "shortline", "v1.0.0") {
		t.Fatal("go.sum accepted a line without two fields")
	}
	if worktreebranches.LockfileEntryContainsVersion("npm", "yarn.lock", "nx@1.0.0:\n", "packages|node_modules/nx|version", "1.0.0") {
		t.Fatal("yarn.lock is not a supported proof format")
	}
	if worktreebranches.LockfileEntryContainsVersion("npm", "package-lock.json", "not: [valid", "packages|node_modules/nx|version", "1.0.0") {
		t.Fatal("invalid YAML was accepted")
	}

	pnpm := "snapshots:\n  /nx@22.7.7:\n    version: 22.7.7\n"
	if !worktreebranches.LockfileEntryContainsVersion("npm", "pnpm-lock.yaml", pnpm, "snapshots|/nx@22.7.7|version", "22.7.7") {
		t.Fatal("structured pnpm proof was not accepted")
	}
	if worktreebranches.LockfileEntryContainsVersion("npm", "pnpm-lock.yaml", pnpm, "snapshots|/nx@22.7.7|version", "22.7.8") {
		t.Fatal("pnpm proof accepted the wrong version")
	}
	if worktreebranches.LockfileEntryContainsVersion("npm", "pnpm-lock.yaml", pnpm, "snapshots|/other@22.7.7|version", "22.7.7") {
		t.Fatal("pnpm proof accepted a missing selector")
	}
	scalar := "snapshots: not-a-mapping\n"
	if worktreebranches.LockfileEntryContainsVersion("npm", "pnpm-lock.yaml", scalar, "snapshots|/nx@22.7.7|version", "22.7.7") {
		t.Fatal("pnpm proof walked into a scalar node")
	}
}

func TestWtLogCovNormalizeDependencyVersion(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"1.2.3": "v1.2.3", "v1.2.3": "v1.2.3", "  v2.0.0 ": "v2.0.0", " 2.0.0": "v2.0.0", "": ""}
	for input, want := range cases {
		if got := worktreebranches.NormalizeDependencyVersion(input); got != want {
			t.Errorf("normalizeDependencyVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWtLogCovDependencyVersionSatisfiesEcosystems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ecosystem string
		candidate string
		requested string
		want      bool
	}{
		{"go", "v1.2.3", "1.2.3", true},
		{"go", "v1.2.4", "1.2.3", false},
		{"go", "notaversion", "1.2.3", false},
		{"npm", "22.8.0", "^22.7.7", true},
		{"npm", "23.0.0", "^22.7.7", false},
		{"npm", "21.9.9", "^22.7.7", false},
		{"npm", "0.2.9", "^0.2.3", true},
		{"npm", "0.3.0", "^0.2.3", false},
		{"npm", "0.0.3", "^0.0.3", true},
		{"npm", "0.0.4", "^0.0.3", false},
		{"npm", "1.2.9", "~1.2.3", true},
		{"npm", "1.3.0", "~1.2.3", false},
		{"npm", "1.5.0", ">=1.0.0 <2.0.0", true},
		{"npm", "2.0.0", ">=1.0.0 <2.0.0", false},
		{"npm", "2.0.0", "^1.0.0 || ^2.0.0", true},
		{"npm", "3.0.0", "^1.0.0 || ^2.0.0", false},
		{"npm", "1.2.3", "", false},
		{"npm", "", "1.2.3", false},
		{"pypi", "v1.2.3", "1.2.3", true},
		{"pypi", "v1.2.4", "1.2.3", false},
	}
	for _, tc := range cases {
		if got := worktreebranches.DependencyVersionSatisfies(tc.ecosystem, tc.candidate, tc.requested); got != tc.want {
			t.Errorf("dependencyVersionSatisfies(%q,%q,%q) = %t, want %t", tc.ecosystem, tc.candidate, tc.requested, got, tc.want)
		}
	}
}

func TestWtLogCovNpmRangeAlternativeSatisfies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate string
		requested string
		want      bool
	}{
		{"v1.2.3", "1.2.3", true},
		{"v1.2.4", "1.2.3", false},
		{"v22.8.0", "^22.7.7", true},
		{"v22.7.6", "^22.7.7", false},
		{"v0.3.0", "^0.2.3", false},
		{"v1.3.0", "~1.2.3", false},
		{"v1.2.9", "~1.2.3", true},
		{"v1.0.0", "^notaversion", false},
		{"v1.0.0", "^1.2", false},
		{"v1.5.0", ">=1.0.0 <2.0.0", true},
		{"v2.5.0", ">=1.0.0 <2.0.0", false},
	}
	for _, tc := range cases {
		if got := worktreebranches.NpmRangeAlternativeSatisfies(tc.candidate, tc.requested); got != tc.want {
			t.Errorf("npmRangeAlternativeSatisfies(%q,%q) = %t, want %t", tc.candidate, tc.requested, got, tc.want)
		}
	}
}

func TestWtLogCovNpmComparatorSatisfies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate  string
		constraint string
		want       bool
	}{
		{"v1.5.0", ">1.0.0", true},
		{"v1.0.0", ">1.0.0", false},
		{"v1.0.0", ">=1.0.0", true},
		{"v0.9.0", ">=1.0.0", false},
		{"v1.9.0", "<2.0.0", true},
		{"v2.0.0", "<2.0.0", false},
		{"v2.0.0", "<=2.0.0", true},
		{"v2.0.1", "<=2.0.0", false},
		{"v1.0.0", "=1.0.0", true},
		{"v1.0.1", "=1.0.0", false},
		{"v1.0.0", "1.0.0", true},
		{"v1.9.9", "1.x", true},
		{"v2.0.0", "1.x", false},
		{"v1.9.9", "1.*", true},
		{"v1.2.9", "1.2.x", true},
		{"v1.3.0", "1.2.X", false},
		{"v9.9.9", "*", true},
		{"vnotaversion", ">=1.0.0", false},
		{"v1.0.0", ">=notaversion", false},
	}
	for _, tc := range cases {
		if got := worktreebranches.NpmComparatorSatisfies(tc.candidate, tc.constraint); got != tc.want {
			t.Errorf("npmComparatorSatisfies(%q,%q) = %t, want %t", tc.candidate, tc.constraint, got, tc.want)
		}
	}
}

func TestWtLogCovValidateDependencyManifestNPM(t *testing.T) {
	t.Parallel()
	contents := []byte(`{"dependencies":{"nx":"22.7.7"},"devDependencies":{"jest":"29.0.0"},"peerDependencies":{"react":"18.0.0"},"optionalDependencies":{"fsevents":"2.3.0"}}`)
	cases := []struct {
		name     string
		delta    SupersessionDependencyDelta
		expected string
		exact    bool
		want     string
	}{
		{"exact match", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.nx", Package: "nx"}, "22.7.7", true, ""},
		{"exact mismatch", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.nx", Package: "nx"}, "22.7.8", true, "want"},
		{"range match", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.nx", Package: "nx"}, "^22.7.0", false, ""},
		{"range mismatch", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.nx", Package: "nx"}, "^23.0.0", false, "want"},
		{"dev dependencies", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "devDependencies.jest", Package: "jest"}, "29.0.0", true, ""},
		{"peer dependencies", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "peerDependencies.react", Package: "react"}, "18.0.0", true, ""},
		{"optional dependencies", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "optionalDependencies.fsevents", Package: "fsevents"}, "2.3.0", true, ""},
		{"unknown field", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "resolutions.nx", Package: "nx"}, "22.7.7", true, "not a direct dependency field"},
		{"selector shape", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies", Package: "nx"}, "22.7.7", true, "exact direct package selector"},
		{"selector package mismatch", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.other", Package: "nx"}, "22.7.7", true, "exact direct package selector"},
		{"absent package", SupersessionDependencyDelta{Ecosystem: "npm", Manifest: "package.json", Selector: "dependencies.missing", Package: "missing"}, "1.0.0", true, "absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejection := worktreebranches.ValidateDependencyManifest(tc.delta, contents, tc.expected, tc.exact)
			if tc.want == "" {
				if rejection != "" {
					t.Fatalf("valid npm manifest rejected: %s", rejection)
				}
				return
			}
			if !strings.Contains(rejection, tc.want) {
				t.Fatalf("rejection %q does not contain %q", rejection, tc.want)
			}
		})
	}
	if rejection := worktreebranches.ValidateDependencyManifest(SupersessionDependencyDelta{Ecosystem: "npm"}, []byte("{"), "1.0.0", true); !strings.Contains(rejection, "cannot parse npm manifest") {
		t.Fatalf("invalid JSON rejection = %q", rejection)
	}
	if rejection := worktreebranches.ValidateDependencyManifest(SupersessionDependencyDelta{Ecosystem: "cargo"}, nil, "1.0.0", true); !strings.Contains(rejection, "unsupported dependency ecosystem") {
		t.Fatalf("unsupported ecosystem rejection = %q", rejection)
	}
}

func TestWtLogCovValidateDependencyManifestGo(t *testing.T) {
	t.Parallel()
	manifest := []byte("module example.com/app\n\ngo 1.21\n\nrequire example.com/nx v1.2.3\n")
	delta := SupersessionDependencyDelta{Ecosystem: "go", Manifest: "go.mod", Selector: "require:example.com/nx", Package: "example.com/nx"}
	if rejection := worktreebranches.ValidateDependencyManifest(delta, manifest, "v1.2.3", true); rejection != "" {
		t.Fatalf("exact Go requirement rejected: %s", rejection)
	}
	// Validation examines every require entry; the value accessor intentionally
	// reports the first one. Keep those distinct contracts when sharing parsers.
	duplicates := []byte("module example.com/app\n\ngo 1.21\n\nrequire example.com/nx v1.2.3\nrequire example.com/nx v1.2.4\n")
	if rejection := worktreebranches.ValidateDependencyManifest(delta, duplicates, "v1.2.3", true); !strings.Contains(rejection, "v1.2.4") {
		t.Fatalf("later duplicate Go requirement was ignored: %q", rejection)
	}
	if value, found, err := worktreebranches.DependencyManifestValue(delta, duplicates); err != nil || !found || value != "v1.2.3" {
		t.Fatalf("first Go requirement value = %q, found=%t, err=%v", value, found, err)
	}
	if rejection := worktreebranches.ValidateDependencyManifest(delta, manifest, "v1.2.4", true); !strings.Contains(rejection, "want") {
		t.Fatalf("mismatched Go requirement rejection = %q", rejection)
	}
	// Go requirements stay exact even on the non-exact range path.
	if rejection := worktreebranches.ValidateDependencyManifest(delta, manifest, "v1.2.4", false); !strings.Contains(rejection, "want") {
		t.Fatalf("Go range check must stay exact: %q", rejection)
	}
	renamed := SupersessionDependencyDelta{Ecosystem: "go", Manifest: "go.mod", Selector: "require:example.com/other", Package: "example.com/nx"}
	if rejection := worktreebranches.ValidateDependencyManifest(renamed, manifest, "v1.2.3", true); !strings.Contains(rejection, "exact direct require selector") {
		t.Fatalf("Go selector rejection = %q", rejection)
	}
	absent := SupersessionDependencyDelta{Ecosystem: "go", Manifest: "go.mod", Selector: "require:example.com/missing", Package: "example.com/missing"}
	if rejection := worktreebranches.ValidateDependencyManifest(absent, manifest, "v1.2.3", true); !strings.Contains(rejection, "absent") {
		t.Fatalf("absent Go module rejection = %q", rejection)
	}
	if rejection := worktreebranches.ValidateDependencyManifest(delta, []byte("not a module\n"), "v1.2.3", true); !strings.Contains(rejection, "cannot parse Go manifest") {
		t.Fatalf("invalid Go manifest rejection = %q", rejection)
	}
}

func TestWtLogCovDependencyManifestValue(t *testing.T) {
	t.Parallel()
	npmContents := []byte(`{"dependencies":{"nx":"22.7.7"},"devDependencies":{"jest":"29.0.0"},"peerDependencies":{"react":"18.0.0"},"optionalDependencies":{"fsevents":"2.3.0"}}`)
	cases := []struct {
		name     string
		delta    SupersessionDependencyDelta
		contents []byte
		want     string
		found    bool
		wantErr  bool
	}{
		{"npm dependencies", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "dependencies.nx", Package: "nx"}, npmContents, "22.7.7", true, false},
		{"npm dev", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "devDependencies.jest", Package: "jest"}, npmContents, "29.0.0", true, false},
		{"npm peer", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "peerDependencies.react", Package: "react"}, npmContents, "18.0.0", true, false},
		{"npm optional", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "optionalDependencies.fsevents", Package: "fsevents"}, npmContents, "2.3.0", true, false},
		{"npm missing", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "dependencies.missing", Package: "missing"}, npmContents, "", false, false},
		{"npm unknown field", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "resolutions.nx", Package: "nx"}, npmContents, "", false, false},
		{"npm selector shape", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "dependencies", Package: "nx"}, npmContents, "", false, false},
		{"npm invalid json", SupersessionDependencyDelta{Ecosystem: "npm", Selector: "dependencies.nx", Package: "nx"}, []byte("{"), "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, found, err := worktreebranches.DependencyManifestValue(tc.delta, tc.contents)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if value != tc.want || found != tc.found {
				t.Fatalf("value = %q found = %t, want %q/%t", value, found, tc.want, tc.found)
			}
		})
	}

	goManifest := []byte("module example.com/app\n\ngo 1.21\n\nrequire example.com/nx v1.2.3\n")
	goDelta := SupersessionDependencyDelta{Ecosystem: "go", Selector: "require:example.com/nx", Package: "example.com/nx"}
	if value, found, err := worktreebranches.DependencyManifestValue(goDelta, goManifest); err != nil || !found || value != "v1.2.3" {
		t.Fatalf("go value = %q found = %t err = %v", value, found, err)
	}
	absent := SupersessionDependencyDelta{Ecosystem: "go", Selector: "require:example.com/missing", Package: "example.com/missing"}
	if _, found, err := worktreebranches.DependencyManifestValue(absent, goManifest); err != nil || found {
		t.Fatalf("absent go module found = %t err = %v", found, err)
	}
	renamed := SupersessionDependencyDelta{Ecosystem: "go", Selector: "require:example.com/other", Package: "example.com/nx"}
	if _, found, err := worktreebranches.DependencyManifestValue(renamed, goManifest); err != nil || found {
		t.Fatalf("renamed go selector found = %t err = %v", found, err)
	}
	if _, _, err := worktreebranches.DependencyManifestValue(goDelta, []byte("not a module\n")); err == nil {
		t.Fatal("invalid go manifest should error")
	}
	if _, _, err := worktreebranches.DependencyManifestValue(SupersessionDependencyDelta{Ecosystem: "cargo"}, nil); err == nil {
		t.Fatal("unsupported ecosystem should error")
	}
}

func TestWtLogCovSameSupersessionReceipt(t *testing.T) {
	t.Parallel()
	left := &SupersessionReceipt{Version: 1, Repository: "acme/app"}
	if !worktreeproof.SameSupersessionReceipt(nil, nil) {
		t.Fatal("two nil receipts should match")
	}
	if worktreeproof.SameSupersessionReceipt(left, nil) || worktreeproof.SameSupersessionReceipt(nil, left) {
		t.Fatal("nil and non-nil receipts must not match")
	}
	right := &SupersessionReceipt{Version: 1, Repository: "acme/app"}
	if !worktreeproof.SameSupersessionReceipt(left, right) {
		t.Fatal("equal receipts should match")
	}
	right.Repository = "acme/other"
	if worktreeproof.SameSupersessionReceipt(left, right) {
		t.Fatal("different receipts must not match")
	}
}

func TestWtLogCovValidateAuthoritativeSourcePullRequest(t *testing.T) {
	t.Parallel()
	entry := ListResult{Repository: "acme/app", HeadSHA: "head-1", OpenPullRequest: dependencyTestPullRequest("head-1")}
	receipt := SupersessionReceipt{
		OriginalPR: "https://github.com/acme/app/pull/17", OriginalPRNumber: 17,
		OriginalPRRepository: "acme/app", OriginalPRHead: "head-1", OriginalHead: "head-1",
	}
	if rejection := worktreebranches.ValidateAuthoritativeSourcePullRequest(receipt, supersessionEntry(entry)); rejection != "" {
		t.Fatalf("authoritative source PR rejected: %s", rejection)
	}
	cases := []struct {
		name    string
		mutate  func(*SupersessionReceipt, *ListResult)
		wantSub string
	}{
		{"no PR", func(r *SupersessionReceipt, e *ListResult) { e.OpenPullRequest = nil }, "no authoritative source pull request"},
		{"incomplete PR", func(r *SupersessionReceipt, e *ListResult) { e.OpenPullRequest.URL = "" }, "missing URL, number, repository, or head"},
		{"url mismatch", func(r *SupersessionReceipt, e *ListResult) { r.OriginalPR = "https://github.com/acme/app/pull/18" }, "does not match authoritative source pull request URL"},
		{"number mismatch", func(r *SupersessionReceipt, e *ListResult) { r.OriginalPRNumber = 18 }, "does not match authoritative source pull request number"},
		{"repository mismatch", func(r *SupersessionReceipt, e *ListResult) { r.OriginalPRRepository = "acme/other" }, "does not match authoritative source repository"},
		{"head mismatch", func(r *SupersessionReceipt, e *ListResult) { r.OriginalPRHead = "head-2" }, "does not match authoritative source head"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mutatedReceipt, mutatedEntry := receipt, entry
			mutatedEntry.OpenPullRequest = dependencyTestPullRequest("head-1")
			tc.mutate(&mutatedReceipt, &mutatedEntry)
			if rejection := worktreebranches.ValidateAuthoritativeSourcePullRequest(mutatedReceipt, supersessionEntry(mutatedEntry)); !strings.Contains(rejection, tc.wantSub) {
				t.Fatalf("rejection %q does not contain %q", rejection, tc.wantSub)
			}
		})
	}
}

func TestWtLogCovValidateDependencyDeltasWrapper(t *testing.T) {
	fixture := newGitFixture(t)
	targetHead := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	entry := ListResult{
		Task: "plain-task", Repository: "acme/app", Branch: "wb/plain-task",
		CanonicalDir: fixture.canonical, WorktreeDir: t.TempDir(),
		HeadSHA: targetHead, RemoteTargetSHA: targetHead,
	}
	if err := ValidateDependencyDeltas(context.Background(), SupersessionReceipt{Version: 1}, entry); err != nil {
		t.Fatalf("generic receipt should not require dependency proof: %v", err)
	}
	withEvidence := SupersessionReceipt{Version: 1, DependencyDeltasComplete: true}
	err := ValidateDependencyDeltas(context.Background(), withEvidence, entry)
	if err == nil || !strings.Contains(err.Error(), "requires original_pr") {
		t.Fatalf("dependency evidence without original_pr error = %v", err)
	}
	campaign := ListResult{Task: "deps-upgrade", Branch: "wb/deps/upgrade"}
	err = ValidateDependencyDeltas(context.Background(), SupersessionReceipt{Version: 1}, campaign)
	if err == nil || !strings.Contains(err.Error(), "dependency campaign supersession requires original_pr") {
		t.Fatalf("campaign receipt error = %v", err)
	}
}

func TestWtLogCovIsDependencyManifestOrImporter(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"package.json", "package-lock.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "pnpm-workspace.yml", "yarn.lock", "go.mod", "go.sum", "apps/web/package.json", ".github/workflows/ci.yml", ".github/workflows/ci.yaml"} {
		if !worktreebranches.IsDependencyManifestOrImporter(want) {
			t.Errorf("isDependencyManifestOrImporter(%q) = false, want true", want)
		}
	}
	for _, unwanted := range []string{"README.md", ".github/workflows/ci.txt", "src/package.json.bak", "cmd/main.go", ""} {
		if worktreebranches.IsDependencyManifestOrImporter(unwanted) {
			t.Errorf("isDependencyManifestOrImporter(%q) = true, want false", unwanted)
		}
	}
}

func TestWtLogCovDependencyAuditRendersEmptyAndSorted(t *testing.T) {
	t.Parallel()
	empty := SupersessionReceipt{OriginalPR: "https://github.com/acme/app/pull/17"}
	jsonBytes, err := empty.DependencyAuditJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jsonBytes), `"dependency_deltas": null`) {
		t.Fatalf("empty audit JSON = %s", jsonBytes)
	}
	markdown := empty.DependencyAuditMarkdown()
	if !strings.Contains(markdown, "# Dependency supersession audit") || !strings.Contains(markdown, "| Consumer | Package |") {
		t.Fatalf("empty audit markdown = %s", markdown)
	}
	if strings.Contains(markdown, "| `acme/app` |") {
		t.Fatalf("empty audit markdown rendered a row:\n%s", markdown)
	}
}
