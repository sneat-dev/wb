package deps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file raises statement coverage for internal/deps/drift.go,
// drift_go.go, drift_npm.go and drift_report.go. Every helper it introduces
// carries the depsCov prefix so it cannot collide with a concurrently edited
// sibling test file.

// depsCovWriteFakeExecutable installs a fake POSIX-shell executable named
// `name` at the front of PATH for one test only. The drift code shells out to
// `go` and `pnpm`, and these tests must stay hermetic, so each test scripts
// the tool's observable output instead of requiring it to be installed.
// Because it mutates PATH via t.Setenv the caller must not run in parallel.
func depsCovWriteFakeExecutable(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executable shim requires a POSIX shell")
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, name)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// depsCovSymlink creates a symlink or skips the calling subtest on platforms
// where unprivileged symlink creation is impossible (notably Windows without
// developer mode). It exists so the "manifest file disappeared between the
// walk and the read" error branches stay reachable.
func depsCovSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
}

func depsCovDriftEvidence(version, reason string, at time.Time) VersionEvidence {
	return VersionEvidence{Value: version, Reason: reason, ObservedAt: at, Source: "depsCov fixture"}
}

func depsCovDriftGoRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relative, contents := range files {
		writeTestFile(t, filepath.Join(dir, relative), contents)
	}
	return dir
}

func depsCovDriftFindGroup(t *testing.T, groups []DriftVersionGroup, dependency string) DriftVersionGroup {
	t.Helper()
	for _, group := range groups {
		if group.Dependency == dependency {
			return group
		}
	}
	t.Fatalf("missing group %s in %+v", dependency, groups)
	return DriftVersionGroup{}
}

func depsCovDriftGoDependency(dependency string, selected, declared VersionEvidence, latest *VersionEvidence) DriftDependency {
	return DriftDependency{Dependency: dependency, Manifest: "go.mod", Selected: selected, Declared: declared, Latest: latest}
}

// TestDepsCovDriftAnalyzeDriftDefaultsAndOversubscribedParallelism asserts the
// documented defaults (go ecosystem, main base ref, wall-clock observation)
// and that asking for more workers than repositories is clamped rather than
// spawning idle goroutines.
func TestDepsCovDriftAnalyzeDriftDefaultsAndOversubscribedParallelism(t *testing.T) {
	t.Parallel()
	checkout := depsCovDriftGoRepository(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n\nrequire example.com/sdk v1.0.0\n",
	})
	before := time.Now().UTC().Add(-time.Minute)
	report, err := AnalyzeDrift(context.Background(), []Repository{
		{Slug: "acme/app", Path: checkout},
	}, DriftOptions{Parallel: 50})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ecosystem != EcosystemGo {
		t.Fatalf("ecosystem = %q, want the empty ecosystem to default to go", report.Ecosystem)
	}
	if report.Mode != "offline" {
		t.Fatalf("mode = %q, want offline", report.Mode)
	}
	if report.BaseRef != "main" {
		t.Fatalf("base ref = %q, want main", report.BaseRef)
	}
	if report.ObservedAt.IsZero() || report.ObservedAt.Before(before) {
		t.Fatalf("observed at = %v, want a wall-clock timestamp", report.ObservedAt)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Repository != "acme/app" {
		t.Fatalf("repositories = %+v", report.Repositories)
	}
}

// TestDepsCovDriftAnalyzeDriftReportsSkippedAndErroredRepositories asserts the
// three ways a selected repository can fail to produce dependency evidence:
// no local clone path (a discovery skip, sorted and reported), an unreadable
// checkout (an error row that fails the gate), and a repository with no slug
// at all (silently dropped rather than reported as an unnamed repository).
func TestDepsCovDriftAnalyzeDriftReportsSkippedAndErroredRepositories(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "not-cloned")
	report, err := AnalyzeDrift(context.Background(), []Repository{
		{Slug: "acme/beta", Path: ""},
		{Slug: "acme/alpha", Path: ""},
		{Slug: "acme/broken", Path: missing},
	}, DriftOptions{Parallel: 1, Now: driftObservedAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DiscoverySkips) != 2 {
		t.Fatalf("discovery skips = %+v", report.DiscoverySkips)
	}
	if report.DiscoverySkips[0].Repository != "acme/alpha" || report.DiscoverySkips[1].Repository != "acme/beta" {
		t.Fatalf("discovery skips must be sorted: %+v", report.DiscoverySkips)
	}
	if !strings.Contains(report.DiscoverySkips[0].Reason, "no local clone path") {
		t.Fatalf("skip reason = %q", report.DiscoverySkips[0].Reason)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Status != "error" {
		t.Fatalf("repositories = %+v, want one error row", report.Repositories)
	}
	if report.Repositories[0].Reason == "" {
		t.Fatal("an errored repository must explain why")
	}
	if report.Summary.Error != 1 {
		t.Fatalf("summary.error = %d, want 1", report.Summary.Error)
	}
	if !DriftFailedWith(report, false, false) {
		t.Fatal("an inspection error must fail the run even without --fail-on-drift")
	}

	t.Run("repository without a slug is not reported", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"go.mod": "module example.com/anon\n\ngo 1.22\n\nrequire example.com/sdk v1.0.0\n",
		})
		anonymous, err := AnalyzeDrift(context.Background(), []Repository{{Slug: "", Path: checkout}}, DriftOptions{Parallel: 1, Now: driftObservedAt})
		if err != nil {
			t.Fatal(err)
		}
		if len(anonymous.Repositories) != 0 || len(anonymous.Groups) != 0 {
			t.Fatalf("anonymous repository leaked into the report: %+v", anonymous)
		}
	})
}

