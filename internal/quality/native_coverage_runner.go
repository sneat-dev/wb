package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// nativeCoverageError distinguishes collection infrastructure from a failing Go test.
type nativeCoverageError struct{ cause error }

func (failure *nativeCoverageError) Error() string {
	return "native executable coverage: " + failure.cause.Error()
}
func (failure *nativeCoverageError) Unwrap() error { return failure.cause }
func isNativeCoverageFailure(err error) bool {
	var failure *nativeCoverageError
	return errors.As(err, &failure)
}
func preserveNativeCoverageFailure(previous, replacement error) error {
	if isNativeCoverageFailure(previous) {
		return &nativeCoverageError{errors.Join(replacement, previous)}
	}
	return replacement
}

func nativeCoverageEnvironment(environment, packages []string) []string {
	return append(append([]string(nil), environment...), "WB_TEST_NATIVE_COVERPKG="+strings.Join(packages, ","))
}

func nativeCoveragePatterns(arguments []string) ([]string, error) {
	for index, argument := range arguments {
		if argument == "-coverpkg" {
			if index+1 == len(arguments) {
				return nil, &nativeCoverageError{errors.New("-coverpkg requires a value")}
			}
			return strings.Split(arguments[index+1], ","), nil
		}
		if value, ok := strings.CutPrefix(argument, "-coverpkg="); ok {
			return strings.Split(value, ","), nil
		}
	}
	return nil, nil
}

func resolveNativeCoveragePackages(ctx context.Context, module string, environment, arguments, knownPatterns, knownPackages []string) ([]string, error) {
	patterns, err := nativeCoveragePatterns(arguments)
	if err != nil || len(patterns) == 0 {
		return nil, err
	}
	if slices.Equal(patterns, knownPatterns) {
		return append([]string(nil), knownPackages...), nil
	}
	packages, err := goCoveragePackages(ctx, module, patterns, environment)
	if err != nil {
		return nil, &nativeCoverageError{fmt.Errorf("resolve selected packages: %w", err)}
	}

	return packages, nil
}

// Each execute owns exactly one actual retry attempt, including its native counters.
type nativeCoverageAttempt struct {
	run           func(context.Context, []string, string, string, ...string) (string, error)
	temporaryRoot string
}

func (attempt nativeCoverageAttempt) execute(ctx context.Context, module string, environment []string, profile string, arguments []string) (string, error) {
	directory, err := os.MkdirTemp(attempt.temporaryRoot, "wb-native-cover-*")
	if err != nil {
		return "", &nativeCoverageError{fmt.Errorf("create attempt directory: %w", err)}
	}
	defer func() { _ = os.RemoveAll(directory) }()
	overrides := append(append([]string(nil), environment...), "WB_TEST_NATIVE_COVERDIR="+directory)
	output, err := attempt.run(ctx, overrides, module, "go", arguments...)
	if err != nil {
		return output, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return output, &nativeCoverageError{fmt.Errorf("inspect attempt directory: %w", err)}
	}
	if len(entries) == 0 {
		return output, nil
	}
	if err = validateNativeCoverageArtifacts(entries); err != nil {
		return output, &nativeCoverageError{err}
	}
	nativeProfile := filepath.Join(directory, "native.cov")
	converted, err := attempt.run(ctx, environment, module, "go", "tool", "covdata", "textfmt", "-i="+directory, "-o="+nativeProfile)
	if converted != "" {
		output += "\n[native executable coverage]\n" + converted
	}
	if err != nil {
		return output, &nativeCoverageError{fmt.Errorf("convert native data: %w", err)}
	}
	if err = ctx.Err(); err != nil {
		return output, &nativeCoverageError{err}
	}
	if err = mergeCoverageProfiles([]string{profile, nativeProfile}, profile); err != nil {
		return output, &nativeCoverageError{fmt.Errorf("merge native profile: %w", err)}
	}
	return output, nil
}

var nativeMetadataName = regexp.MustCompile(`^covmeta\.([0-9a-f]{32})$`)
var nativeCounterName = regexp.MustCompile(`^covcounters\.([0-9a-f]{32})\.[0-9]+\.[0-9]+$`)

// covdata tolerates orphan pods; profile acceptance deliberately requires complete data.
func validateNativeCoverageArtifacts(entries []os.DirEntry) error {
	type pair struct{ metadata, counters bool }
	pairs := make(map[string]pair)
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected native artifact directory %q", entry.Name())
		}
		if match := nativeMetadataName.FindStringSubmatch(entry.Name()); match != nil {
			value := pairs[match[1]]
			value.metadata = true
			pairs[match[1]] = value
		} else if match := nativeCounterName.FindStringSubmatch(entry.Name()); match != nil {
			value := pairs[match[1]]
			value.counters = true
			pairs[match[1]] = value
		} else {
			return fmt.Errorf("unexpected native artifact %q", entry.Name())
		}
	}
	for digest, value := range pairs {
		if !value.metadata || !value.counters {
			return fmt.Errorf("native pod %s requires matching metadata and counters", digest)
		}
	}
	return nil
}
