package agentfields

import (
	"strings"
	"testing"
)

func TestCleanDropsAnAgentWhoseIdentifyingFieldFailsAndBlanksTheRest(t *testing.T) {
	t.Parallel()
	good := Agent{Kind: KindRun, RunID: "agt-1", Runtime: "claude", Model: "meta/llama:8b", State: "running", Activity: "working", Task: "fix-ci", Repository: "sneat-dev/wb"}
	cleaned, ok := Clean(good, IsTaskName)
	if !ok || cleaned != good || !Valid(good, IsTaskName) {
		t.Fatalf("a good agent changed: %+v %v", cleaned, ok)
	}
	for name, bad := range map[string]Agent{
		"kind":    {Kind: "daemon", State: "running"},
		"state":   {Kind: KindSession, State: "running"},
		"session": {Kind: KindSession, State: "live", SessionID: "a b"},
		"run":     {Kind: KindRun, State: "running", RunID: "/etc/passwd"},
	} {
		if _, ok := Clean(bad, IsTaskName); ok || Valid(bad, IsTaskName) {
			t.Errorf("%s: a bad identifying field kept the agent", name)
		}
	}
	dirty := Agent{Kind: KindSession, State: "live", Runtime: "a b", Model: "/Users/x/m.gguf", Activity: "napping", Task: "two words", Repository: "nope"}
	cleaned, ok = Clean(dirty, IsTaskName)
	if !ok || cleaned != (Agent{Kind: KindSession, State: "live"}) || Valid(dirty, IsTaskName) {
		t.Fatalf("fields were not blanked: %+v %v", cleaned, ok)
	}
}

func TestTheTaskRuleIsPassedIn(t *testing.T) {
	t.Parallel()
	agent := Agent{Kind: KindSession, State: "live", Task: "a task with spaces"}
	if cleaned, _ := Clean(agent, IsTaskName); cleaned.Task != "" {
		t.Error("the strict task rule kept prose")
	}
	if cleaned, _ := Clean(agent, IsText); cleaned.Task != agent.Task {
		t.Error("the text rule blanked a readable task")
	}
	if IsText(strings.Repeat("x", MaxTextBytes+1)) || IsText("a\u202eb") || IsText("a\u3000b") || IsText("a\ufffdb") || !IsText("plain text") {
		t.Error("the text rule is wrong")
	}
	if !IsTaskName("feature/sub-task.1") || IsTaskName("/abs") || IsTaskName("a=b") || IsTaskName("") {
		t.Error("the task name rule is wrong")
	}
}

func TestClosedSets(t *testing.T) {
	t.Parallel()
	if len(states[KindSession]) != 2 || len(states[KindRun]) != 5 || states["x"] != nil || len(Activities) != 5 {
		t.Fatal("the closed sets changed")
	}
}