// TestDepsCovDriftFailedWithGates pins the exit-code ladder: an inspection
// error always fails, a per-repository error row fails even when the summary
// was not populated, --fail-on-behind is independent from --fail-on-drift, and
// a clean report never fails.
func TestDepsCovDriftFailedWithGates(t *testing.T) {
	t.Parallel()
	if !DriftFailedWith(DriftReport{Summary: DriftSummary{Error: 1}}, false, false) {
		t.Fatal("a summary error must fail")
	}
	if !DriftFailedWith(DriftReport{
		Repositories: []DriftRepository{{Repository: "acme/x", Status: "error"}},
	}, false, false) {
		t.Fatal("an error repository row must fail even when the summary was not counted")
	}
	if DriftFailedWith(DriftReport{Summary: DriftSummary{Behind: 1}}, false, false) {
		t.Fatal("behind-latest must not fail without --fail-on-behind")
	}
	if !DriftFailedWith(DriftReport{Summary: DriftSummary{Behind: 1}}, false, true) {
		t.Fatal("--fail-on-behind must fail when a group lags latest")
	}
	if DriftFailedWith(DriftReport{Summary: DriftSummary{Divergent: 3}}, false, false) {
		t.Fatal("divergence must not fail without --fail-on-drift")
	}
	for _, summary := range []DriftSummary{{Divergent: 1}, {Replaced: 1}, {MajorSplit: 1}} {
		if !DriftFailedWith(DriftReport{Summary: summary}, true, false) {
			t.Fatalf("summary %+v must fail under --fail-on-drift", summary)
		}
	}
}

// TestDepsCovDriftSummarizeDriftCountsEveryClassification asserts each
// classification bucket, the orthogonal Behind counter, and the fact that a
// repository-level error increments the same Error bucket.
func TestDepsCovDriftSummarizeDriftCountsEveryClassification(t *testing.T) {
	t.Parallel()
	report := DriftReport{
		Groups: []DriftVersionGroup{
			{Dependency: "a", Classification: DriftConverged},
			{Dependency: "b", Classification: DriftDivergent},
			{Dependency: "c", Classification: DriftReplaced},
			{Dependency: "d", Classification: DriftMajorPathSplit},
			{Dependency: "e", Classification: DriftBehindLatest, Behind: true},
			{Dependency: "f", Classification: DriftUnavailable},
			{Dependency: "g", Classification: DriftError},
			{Dependency: "h", Classification: DriftConverged, Behind: true},
		},
		Repositories: []DriftRepository{
			{Repository: "acme/ok", Status: "ok"},
			{Repository: "acme/bad", Status: "error"},
		},
	}
	summary := summarizeDrift(report)
	want := DriftSummary{
		Repositories: 2, Dependencies: 8,
		Converged: 2, Divergent: 1, Replaced: 1, MajorSplit: 1,
		Unavailable: 1, Error: 2, Behind: 2,
	}
	if summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
}

// TestDepsCovDriftClassifyDriftGroupsDeclaredAndUnknownFallbacks asserts the
// fallback ladder from a selected version through the declared specifier to
// "(unknown)", that an errored repository is excluded, that the --dependency
// selector drops non-matching rows, and that offline groups record why latest
// was not queried.
func TestDepsCovDriftClassifyDriftGroupsDeclaredAndUnknownFallbacks(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	repositories := []DriftRepository{
		{Repository: "acme/broken", Status: "error", Dependencies: []DriftDependency{
			depsCovDriftGoDependency("example.com/should-not-appear", depsCovDriftEvidence("v9.9.9", "", at), VersionEvidence{}, nil),
		}},
		{Repository: "acme/app", Status: "ok", Dependencies: []DriftDependency{
			depsCovDriftGoDependency("example.com/filtered", depsCovDriftEvidence("v1.0.0", "", at), VersionEvidence{}, nil),
			depsCovDriftGoDependency("example.com/sdk", depsCovDriftEvidence("v1.0.0", "", at), depsCovDriftEvidence("v1.0.0", "", at), nil),
			depsCovDriftGoDependency("example.com/decl", VersionEvidence{}, depsCovDriftEvidence("v1.2.0", "", at), nil),
			depsCovDriftGoDependency("example.com/unk", VersionEvidence{}, VersionEvidence{}, nil),
			depsCovDriftGoDependency("example.com/err", VersionEvidence{Reason: "go list -m inspection failed: module cache corrupt"}, depsCovDriftEvidence("v3.0.0", "", at), nil),
		}},
	}
	groups := classifyDriftGroups(repositories, DriftOptions{
		Ecosystem:    EcosystemGo,
		Dependencies: []string{"example.com/sdk", "example.com/decl", "example.com/unk", "example.com/err"},
	}, at)
	if len(groups) != 4 {
		t.Fatalf("groups = %+v, want the errored repository and the filtered dependency dropped", groups)
	}

	selected := depsCovDriftFindGroup(t, groups, "example.com/sdk")
	if selected.Classification != DriftConverged || len(selected.Versions) != 1 || selected.Versions[0].Kind != "selected" || selected.Versions[0].Version != "v1.0.0" {
		t.Fatalf("selected group = %+v", selected)
	}
	if selected.Latest == nil || selected.Latest.Source != "not_queried_offline" {
		t.Fatalf("offline latest = %+v, want the not_queried_offline marker", selected.Latest)
	}

	declared := depsCovDriftFindGroup(t, groups, "example.com/decl")
	if declared.Versions[0].Kind != "declared" || declared.Versions[0].Version != "v1.2.0" {
		t.Fatalf("declared group = %+v", declared)
	}

	unknown := depsCovDriftFindGroup(t, groups, "example.com/unk")
	if unknown.Versions[0].Kind != "declared" || unknown.Versions[0].Version != "(unknown)" {
		t.Fatalf("unknown group = %+v", unknown)
	}

	failed := depsCovDriftFindGroup(t, groups, "example.com/err")
	if failed.Classification != DriftError {
		t.Fatalf("failed group = %+v, want an error classification from the inspection failure", failed)
	}
}

