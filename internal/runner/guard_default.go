//go:build !e2e

package runner

// e2eBuildTag is false in every ordinary build, including the default `go
// test ./...` unit tier. See guard_e2e.go for the e2e-tagged counterpart:
// exactly one of the two files in this mutually exclusive pair is compiled,
// selected by the `e2e` build tag, the same technique
// internal/process/command_unix.go and command_other.go already use for a
// platform switch.
const e2eBuildTag = false
