//go:build e2e

package runner

// e2eBuildTag is true whenever the binary is built with `-tags e2e`
// (spec/plans/coverage-to-100/README.md task-24's e2e tier), which lifts
// the runtime guard: an e2e-tagged test proves the real thing against real
// git on purpose. See guard_default.go for the ordinary-build counterpart.
const e2eBuildTag = true