// TestDepsCovDriftClassifyDriftGroupsUnavailableAndBehindLatest asserts that
// an online lookup which returns no value is reported as unavailable, and that
// a group whose observations lag the published latest is classified
// behind_latest with the offending repositories named.
func TestDepsCovDriftClassifyDriftGroupsUnavailableAndBehindLatest(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	unavailable := classifyDriftGroups([]DriftRepository{
		{Repository: "acme/app", Status: "ok", Dependencies: []DriftDependency{
			depsCovDriftGoDependency("example.com/net",
				depsCovDriftEvidence("v1.0.0", "", at),
				depsCovDriftEvidence("v1.0.0", "", at),
				&VersionEvidence{ObservedAt: at, Source: "go list -m -json", Reason: "module proxy unreachable"}),
		}},
	}, DriftOptions{Ecosystem: EcosystemGo, Online: true}, at)
	if group := depsCovDriftFindGroup(t, unavailable, "example.com/net"); group.Classification != DriftUnavailable {
		t.Fatalf("group = %+v, want unavailable", group)
	}

	latest := &VersionEvidence{ObservedAt: at, Source: "go list -m -json", Value: "v2.0.0"}
	behind := classifyDriftGroups([]DriftRepository{
		{Repository: "acme/a", Status: "ok", Dependencies: []DriftDependency{
			depsCovDriftGoDependency("example.com/sdk", depsCovDriftEvidence("v1.0.0", "", at), depsCovDriftEvidence("v1.0.0", "", at), latest),
		}},
		{Repository: "acme/b", Status: "ok", Dependencies: []DriftDependency{
			depsCovDriftGoDependency("example.com/sdk", VersionEvidence{}, depsCovDriftEvidence("v1.0.0", "", at), latest),
		}},
	}, DriftOptions{Ecosystem: EcosystemGo, Online: true}, at)
	group := depsCovDriftFindGroup(t, behind, "example.com/sdk")
	if group.Classification != DriftBehindLatest || !group.Behind {
		t.Fatalf("group = %+v, want behind_latest", group)
	}
	if group.BehindReason == "" || !strings.Contains(group.BehindReason, "v2.0.0") {
		t.Fatalf("behind reason = %q", group.BehindReason)
	}
	if len(group.BehindRepositories) != 2 || group.BehindRepositories[0] != "acme/a" || group.BehindRepositories[1] != "acme/b" {
		t.Fatalf("behind repositories = %+v", group.BehindRepositories)
	}
	// A selected and a declared observation of the same version collapse into
	// one distinct version but must stay separate uses, ordered by kind.
	if len(group.Versions) != 2 || group.Versions[0].Kind != "declared" || group.Versions[1].Kind != "selected" {
		t.Fatalf("versions = %+v, want declared before selected", group.Versions)
	}
	if distinctSelectedVersions(group.Versions) != 1 {
		t.Fatalf("distinct versions = %d, want 1", distinctSelectedVersions(group.Versions))
	}
}

// TestDepsCovDriftDistinctSelectedVersionsIgnoresNonVersionUses asserts that
// only selected/declared uses carrying a real version are counted: unknown
// placeholders, empty values and non-version kinds are all ignored, while the
// same version observed by two repositories counts once.
func TestDepsCovDriftDistinctSelectedVersionsIgnoresNonVersionUses(t *testing.T) {
	t.Parallel()
	versions := []DriftVersionUse{
		{Version: "1.0.0", Kind: "selected"},
		{Version: "1.0.0", Kind: "declared"},
		{Version: "2.0.0", Kind: "selected"},
		{Version: "", Kind: "selected"},
		{Version: "(unknown)", Kind: "declared"},
		{Version: "9.9.9", Kind: "latest"},
		{Version: "8.8.8", Kind: ""},
	}
	if got := distinctSelectedVersions(versions); got != 2 {
		t.Fatalf("distinct = %d, want 2", got)
	}
	if got := distinctSelectedVersions(nil); got != 0 {
		t.Fatalf("distinct(nil) = %d", got)
	}
}

// TestDepsCovDriftMatchesAnyGlobSemantics asserts literal comparison, path.Match
// semantics where "*" never crosses "/", and the malformed-pattern fallback.
func TestDepsCovDriftMatchesAnyGlobSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    string
		patterns []string
		want     bool
	}{
		{name: "empty patterns skipped", value: "example.com/sdk", patterns: []string{"", "   ", "example.com/*"}, want: true},
		{name: "exact", value: "example.com/sdk", patterns: []string{"example.com/sdk"}, want: true},
		{name: "glob does not cross slash", value: "example.com/sdk/v2", patterns: []string{"example.com/*"}, want: false},
		{name: "malformed pattern is not a silent match", value: "anything", patterns: []string{"["}, want: false},
		{name: "no patterns", value: "x", want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := matchesAnyGlob(test.value, test.patterns); got != test.want {
				t.Fatalf("matchesAnyGlob(%q, %v) = %v, want %v", test.value, test.patterns, got, test.want)
			}
		})
	}
}

