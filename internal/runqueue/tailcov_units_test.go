package runqueue

import (
	"os"
	"runtime"
	"testing"
)

// tailCovRequireUnixFilesystem skips the handful of tests below whose premise
// is real Unix filesystem semantics: permission bits that actually deny
// access, TMPDIR resolution, and unprivileged symlink creation. Windows and a
// root user both defeat those premises, so those paths cannot be exercised
// there; every other test in this package runs unguarded on every platform.
func tailCovRequireUnixFilesystem(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("path is not reachable on Windows: chmod/TMPDIR/symlink semantics differ")
	}
	if os.Geteuid() == 0 {
		t.Skip("path is not reachable as root: filesystem permission bits are not enforced")
	}
}

// TestTailCovUnitsClassifiesEveryToolShape drives the classifier through every
// CLI family and verb bracket it claims to understand, including the budget
// clamp and the "known tool, no heavy verb" fall-throughs.
func TestTailCovUnitsClassifiesEveryToolShape(t *testing.T) {
	// Forced small-machine (N<8): every case below predates and is
	// unaffected by sneat-dev/wb#621's adaptive heavy-job sharing, which
	// only applies once numCPU >= 8.
	defer SetNumCPUForTest(4)()
	cases := []struct {
		name   string
		argv   []string
		budget int
		want   int
	}{
		{"nil argv", nil, 3, 0},
		{"empty argv", []string{}, 3, 0},
		{"non-positive budget", []string{"go", "test", "./..."}, 0, 0},
		{"go without a heavy verb", []string{"go", "run", "./cmd/wb"}, 3, 0},
		{"go vet", []string{"go", "vet", "./internal/runqueue"}, 3, 1},
		{"go build broad scope clamps to a one-unit budget", []string{"go", "build", "./..."}, 1, 1},
		{"go cover run takes the whole budget", []string{"go", "test", "-coverprofile=/tmp/x.cov", "./..."}, 4, 4},
		{"tool name is lower-cased", []string{"GO", "test", "./internal/runqueue"}, 3, 1},
		{"golangci-lint with no scope stays focused (own default is whole repo but bare run does not say so)", []string{"golangci-lint", "run"}, 3, 1},
		{"golangci-lint run ./... is a whole-repo lint", []string{"golangci-lint", "run", "./..."}, 3, 2},
		{"golangci-lint scoped to one package", []string{"golangci-lint", "run", "./internal/runqueue"}, 3, 1},
		{"go test with two explicit packages is broad", []string{"go", "test", "./internal/runqueue", "./internal/gitops"}, 3, 2},
		{"go build with one explicit package stays focused", []string{"go", "build", "./internal/runqueue"}, 3, 1},
		{"staticcheck", []string{"staticcheck", "./..."}, 3, 1},
		{"pytest", []string{"pytest", "-q"}, 3, 1},
		{"vitest with no file is a whole-suite run", []string{"vitest", "run"}, 3, 2},
		{"vitest run scoped to a file", []string{"vitest", "run", "src/foo.test.ts"}, 3, 1},
		{"jest with no file is a whole-suite run", []string{"jest", "--ci"}, 3, 2},
		{"jest scoped to a file", []string{"jest", "--ci", "src/foo.test.js"}, 3, 1},
		{"mocha with no file is a whole-suite run", []string{"mocha"}, 3, 2},
		{"mocha scoped to a file", []string{"mocha", "test/foo.js"}, 3, 1},
		{"nx build", []string{"nx", "build", "app"}, 3, 2},
		{"nx run-many clamps", []string{"nx", "run-many", "-t", "build"}, 1, 1},
		{"nx affected", []string{"nx", "affected", "--base=main"}, 3, 2},
		{"nx test", []string{"nx", "test", "app"}, 3, 1},
		{"nx lint with no project is workspace-wide", []string{"nx", "lint"}, 3, 2},
		{"nx lint scoped to a project", []string{"nx", "lint", "app"}, 3, 1},
		{"nx e2e with no project is workspace-wide", []string{"nx", "e2e"}, 3, 2},
		{"nx e2e scoped to a project", []string{"nx", "e2e", "app"}, 3, 1},
		{"nx unknown verb is free", []string{"nx", "graph"}, 3, 0},
		{"npm build", []string{"npm", "run", "build"}, 3, 2},
		{"pnpm e2e clamps", []string{"pnpm", "run", "e2e"}, 1, 1},
		{"yarn affected", []string{"yarn", "affected"}, 3, 2},
		{"bun test with no filter runs the whole workspace", []string{"bun", "run", "test"}, 3, 2},
		{"bun test scoped by --filter", []string{"bun", "run", "test", "--filter", "app"}, 3, 1},
		{"npx lint with no path lints the whole workspace", []string{"npx", "lint"}, 3, 2},
		{"npx lint scoped to a path", []string{"npx", "lint", "src/foo.ts"}, 3, 1},
		{"npm unrelated script is free", []string{"npm", "install"}, 3, 0},
		{"cargo test", []string{"cargo", "test"}, 3, 2},
		{"cargo build clamps", []string{"cargo", "build"}, 1, 1},
		{"cargo check", []string{"cargo", "check"}, 3, 2},
		{"cargo clippy", []string{"cargo", "clippy"}, 3, 2},
		{"cargo without a heavy verb", []string{"cargo", "fmt"}, 3, 0},
		{"unclassified tool", []string{"git", "status"}, 3, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Units(testCase.argv, testCase.budget); got != testCase.want {
				t.Fatalf("Units(%v, %d) = %d, want %d", testCase.argv, testCase.budget, got, testCase.want)
			}
		})
	}
}

// TestTailCovProcessAliveRejectsNilAndDeadPIDs pins the liveness probe itself:
// non-positive PIDs are never alive, a dead PID is not alive, and this test's
// own process is.
func TestTailCovProcessAliveRejectsNilAndDeadPIDs(t *testing.T) {
	if processAlive(0) {
		t.Fatal("processAlive(0) = true, want false")
	}
	if processAlive(-1) {
		t.Fatal("processAlive(-1) = true, want false")
	}
	if processAlive(deadPID) {
		t.Fatalf("processAlive(%d) = true, want false for a PID that must not exist", deadPID)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive(os.Getpid()) = false, want true")
	}
}
