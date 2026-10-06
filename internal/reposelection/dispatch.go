package reposelection

import "sync"

// ForEach calls run exactly once for each index in [0,count) and waits for all
// callbacks to finish. At most parallel callbacks run concurrently; parallel=1
// runs directly in index order. Callbacks own separate indices and must protect
// any shared state. This function does not cancel or recover callback panics.
// Zero work returns regardless of parallel. Negative count or nonpositive
// parallel with nonempty work panics rather than deadlocking on zero workers.
func ForEach(count, parallel int, run func(int)) {
	if count < 0 {
		panic("reposelection.ForEach: count must not be negative")
	}
	if count == 0 {
		return
	}
	if parallel < 1 {
		panic("reposelection.ForEach: parallelism must be at least 1")
	}
	if parallel > count {
		parallel = count
	}
	if parallel == 1 {
		for index := range count {
			run(index)
		}
		return
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range parallel {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				run(index)
			}
		}()
	}
	for index := range count {
		jobs <- index
	}
	close(jobs)
	group.Wait()
}