// TestDepsCovDriftObservationLagsLatestMatrix covers every verdict the
// behind-latest predicate can reach for both ecosystems: exact comparison for
// locked/selected versions, the Go "specifier WB cannot read is never behind"
// rule, and npm's evaluated-range judgement.
func TestDepsCovDriftObservationLagsLatestMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                            string
		ecosystem                       Ecosystem
		version, kind, declared, latest string
		want                            bool
	}{
		{name: "selected exact older", ecosystem: EcosystemGo, version: "1.0.0", kind: "selected", latest: "2.0.0", want: true},
		{name: "selected exact newer", ecosystem: EcosystemGo, version: "3.0.0", kind: "selected", latest: "2.0.0", want: false},
		{name: "go declared exact older", ecosystem: EcosystemGo, version: "v1.0.0", kind: "declared", latest: "v2.0.0", want: true},
		{name: "go unreadable specifier is never behind", ecosystem: EcosystemGo, version: "^1.0.0", kind: "declared", declared: "^1.0.0", latest: "v2.0.0", want: false},
		{name: "npm exact older", ecosystem: EcosystemNPM, version: "1.0.0", kind: "declared", declared: "1.0.0", latest: "2.0.0", want: true},
		{name: "npm range cannot reach latest", ecosystem: EcosystemNPM, version: "", kind: "declared", declared: "^1.0.0", latest: "2.0.0", want: true},
		{name: "npm range admits latest", ecosystem: EcosystemNPM, version: "", kind: "declared", declared: "^1.0.0", latest: "1.5.0", want: false},
		{name: "npm protocol specifier is unevaluated", ecosystem: EcosystemNPM, version: "", kind: "declared", declared: "workspace:*", latest: "2.0.0", want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := observationLagsLatest(test.ecosystem, test.version, test.kind, test.declared, test.latest); got != test.want {
				t.Fatalf("observationLagsLatest(%s, %q, %q, %q, %q) = %v, want %v",
					test.ecosystem, test.version, test.kind, test.declared, test.latest, got, test.want)
			}
		})
	}
}

// TestDepsCovDriftPathHelpers covers the pure path helpers: major-version
// family collapsing for versioned module paths, the fallback for paths with no
// usable family prefix, the relative-manifest failure fallback, and module
// directory resolution for root and nested manifests.
func TestDepsCovDriftPathHelpers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		modulePath string
		want       string
	}{
		{modulePath: "example.com/foo/v2", want: "example.com/foo"},
		{modulePath: "example.com/foo", want: "example.com/foo"},
		{modulePath: "/v2", want: "/v2"},
		{modulePath: "", want: ""},
	} {
		if got := modulePathFamily(test.modulePath); got != test.want {
			t.Fatalf("modulePathFamily(%q) = %q, want %q", test.modulePath, got, test.want)
		}
	}

	if got := relativeManifest("/abs/repo", "relative/path"); got != "relative/path" {
		t.Fatalf("relativeManifest with a mismatched base = %q, want the manifest path unchanged", got)
	}
	if got := relativeManifest("/abs/repo", "/abs/repo/sub/go.mod"); got != "sub/go.mod" {
		t.Fatalf("relativeManifest = %q, want sub/go.mod", got)
	}

	if got := joinModuleDir("/repo", "go.mod"); got != "/repo" {
		t.Fatalf("joinModuleDir(root manifest) = %q, want /repo", got)
	}
	if got, want := joinModuleDir("/repo", "sub/go.mod"), filepath.Join("/repo", "sub"); got != want {
		t.Fatalf("joinModuleDir(nested manifest) = %q, want %q", got, want)
	}
}

// TestDepsCovDriftInspectGoDriftRepositoryErrorsAndReplaceRows asserts the
// behaviour of the Go manifest walker: an unreadable checkout is an error, an
// unparsable go.mod is an error naming the manifest, a replace-only row is
// reported with its redirect, the selector can drop it, and the same module
// named by two manifests produces two rows ordered by manifest.
func TestDepsCovDriftInspectGoDriftRepositoryErrorsAndReplaceRows(t *testing.T) {
	t.Parallel()
	t.Run("missing checkout", func(t *testing.T) {
		t.Parallel()
		_, err := inspectGoDriftRepository(context.Background(), Repository{
			Slug: "acme/missing", Path: filepath.Join(t.TempDir(), "absent"),
		}, DriftOptions{}, driftObservedAt())
		if err == nil {
			t.Fatal("walking a missing checkout must fail")
		}
	})

	t.Run("unparsable go.mod", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"go.mod": "module example.com/app\n\nrequire (\n",
		})
		_, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt())
		if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
			t.Fatalf("err = %v, want a parse error naming go.mod", err)
		}
	})

	t.Run("go.mod disappears before the read", func(t *testing.T) {
		t.Parallel()
		checkout := t.TempDir()
		depsCovSymlink(t, filepath.Join(checkout, "gone.mod"), filepath.Join(checkout, "go.mod"))
		if _, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt()); err == nil {
			t.Fatal("an unreadable go.mod must fail the inspection")
		}
	})

	t.Run("replace without a require row", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"go.mod": "module example.com/app\n\ngo 1.22\n\nreplace example.com/money => example.com/fork v1.0.0\n",
		})
		report, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Dependencies) != 1 {
			t.Fatalf("dependencies = %+v, want one replace-only row", report.Dependencies)
		}
		dependency := report.Dependencies[0]
		if dependency.Dependency != "example.com/money" || dependency.Replacement == nil || dependency.Replacement.NewPath != "example.com/fork" {
			t.Fatalf("dependency = %+v", dependency)
		}
		if !strings.Contains(dependency.Declared.Reason, "replaced without a matching require row") {
			t.Fatalf("declared = %+v", dependency.Declared)
		}
		if dependency.Selected.Source != "replace" {
			t.Fatalf("selected = %+v, want the replace redirect recorded", dependency.Selected)
		}
		if dependency.Latest == nil || dependency.Latest.Source != "not_queried_offline" {
			t.Fatalf("latest = %+v", dependency.Latest)
		}
	})

	t.Run("replace-only row filtered by the selector", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"go.mod": "module example.com/app\n\ngo 1.22\n\nreplace example.com/money => example.com/fork v1.0.0\n",
		})
		report, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{
			Dependencies: []string{"example.com/other"},
		}, driftObservedAt())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Dependencies) != 0 {
			t.Fatalf("dependencies = %+v, want the replace-only row filtered out", report.Dependencies)
		}
	})

	t.Run("same module in two manifests", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"go.mod":     "module example.com/app\n\ngo 1.22\n\nrequire example.com/sdk v1.0.0\n",
			"sub/go.mod": "module example.com/sub\n\ngo 1.22\n\nrequire example.com/sdk v1.1.0\n",
		})
		report, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Dependencies) != 2 {
			t.Fatalf("dependencies = %+v, want one row per manifest", report.Dependencies)
		}
		if report.Dependencies[0].Manifest != "go.mod" || report.Dependencies[1].Manifest != "sub/go.mod" {
			t.Fatalf("dependencies must be ordered by manifest: %+v", report.Dependencies)
		}
	})
}

