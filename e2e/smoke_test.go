//go:build e2e

// Package e2e holds task-23's real-git end-to-end journeys and the contract
// tests that prove task-8's fakes behave like the real programs they
// replace (spec/plans/coverage-to-100/README.md). This file is task-24's own
// placeholder: it proves the tier's own wiring -- the //go:build e2e tag,
// the TestE2E*/TestContract* naming convention, and the
// `go test -tags e2e -run '^Test(E2E|Contract)' ./...` CI job -- actually
// compiles and runs a test, before task-23 adds the real journeys and
// contract tests. It is not itself a journey or a contract test.
package e2e

import "testing"

// TestE2ESmokeTierWiring asserts nothing about wb; it only has to run. The
// coverage job's default `go test ./...` never even builds this file --
// //go:build e2e excludes it entirely -- so it can never count toward the
// coverage profile, matching task-24's "e2e tier ... has no coverage step."
func TestE2ESmokeTierWiring(t *testing.T) {
	t.Parallel()
}
