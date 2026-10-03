package cmdquality

import (
	"errors"
	"strings"
	"testing"
)

func TestCoverageShardingFlagsFailClosedOnAmbiguousScope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options qualityOptions
		want    string
	}{
		{name: "zero shards", options: qualityOptions{testShards: 0}, want: "at least 1"},
		{name: "shards without package", options: qualityOptions{testShards: 2}, want: "requires at least one"},
		{name: "package without shards", options: qualityOptions{testShards: 1, shardPackages: []string{"./internal/worktrees"}}, want: "greater than 1"},
		{name: "fleet package", options: qualityOptions{testShards: 2, shardPackages: []string{"./internal/worktrees"}, fleet: true}, want: "repository-specific"},
		{name: "fleet profile", options: qualityOptions{testShards: 1, fleet: true, coverageProfile: "profile.cov"}, want: "one fresh repository"},
		{name: "invalid minimum", options: qualityOptions{testShards: 1, minimumCoverage: 101}, want: "between 0 and 100"},
		{name: "CI with changed", options: qualityOptions{testShards: 1, ci: true, changed: true}, want: "--ci cannot be combined with --changed"},
		{name: "CI with resume", options: qualityOptions{testShards: 1, ci: true, resume: true}, want: "--ci cannot be combined with --resume"},
		{name: "CI with coverage profile", options: qualityOptions{testShards: 1, ci: true, coverageProfile: "profile.cov"}, want: "--ci cannot be combined with --coverage-profile"},
		{name: "CI with package filter", options: qualityOptions{testShards: 1, ci: true, explicitGoTestPackages: true, packagePatterns: []string{"./internal/worktrees"}}, want: "--ci cannot be combined with --package"},
		{name: "changed without target", options: qualityOptions{testShards: 1, changed: true}, want: "requires --target"},
		{name: "changed with fleet", options: qualityOptions{testShards: 1, changed: true, target: "main", fleet: true}, want: "repository-specific"},
		{name: "changed with resume", options: qualityOptions{testShards: 1, changed: true, target: "main", resume: true}, want: "cannot be combined with --resume"},
		{name: "changed with test-shards", options: qualityOptions{testShards: 2, shardPackages: []string{"./internal/worktrees"}, explicitGoTestSharding: true, changed: true, target: "main"}, want: "cannot be combined with --test-shards"},
		{name: "changed with package filter", options: qualityOptions{testShards: 1, changed: true, target: "main", explicitGoTestPackages: true, packagePatterns: []string{"./internal/worktrees"}}, want: "--changed cannot be combined with --package"},
		{name: "fleet with package filter", options: qualityOptions{testShards: 1, fleet: true, explicitGoTestPackages: true, packagePatterns: []string{"./internal/worktrees"}}, want: "--package is repository-specific and cannot be combined with --fleet"},
		{name: "resume with package filter", options: qualityOptions{testShards: 1, resume: true, explicitGoTestPackages: true, packagePatterns: []string{"./internal/worktrees"}}, want: "--package cannot be combined with --resume"},
		{name: "empty package filter", options: qualityOptions{testShards: 1, explicitGoTestPackages: true, packagePatterns: []string{""}}, want: "--package must not be empty"},
		{name: "changed invalid format", options: qualityOptions{testShards: 1, changed: true, target: "main", format: "summary"}, want: "changed supports --format markdown or json only"},
		{name: "target without changed", options: qualityOptions{testShards: 1, target: "main"}, want: "--target requires --changed"},
		{name: "baseline-file without changed", options: qualityOptions{testShards: 1, baselineFile: "baseline.json"}, want: "--baseline-file requires --changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCoverageExecutionOptions(testRuntime(), test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}
func TestCoverageOptionsForCommandRecordsExplicitSharding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "omitted flags use policy", want: false},
		{name: "test shards flag", args: []string{"--test-shards", "4"}, want: true},
		{name: "shard package flag", args: []string{"--shard-package", "./internal/worktrees"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := NewCoverage(testRuntime(), testDependencies(t))
			if usage := command.Flags().Lookup("shard-package").Usage; !strings.Contains(usage, "within the --package scope") || !strings.Contains(usage, "selected packages not named here run once") {
				t.Fatalf("shard-package help = %q", usage)
			}
			if usage := command.Flags().Lookup("package").Usage; !strings.Contains(usage, "restrict coverage") {
				t.Fatalf("package help = %q", usage)
			}
			if err := command.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			options := coverageOptionsForCommand(qualityOptions{
				testShards:             4,
				shardPackages:          []string{"./internal/worktrees"},
				explicitGoTestSharding: coverageShardingExplicit(command),
			})
			if options.ExplicitGoTestSharding != tc.want {
				t.Fatalf("ExplicitGoTestSharding = %t, want %t", options.ExplicitGoTestSharding, tc.want)
			}
		})
	}
}
func TestCoverageCmdRejectsPackageFilterInUnsupportedModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "CI", args: []string{"--ci", "--package", "./internal/worktrees"}, want: "--ci cannot be combined with --package"},
		{name: "changed", args: []string{"--changed", "--target", "main", "--package", "./internal/worktrees"}, want: "--changed cannot be combined with --package"},
		{name: "fleet", args: []string{"--fleet", "--package", "./internal/worktrees"}, want: "--package is repository-specific and cannot be combined with --fleet"},
		{name: "resume", args: []string{"--resume", "--package", "./internal/worktrees"}, want: "--package cannot be combined with --resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := executeTest(t, NewCoverage(testRuntime(), testDependencies(t)), tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("coverage %v = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
}
func TestCoverageCmdRejectsFlagShapedPackagePatterns(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{"-run=^$", "-coverpkg=./...", "-deps"} {
		t.Run(pattern, func(t *testing.T) {
			if _, _, err := executeTest(t, NewCoverage(testRuntime(), testDependencies(t)), "--package="+pattern); err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
				t.Fatalf("ordinary coverage package %q = %v, want flag-shaped package rejection", pattern, err)
			}
		})
	}
}
func TestCoverageCmdRejectsExplicitShardingInCIAndChangedModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "CI test shards one", args: []string{"--ci", "--test-shards", "1"}, want: "--ci cannot be combined with --test-shards or --shard-package"},
		{name: "CI shard package", args: []string{"--ci", "--shard-package", "./internal/worktrees"}, want: "--ci cannot be combined with --test-shards or --shard-package"},
		{name: "changed test shards one", args: []string{"--changed", "--target", "main", "--test-shards", "1"}, want: "--changed cannot be combined with --test-shards or --shard-package"},
		{name: "changed shard package", args: []string{"--changed", "--target", "main", "--shard-package", "./internal/worktrees"}, want: "--changed cannot be combined with --test-shards or --shard-package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := executeTest(t, NewCoverage(testRuntime(), testDependencies(t)), tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("coverage %v = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
}
func TestAffectedCoverageFlags(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options qualityOptions
		want    string
	}{
		{name: "requires ratchet", options: qualityOptions{affectedPackages: true}, want: "requires --changed"},
		{name: "reject explicit packages", options: qualityOptions{affectedPackages: true, changed: true, explicitGoTestPackages: true}, want: "cannot be combined with --package"},
		{name: "reject aggregate floor", options: qualityOptions{affectedPackages: true, changed: true, minimumCoverage: 94}, want: "repository-wide --minimum"},
		{name: "accepted", options: qualityOptions{affectedPackages: true, changed: true, target: "main", testShards: 1, minimumCoverage: -1, format: "json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCoverageExecutionOptions(testRuntime(), test.options)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
			var usage *testExitError
			if !errors.As(err, &usage) || usage.code != exitUsage {
				t.Fatalf("misused affected flag error=%v, want usage exit code %d", err, exitUsage)
			}
		})
	}
}
func TestNativeCoverageOptionsRequireFreshRunAndReachBothRunnerPaths(t *testing.T) {
	t.Parallel()
	for _, options := range []qualityOptions{{ci: true, includeE2E: true}, {resume: true, includeE2E: true}} {
		if err := validateCoverageExecutionOptions(testRuntime(), options); err == nil || !strings.Contains(err.Error(), "--include-e2e") {
			t.Fatalf("invalid mode: %v", err)
		}
	}
	options := qualityOptions{includeE2E: true, testShards: 1, minimumCoverage: -1}
	if err := validateCoverageExecutionOptions(testRuntime(), options); err != nil {
		t.Fatal(err)
	}
	if !runOptions(options).IncludeE2E || !coverageOptionsForCommand(options).IncludeE2E {
		t.Fatal("native tier lost in ordinary or changed runner options")
	}
	if NewCoverage(testRuntime(), testDependencies(t)).Flags().Lookup("include-e2e").DefValue != "false" {
		t.Fatal("native tier enabled by default")
	}
}

func TestNamedCheckProfilesRejectUnknown(t *testing.T) {
	t.Parallel()
	if _, err := checksForProfile("ci"); err != nil {
		t.Fatal(err)
	}
	if _, err := checksForProfile("unknown"); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