// TestDepsCovDriftInspectGoDriftRepositoryOnlineRecordsLatest asserts that an
// online inspection attaches the published latest version observed from the
// module proxy, using a fake `go` executable so no network is touched.
func TestDepsCovDriftInspectGoDriftRepositoryOnlineRecordsLatest(t *testing.T) {
	depsCovWriteFakeExecutable(t, "go", `
for arg in "$@"; do
  if [ "$arg" = "-json" ]; then
    printf '%s\n' '{"Version":"v1.9.9"}'
    exit 0
  fi
done
printf '%s\n' 'v9.9.9'
exit 0
`)
	checkout := depsCovDriftGoRepository(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n\nrequire example.com/sdk v1.0.0\n",
	})
	report, err := inspectGoDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{
		Online: true, Timeout: time.Minute,
	}, driftObservedAt())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf("dependencies = %+v", report.Dependencies)
	}
	dependency := report.Dependencies[0]
	if dependency.Latest == nil || dependency.Latest.Value != "v1.9.9" {
		t.Fatalf("latest = %+v, want the fake proxy version", dependency.Latest)
	}
	if dependency.Selected.Value != "v9.9.9" {
		t.Fatalf("selected = %+v, want the fake go list selection", dependency.Selected)
	}
}

// TestDepsCovDriftSelectedGoModuleVersionFallbacks asserts that the selected
// version comes from `go list -m` when it works, and falls back to the
// declared require version with an explicit reason when go is unavailable or
// returns nothing.
func TestDepsCovDriftSelectedGoModuleVersionFallbacks(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	t.Run("go list wins", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "printf '%s\\n' 'v1.2.3'\nexit 0\n")
		evidence := selectedGoModuleVersion(context.Background(), t.TempDir(), "example.com/sdk", "v1.0.0", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "v1.2.3" || evidence.Source != "go list -m" {
			t.Fatalf("evidence = %+v", evidence)
		}
	})
	t.Run("declared fallback on failure", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "printf 'go: module lookup failed\\n' >&2\nexit 1\n")
		evidence := selectedGoModuleVersion(context.Background(), t.TempDir(), "example.com/sdk", "v1.0.0", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "v1.0.0" || evidence.Source != "declared_fallback" {
			t.Fatalf("evidence = %+v", evidence)
		}
		if !strings.Contains(evidence.Reason, "go list -m unavailable") || !strings.Contains(evidence.Reason, "module lookup failed") {
			t.Fatalf("reason = %q", evidence.Reason)
		}
	})
	t.Run("declared fallback on empty output", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "exit 0\n")
		evidence := selectedGoModuleVersion(context.Background(), t.TempDir(), "example.com/sdk", "v1.0.0", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "v1.0.0" || evidence.Source != "declared_fallback" {
			t.Fatalf("evidence = %+v", evidence)
		}
		if !strings.Contains(evidence.Reason, "empty version") {
			t.Fatalf("reason = %q", evidence.Reason)
		}
	})
}

// TestDepsCovDriftObserveLatestGoVersion asserts the online latest lookup
// records the proxy version on success and an explicit reason when the proxy
// lookup fails.
func TestDepsCovDriftObserveLatestGoVersion(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	t.Run("valid version", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "printf '%s\\n' '{\"Version\":\"v1.2.3\"}'\nexit 0\n")
		evidence := observeLatestGoVersion(context.Background(), "example.com/sdk", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "v1.2.3" {
			t.Fatalf("evidence = %+v", evidence)
		}
		if !strings.Contains(evidence.Source, "example.com/sdk@latest") {
			t.Fatalf("source = %q", evidence.Source)
		}
	})
	t.Run("lookup failure", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "printf 'go: proxy unreachable\\n' >&2\nexit 1\n")
		evidence := observeLatestGoVersion(context.Background(), "example.com/sdk", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "" {
			t.Fatalf("evidence = %+v, want no value on failure", evidence)
		}
		if !strings.Contains(evidence.Reason, "proxy unreachable") {
			t.Fatalf("reason = %q, want the proxy failure surfaced", evidence.Reason)
		}
	})
	t.Run("malformed response", func(t *testing.T) {
		depsCovWriteFakeExecutable(t, "go", "printf 'not json'\nexit 0\n")
		evidence := observeLatestGoVersion(context.Background(), "example.com/sdk", DriftOptions{Timeout: time.Minute}, at)
		if evidence.Value != "" || evidence.Reason == "" {
			t.Fatalf("evidence = %+v, want a reason for the malformed response", evidence)
		}
	})
}

