package worktreerun

import (
	"errors"
	"io"
	"testing"
	"time"
)

type movementPulseWriter struct {
	messages chan string
	fail     bool
}

func (w movementPulseWriter) Write(p []byte) (int, error) {
	w.messages <- string(p)
	if w.fail {
		return 0, errors.New("closed output")
	}
	return len(p), nil
}
func TestMovementRelocationProgressTicksAndJoins(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		ticks := make(chan time.Time)
		done := make(chan struct{})
		stopped := make(chan struct{})
		messages := make(chan string)
		go func() {
			defer close(stopped)
			runRelocationProgress(movementPulseWriter{messages, fail}, "task", ticks, done)
		}()
		ticks <- time.Time{}
		if got := <-messages; got != "relocate task still running; inspecting or moving managed worktrees\n" {
			t.Fatalf("pulse=%q", got)
		}
		close(done)
		<-stopped
	}
}
func TestMovementNativeRelocationProgressFactoryStopsPromptly(t *testing.T) {
	t.Parallel()
	stop := StartRelocationProgress(io.Discard, "task")
	stop()
}
