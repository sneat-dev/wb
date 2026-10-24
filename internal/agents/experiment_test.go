package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExperimentDocumentIsImmutableAndReportsMemberRuns(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	if !strings.HasPrefix(experimentID, experimentPrefix) || len(experimentID) != len(experimentPrefix)+32 {
		t.Fatalf("experiment ID = %q", experimentID)
	}
	digest, size := TaskIdentity("compare me")
	experiment := Experiment{
		ExperimentID: experimentID, TaskSHA256: digest, TaskBytes: size,
		TaskType: "coding", Base: "main", Profiles: []string{"a", "b"}, Repeat: 2, Concurrency: 3,
		CreatedAt: time.Unix(100, 0).UTC(),
	}
	firstID, _ := NewID()
	secondID, _ := NewID()
	experiment.Attempts = []Attempt{
		{Profile: "a", RepeatIndex: 0, AgentID: firstID, Operation: "exp-a-r1", Status: AttemptPlanned},
		{Profile: "b", RepeatIndex: 0, AgentID: secondID, Operation: "exp-b-r1", Status: AttemptPlanned},
	}
	if err := store.CreateExperiment(experiment); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.CreateExperiment(experiment); err == nil {
		t.Fatal("a second write must be refused: the document is immutable")
	}

	// One member run exists and completed; the other never started.
	if err := store.Create(Record{
		AgentID: firstID, State: StateCompleted, ExecutionStatus: StatusCompleted,
		RequestedProfile: "a", Resolved: Resolved{Profile: "a"}, ExperimentID: experimentID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(110, 0).UTC(), DurationMS: 10000,
		Changes:    &ChangeSummary{FilesChanged: 3},
		Validation: ValidationRecord{Status: ValidationPassed},
		Telemetry:  Telemetry{Available: true, Source: UsageFromEvents, Tokens: &Tokens{Input: intPointer(10), Output: intPointer(5)}},
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadExperiment(experimentID)
	if err != nil {
		t.Fatalf("LoadExperiment: %v", err)
	}
	report := store.BuildReport(loaded)
	if len(report.Runs) != 1 {
		t.Fatalf("the report must join only records that exist: %#v", report.Runs)
	}
	if len(report.Summaries) != 2 {
		t.Fatalf("both profiles must appear in the summary: %#v", report.Summaries)
	}
	if report.Summaries[0].Profile != "a" || report.Summaries[0].Completed != 1 || report.Summaries[0].Validated != 1 {
		t.Fatalf("profile a summary = %#v", report.Summaries[0])
	}
	if report.Summaries[0].MeanDurationMS == nil || *report.Summaries[0].MeanDurationMS != 10000 {
		t.Fatalf("mean duration = %#v", report.Summaries[0].MeanDurationMS)
	}
	if report.Summaries[0].TotalTokens == nil || *report.Summaries[0].TotalTokens != 15 {
		t.Fatalf("total tokens = %#v", report.Summaries[0].TotalTokens)
	}
	if report.Summaries[1].Attempts != 0 {
		t.Fatalf("profile b summary = %#v", report.Summaries[1])
	}
}

func TestUnknownExperimentAndMalformedIDsAreRefused(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if _, err := store.LoadExperiment("exp-00000000000000000000000000000000"); !IsUnknownExperiment(err) {
		t.Fatalf("missing experiment = %v", err)
	}
	for _, bad := range []string{"", "exp-", "exp-zz", "not-an-experiment"} {
		if _, err := store.LoadExperiment(bad); err == nil {
			t.Fatalf("LoadExperiment accepted %q", bad)
		}
		if err := store.CreateExperiment(Experiment{ExperimentID: bad}); err == nil {
			t.Fatalf("CreateExperiment accepted %q", bad)
		}
	}
}

func TestExperimentStoreReportsCorruptAndFutureDocuments(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	path := store.ExperimentPath(experimentID)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadExperiment(experimentID); err == nil || !strings.Contains(err.Error(), "parse experiment") {
		t.Fatalf("corrupt experiment = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"experiment_id":"`+experimentID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadExperiment(experimentID); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("future experiment = %v", err)
	}
}

func TestExperimentSheetsArePrivate(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	experimentID := NewExperimentID()
	if err := store.CreateExperiment(Experiment{ExperimentID: experimentID, CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.ExperimentPath(experimentID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("experiment mode = %v", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(store.ExperimentPath(experimentID)))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("experiment directory mode = %v", dirInfo.Mode().Perm())
	}
}