// TestDepsCovDriftInspectNpmDriftRepositoryManifestErrors asserts the npm
// walker's failure modes: a missing checkout, an unparsable package.json, a
// workspace reference excluded by the selector, and manifests that vanish
// between the walk and the read.
func TestDepsCovDriftInspectNpmDriftRepositoryManifestErrors(t *testing.T) {
	t.Parallel()
	t.Run("missing checkout", func(t *testing.T) {
		t.Parallel()
		_, err := inspectNpmDriftRepository(context.Background(), Repository{
			Slug: "acme/missing", Path: filepath.Join(t.TempDir(), "absent"),
		}, DriftOptions{}, driftObservedAt(), newNpmLatestVersions())
		if err == nil {
			t.Fatal("walking a missing checkout must fail")
		}
	})

	t.Run("unparsable package.json", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{"package.json": "{"})
		_, err := inspectNpmDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt(), newNpmLatestVersions())
		if err == nil || !strings.Contains(err.Error(), "parse package.json") {
			t.Fatalf("err = %v, want a parse error naming package.json", err)
		}
	})

	t.Run("workspace reference filtered by the selector", func(t *testing.T) {
		t.Parallel()
		checkout := depsCovDriftGoRepository(t, map[string]string{
			"pnpm-workspace.yaml": "overrides:\n  '@sneat/core': 1.0.0\n",
		})
		report, err := inspectNpmDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{
			Ecosystem: EcosystemNPM, Dependencies: []string{"@other/pkg"},
		}, driftObservedAt(), newNpmLatestVersions())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Dependencies) != 0 {
			t.Fatalf("dependencies = %+v, want the workspace override filtered out", report.Dependencies)
		}
	})

	t.Run("package.json disappears before the read", func(t *testing.T) {
		t.Parallel()
		checkout := t.TempDir()
		depsCovSymlink(t, filepath.Join(checkout, "gone.json"), filepath.Join(checkout, "package.json"))
		_, err := inspectNpmDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{}, driftObservedAt(), newNpmLatestVersions())
		if err == nil {
			t.Fatal("an unreadable package.json must fail the inspection")
		}
	})

	t.Run("pnpm-workspace.yaml disappears before the read", func(t *testing.T) {
		t.Parallel()
		checkout := t.TempDir()
		depsCovSymlink(t, filepath.Join(checkout, "gone.yaml"), filepath.Join(checkout, "pnpm-workspace.yaml"))
		_, err := inspectNpmDriftRepository(context.Background(), Repository{Slug: "acme/app", Path: checkout}, DriftOptions{Ecosystem: EcosystemNPM}, driftObservedAt(), newNpmLatestVersions())
		if err == nil {
			t.Fatal("an unreadable pnpm-workspace.yaml must fail the inspection")
		}
	})
}

