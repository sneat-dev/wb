package agentrun

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/agents"
)

// TestExperimentRebuildsAReportFromDurableRecords proves that an experiment is
// described by its immutable document plus its member records: nothing is held
// only in the process that ran the comparison.
func TestExperimentRebuildsAReportFromDurableRecords(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	experimentID := "exp-00000000000000000000000000000001"
	firstID, _ := agents.NewID()
	secondID, _ := agents.NewID()
	experiment := agents.Experiment{
		ExperimentID: experimentID, TaskSHA256: "digest", TaskBytes: 10, TaskType: "coding",
		Profiles: []string{"cheap"}, Repeat: 2, Concurrency: 1, CreatedAt: fixture.now,
		Attempts: []agents.Attempt{
			{Profile: "cheap", RepeatIndex: 0, AgentID: firstID, Operation: "exp-r1", Status: agents.AttemptPlanned},
			{Profile: "cheap", RepeatIndex: 1, AgentID: secondID, Operation: "exp-r2", Status: agents.AttemptPlanned},
		},
	}
	if err := store.CreateExperiment(experiment); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(agents.Record{
		AgentID: firstID, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted,
		RequestedProfile: "cheap", Resolved: agents.Resolved{Profile: "cheap"}, BaseSHA: "pinned",
		StartedAt: fixture.now, FinishedAt: fixture.now, DurationMS: 1000,
		Validation: agents.ValidationRecord{Status: agents.ValidationPassed},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := fixture.service().Experiment(context.Background(), ExperimentRequest{
		ProjectsRoot: fixture.root, ExperimentID: experimentID,
	})
	if err != nil {
		t.Fatalf("Experiment: %v", err)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("runs = %#v", report.Runs)
	}
	if report.BaseSHA != "pinned" {
		t.Fatalf("base was not recovered from the member record: %q", report.BaseSHA)
	}
	if report.Attempts[0].Status != agents.StatusCompleted.String() {
		t.Fatalf("a started attempt must show its outcome: %#v", report.Attempts[0])
	}
	if report.Attempts[1].Status != agents.AttemptPlanned {
		t.Fatalf("an attempt with no record stays planned: %#v", report.Attempts[1])
	}
	if len(report.Summaries) != 1 || report.Summaries[0].Completed != 1 {
		t.Fatalf("summaries = %#v", report.Summaries)
	}
}
