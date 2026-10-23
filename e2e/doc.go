// Package e2e holds spec/plans/coverage-to-100/README.md's real-git
// end-to-end journeys (task-23) and the contract tests that prove task-8's
// fakes behave like the real programs they replace. Every test in this
// package carries a `//go:build e2e` tag (see smoke_test.go): CI runs them
// with `go test -tags e2e -run '^Test(E2E|Contract)' ./...` as their own,
// separately required job (task-24), never through the default `go test
// ./...` the coverage job runs.
//
// This file carries no build tag of its own so that an ordinary `go vet
// ./...`/`go build ./...` -- run without -tags e2e -- still finds a
// buildable package here, rather than "build constraints exclude all Go
// files."
package e2e