// TestDepsCovDriftNpmSelectedVersionEvidenceMatrix pins every selection
// outcome: no governing lockfile, a lockfile that does not resolve the
// package, a lockfile that could not be indexed, conflicting importers, and a
// clean single resolution.
func TestDepsCovDriftNpmSelectedVersionEvidenceMatrix(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	scopes := map[string]npmLockScope{
		"packages/app": {Directory: "packages/app", Versions: map[string]npmLockedVersion{
			"@sneat/conflict": {Values: []string{"1.0.0", "2.0.0"}, Source: "packages/app/pnpm-lock.yaml"},
			"@sneat/ok":       {Values: []string{"1.2.3"}, Source: "packages/app/pnpm-lock.yaml"},
		}},
		"packages/broken": {Directory: "packages/broken", Versions: map[string]npmLockedVersion{}, Reason: "no importers section"},
		"":                {Directory: "", Versions: map[string]npmLockedVersion{}},
	}

	ungoverned := npmSelectedVersion("@sneat/core", "package.json", "^1.0.0", nil, scopes, at)
	if ungoverned.Source != "declared_fallback" || ungoverned.Value != "^1.0.0" || !strings.Contains(ungoverned.Reason, "no lockfile governs package.json") {
		t.Fatalf("ungoverned = %+v", ungoverned)
	}

	unresolved := npmSelectedVersion("@sneat/missing", "package.json", "^1.0.0", []string{""}, scopes, at)
	if unresolved.Source != "declared_fallback" || !strings.Contains(unresolved.Reason, "lockfile in the repository root does not resolve this package") {
		t.Fatalf("unresolved = %+v", unresolved)
	}

	unindexed := npmSelectedVersion("@sneat/core", "packages/broken/package.json", "^1.0.0", []string{"packages/broken"}, scopes, at)
	if unindexed.Source != "declared_fallback" || !strings.Contains(unindexed.Reason, "lockfile in packages/broken could not be indexed (no importers section)") {
		t.Fatalf("unindexed = %+v", unindexed)
	}

	conflict := npmSelectedVersion("@sneat/conflict", "packages/app/package.json", "^1.0.0", []string{"packages/app"}, scopes, at)
	if conflict.Value != "" || conflict.Source != "packages/app/pnpm-lock.yaml" || !strings.Contains(conflict.Reason, "conflicting versions") {
		t.Fatalf("conflict = %+v", conflict)
	}

	resolved := npmSelectedVersion("@sneat/ok", "packages/app/package.json", "^1.0.0", []string{"packages/app"}, scopes, at)
	if resolved.Value != "1.2.3" || resolved.Source != "packages/app/pnpm-lock.yaml" || resolved.Reason != "" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

// TestDepsCovDriftLockScopeLabel asserts the human-readable lockfile scope
// label for the repository root and for a nested workspace.
func TestDepsCovDriftLockScopeLabel(t *testing.T) {
	t.Parallel()
	if got := lockScopeLabel(""); got != "the repository root" {
		t.Fatalf("lockScopeLabel(\"\") = %q", got)
	}
	if got := lockScopeLabel("packages/app"); got != "packages/app" {
		t.Fatalf("lockScopeLabel(nested) = %q", got)
	}
}

// TestDepsCovDriftSortDriftDependencies asserts the three-key ordering:
// dependency name, then manifest, then field.
func TestDepsCovDriftSortDriftDependencies(t *testing.T) {
	t.Parallel()
	dependencies := []DriftDependency{
		{Dependency: "b", Manifest: "a.json", Field: "dependencies"},
		{Dependency: "a", Manifest: "z.json", Field: "dependencies"},
		{Dependency: "a", Manifest: "a.json", Field: "peerDependencies"},
		{Dependency: "a", Manifest: "a.json", Field: "dependencies"},
	}
	sortDriftDependencies(dependencies)
	got := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		got = append(got, dependency.Dependency+"|"+dependency.Manifest+"|"+dependency.Field)
	}
	want := []string{
		"a|a.json|dependencies",
		"a|a.json|peerDependencies",
		"a|z.json|dependencies",
		"b|a.json|dependencies",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestDepsCovDriftNpmLatestVersionsCache asserts that a package is looked up
// at most once and that a concurrent writer's first observation wins, so the
// report stays deterministic under parallel workers.
func TestDepsCovDriftNpmLatestVersionsCache(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()

	t.Run("memoized across calls", func(t *testing.T) {
		t.Parallel()
		cache := newNpmLatestVersions()
		calls := 0
		options := DriftOptions{LatestNpmVersion: func(context.Context, string) (string, error) {
			calls++
			return "1.2.3", nil
		}}
		first := cache.observe(context.Background(), "@sneat/core", options, at)
		second := cache.observe(context.Background(), "@sneat/core", options, at)
		if calls != 1 {
			t.Fatalf("registry lookups = %d, want 1", calls)
		}
		if first.Value != "1.2.3" || second.Value != "1.2.3" {
			t.Fatalf("first = %+v, second = %+v", first, second)
		}
	})

	t.Run("first recorded observation wins", func(t *testing.T) {
		t.Parallel()
		cache := newNpmLatestVersions()
		options := DriftOptions{LatestNpmVersion: func(context.Context, string) (string, error) {
			// Simulate a concurrent worker that resolved the package while
			// this lookup was in flight.
			cache.mutex.Lock()
			cache.byKey["@sneat/core"] = VersionEvidence{Value: "9.9.9", Source: "concurrent worker", ObservedAt: at}
			cache.mutex.Unlock()
			return "1.1.1", nil
		}}
		evidence := cache.observe(context.Background(), "@sneat/core", options, at)
		if evidence.Value != "9.9.9" || evidence.Source != "concurrent worker" {
			t.Fatalf("evidence = %+v, want the first recorded observation to win", evidence)
		}
	})
}

// TestDepsCovDriftObserveLatestNpmVersionErrorsAndSuccess asserts that an
// invalid package name and a failed registry lookup both produce an explicit
// reason instead of a fabricated version, and that a valid lookup records the
// published version.
func TestDepsCovDriftObserveLatestNpmVersionErrorsAndSuccess(t *testing.T) {
	t.Parallel()
	at := driftObservedAt()
	t.Run("invalid package name", func(t *testing.T) {
		t.Parallel()
		evidence := observeLatestNpmVersion(context.Background(), "NotScoped", DriftOptions{}, at)
		if evidence.Value != "" || !strings.Contains(evidence.Reason, "must be lowercase") {
			t.Fatalf("evidence = %+v", evidence)
		}
	})
	t.Run("registry lookup failure", func(t *testing.T) {
		t.Parallel()
		evidence := observeLatestNpmVersion(context.Background(), "@sneat/core", DriftOptions{
			LatestNpmVersion: func(context.Context, string) (string, error) {
				return "", context.DeadlineExceeded
			},
		}, at)
		if evidence.Value != "" || evidence.Reason == "" {
			t.Fatalf("evidence = %+v, want a reason", evidence)
		}
	})
	t.Run("published version", func(t *testing.T) {
		t.Parallel()
		evidence := observeLatestNpmVersion(context.Background(), "@sneat/core", DriftOptions{
			LatestNpmVersion: func(context.Context, string) (string, error) { return "1.2.3", nil },
		}, at)
		if evidence.Value != "1.2.3" || !strings.Contains(evidence.Source, "pnpm view @sneat/core version") {
			t.Fatalf("evidence = %+v", evidence)
		}
	})
}

// TestDepsCovDriftMarkdownRendersEverySection asserts the full markdown
// surface: excluded and skipped repositories, a group behind its latest, a
// repository with a reason, a rename replacement, a per-dependency latest, and
// a repository that contributes no dependencies at all.
func TestDepsCovDriftMarkdownRendersEverySection(t *testing.T) {
	t.Parallel()
	report := DriftReport{
		Ecosystem:  EcosystemNPM,
		Mode:       "online",
		BaseRef:    "main",
		ObservedAt: driftObservedAt(),
		Summary:    DriftSummary{Repositories: 2, Dependencies: 2, Converged: 1, Behind: 1},
		Excluded:   []string{"acme/gone"},
		DiscoverySkips: []GraphDiscoverySkip{
			{Repository: "acme/noclone", Reason: "repository has no local clone path"},
		},
		Groups: []DriftVersionGroup{
			{
				Dependency: "@sneat/core", Classification: DriftBehindLatest,
				Versions: []DriftVersionUse{{Version: "1.0.0", Kind: "selected", Repositories: []string{"acme/a"}}},
				Latest:   &VersionEvidence{Value: "1.1.0"}, Behind: true,
				BehindRepositories: []string{"acme/a"}, Reason: "at least one repository lags latest",
			},
			{
				Dependency: "@sneat/ui", Classification: DriftConverged,
				Latest: &VersionEvidence{Reason: "latest was not queried"},
			},
		},
		Repositories: []DriftRepository{
			{
				Repository: "acme/a", Status: "ok", Reason: "inspected from a shallow clone", Path: "/checkout/a",
				Dependencies: []DriftDependency{
					{
						Dependency: "@sneat/core", Field: "dependencies",
						Declared: VersionEvidence{Value: "^1.0.0"},
						Selected: VersionEvidence{Value: "1.0.0"},
						Replacement: &ReplaceEvidence{
							OldPath: "@sneat/core", NewPath: "@sneat/core-fork",
						},
						Latest: &VersionEvidence{Value: "1.1.0"},
					},
					{
						Dependency: "@sneat/gap",
						Declared:   VersionEvidence{},
						Selected:   VersionEvidence{Reason: "no lockfile governs package.json"},
					},
				},
			},
			{Repository: "acme/empty", Status: "ok"},
		},
	}
	markdown := report.Markdown()
	for _, wanted := range []string{
		"# WB dependency drift (npm)",
		"## Excluded repositories",
		"- `acme/gone` — never inspected because it matched an --exclude pattern",
		"## Discovery skips",
		"- `acme/noclone`: repository has no local clone path",
		"| `@sneat/core` | `behind_latest` | `1.0.0` (selected: acme/a) | `1.1.0` | acme/a |",
		"## acme/a",
		"- Status: `ok` — inspected from a shallow clone",
		"| `@sneat/core` | `dependencies` | `^1.0.0` | `1.0.0` | `@sneat/core` → `@sneat/core-fork` | `1.1.0` |",
		"| `@sneat/gap` | — | `—` | `no lockfile governs package.json` | — | — |",
		"## acme/empty",
	} {
		if !strings.Contains(markdown, wanted) {
			t.Fatalf("markdown missing %q:\n%s", wanted, markdown)
		}
	}
}

// TestDepsCovDriftEvidenceOrDash asserts the evidence rendering ladder:
// a value, then a reason, then a dash.
func TestDepsCovDriftEvidenceOrDash(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		evidence VersionEvidence
		want     string
	}{
		{name: "value", evidence: VersionEvidence{Value: "1.2.3", Reason: "ignored"}, want: "1.2.3"},
		{name: "reason", evidence: VersionEvidence{Reason: "not queried"}, want: "not queried"},
		{name: "dash", evidence: VersionEvidence{}, want: "—"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := evidenceOrDash(test.evidence); got != test.want {
				t.Fatalf("evidenceOrDash(%+v) = %q, want %q", test.evidence, got, test.want)
			}
		})
	}
}

