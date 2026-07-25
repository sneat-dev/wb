package console

import (
	"slices"
	"strings"
	"testing"
)

// TestCommandEnvDisablesEveryChildPrompt guards the settings that stop git, gh,
// and ssh from waiting on a human. Losing any one of them reintroduces a hang
// that no amount of stdin redirection prevents, because these tools prompt on
// /dev/tty directly.
func TestCommandEnvDisablesEveryChildPrompt(t *testing.T) {
	t.Parallel()
	environment := CommandEnv([]string{"PATH=/usr/bin"})
	required := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GH_PAGER=cat",
		"GH_PROMPT_DISABLED=1",
	}
	for _, entry := range required {
		if !slices.Contains(environment, entry) {
			t.Errorf("child environment is missing %s", entry)
		}
	}
	if !slices.Contains(environment, sshBatchCommand) {
		t.Errorf("child environment is missing %s", sshBatchCommand)
	}
	if !slices.Contains(environment, "PATH=/usr/bin") {
		t.Error("child environment dropped the caller's own variables")
	}
}

// TestCommandEnvKeepsAnExplicitSSHCommand protects a user who configured a
// specific key or proxy: overwriting it would surface as an authentication
// failure that looks nothing like the cause.
func TestCommandEnvKeepsAnExplicitSSHCommand(t *testing.T) {
	t.Parallel()
	custom := "GIT_SSH_COMMAND=ssh -i /keys/deploy"
	environment := CommandEnv([]string{custom})
	if !slices.Contains(environment, custom) {
		t.Error("an explicitly configured GIT_SSH_COMMAND was dropped")
	}
	count := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, "GIT_SSH_COMMAND=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("GIT_SSH_COMMAND appears %d times, want exactly 1", count)
	}
}

func TestEnvBuildsOnTheProcessEnvironment(t *testing.T) {
	t.Setenv("WB_COMMANDENV_PROBE", "present")
	if !slices.Contains(Env(), "WB_COMMANDENV_PROBE=present") {
		t.Error("Env() did not inherit the process environment")
	}
	if !slices.Contains(Env(), "GIT_TERMINAL_PROMPT=0") {
		t.Error("Env() did not apply the non-interactive settings")
	}
}
