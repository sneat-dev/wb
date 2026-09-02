package deps

// defaultReadOnlyParallel is the worker floor applied to pools that only
// read: graph discovery (git fetch plus manifest reads of origin/<ref>) and
// registry release observations. It deliberately matches `wb sync`'s
// --parallel default — the concurrency WB already applies to fleet-wide
// read-only git network operations out of the box — so a default `wb deps
// bump` stops paying one serial `git fetch` per fleet repository per wave
// while the mutating lifecycle (worktree apply, local verification, merges)
// keeps the conservative --parallel bound for small machines.
const defaultReadOnlyParallel = 4

// readOnlyWorkerCount bounds one read-only worker pool. An explicit
// --parallel is authoritative in both directions: raising it widens these
// pools, and an explicit --parallel 1 keeps them serial. When the flag is
// left at its default, the pool gets a floor of defaultReadOnlyParallel,
// never exceeding the job count and never below one worker.
func readOnlyWorkerCount(parallel int, parallelExplicit bool, jobs int) int {
	workers := parallel
	if !parallelExplicit && workers < defaultReadOnlyParallel {
		workers = defaultReadOnlyParallel
	}
	if workers > jobs {
		workers = jobs
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}