// TestDepsCovDriftWriteDriftReportsArtifactsAndErrors asserts the three
// artifacts written by WriteDriftReports and every write failure the function
// can surface: an unusable report directory, and a markdown, yaml, or json
// destination already occupied by a directory.
func TestDepsCovDriftWriteDriftReportsArtifactsAndErrors(t *testing.T) {
	t.Parallel()
	report := DriftReport{
		Ecosystem: EcosystemGo, Mode: "offline", BaseRef: "main", ObservedAt: driftObservedAt(),
		Summary: DriftSummary{Repositories: 1, Dependencies: 1, Converged: 1},
		Groups:  []DriftVersionGroup{{Dependency: "example.com/sdk", Classification: DriftConverged}},
	}

	t.Run("artifacts", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "nested", "report")
		if err := WriteDriftReports(directory, report); err != nil {
			t.Fatal(err)
		}
		markdown, err := os.ReadFile(filepath.Join(directory, "deps-drift.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(markdown) != report.Markdown() {
			t.Fatalf("deps-drift.md differs from Markdown():\n%s", markdown)
		}
		for _, name := range []string{"deps-drift.yaml", "deps-drift.json"} {
			contents, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatalf("missing %s: %v", name, err)
			}
			if len(contents) == 0 {
				t.Fatalf("%s is empty", name)
			}
		}
	})

	t.Run("unusable report directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		writeTestFile(t, blocker, "not a directory")
		if err := WriteDriftReports(filepath.Join(blocker, "report"), report); err == nil {
			t.Fatal("a report directory under a regular file must fail")
		}
	})

	for _, occupied := range []string{"deps-drift.md", "deps-drift.yaml", "deps-drift.json"} {
		t.Run("write failure for "+occupied, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			if err := os.MkdirAll(filepath.Join(directory, occupied), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := WriteDriftReports(directory, report); err == nil {
				t.Fatalf("writing over the %s directory must fail", occupied)
			}
		})
	}
}
