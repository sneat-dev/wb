//go:build e2e

package agents

import (
	"os/exec"
	"testing"
)

// TestContainerRuntimeAttachCommandDelegatesWaitAndKill starts real child
// processes to prove the attach adapter forwards Wait and Kill to the process
// it wraps, so it belongs to the e2e tier.
func TestE2EContainerRuntimeAttachCommandDelegatesWaitAndKill(t *testing.T) {
	t.Parallel()
	t.Run("wait reaps a completed child", func(t *testing.T) {
		t.Parallel()
		command := exec.Command("/bin/sh", "-c", "exit 0")
		if err := command.Start(); err != nil {
			t.Fatalf("start the harmless attach test child: %v", err)
		}
		attach := &containerAttachCommand{command: command}
		if err := attach.Wait(); err != nil {
			t.Fatalf("Wait on a completed child: %v", err)
		}
	})
	t.Run("kill stops the child and wait reaps it", func(t *testing.T) {
		t.Parallel()
		command := exec.Command("/bin/sh", "-c", "exec sleep 30")
		if err := command.Start(); err != nil {
			t.Fatalf("start the harmless attach test child: %v", err)
		}
		attach := &containerAttachCommand{command: command}
		if err := attach.Kill(); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if err := attach.Wait(); err == nil {
			t.Fatal("Wait must report the killed attach child rather than dropping the exit status")
		}
	})
	if err := (&containerAttachCommand{}).Kill(); err == nil {
		t.Fatal("Kill must refuse an adapter with no started process")
	}
}
