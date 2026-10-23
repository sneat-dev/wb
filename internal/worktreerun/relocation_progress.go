package worktreerun

import (
	"fmt"
	"io"
	"time"
)

// StartRelocationProgress emits the established delayed relocation pulse.
// Its stop function joins the reporter before returning.
func StartRelocationProgress(out io.Writer, task string) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		runRelocationProgress(out, task, ticker.C, done)
	}()
	return func() { close(done); <-stopped }
}
func runRelocationProgress(out io.Writer, task string, ticks <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ticks:
			_, _ = fmt.Fprintf(out, "relocate %s still running; inspecting or moving managed worktrees\n", task)
		}
	}
}
