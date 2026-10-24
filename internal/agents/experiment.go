package agents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// Experiment recording constants.
const (
	experimentDirName  = "experiments"
	experimentPrefix   = "exp-"
	experimentFileName = "experiment.json"
)

// Attempt states inside one experiment. An attempt that fails before it starts
// is still recorded: an experiment that silently drops a planned comparison
// would misreport itself.
const (
	AttemptPlanned      = "planned"
	AttemptDispatched   = "dispatched"
	AttemptBaseMismatch = "base_mismatch"
	AttemptFailed       = "failed"
)

// Attempt is one planned execution inside an experiment.
type Attempt struct {
	Profile     string `json:"profile"`
	RepeatIndex int    `json:"repeat_index"`
	AgentID     string `json:"agent_id"`
	Operation   string `json:"operation"`
	BaseSHA     string `json:"base_sha,omitempty"`
	Status      string `json:"status"`
	Failure     string `json:"failure,omitempty"`
}

// Experiment is the immutable description of one comparison: one task, one
// base, several profiles and repeats. It is written once, before any execution
// starts, and never rewritten; member results live in their own run records.
type Experiment struct {
	SchemaVersion int       `json:"schema_version"`
	ExperimentID  string    `json:"experiment_id"`
	TaskSHA256    string    `json:"task_sha256"`
	TaskBytes     int       `json:"task_bytes"`
	TaskType      string    `json:"task_type"`
	Complexity    string    `json:"complexity,omitempty"`
	Base          string    `json:"base,omitempty"`
	BaseSHA       string    `json:"base_sha,omitempty"`
	Profiles      []string  `json:"profiles"`
	Repeat        int       `json:"repeat"`
	Concurrency   int       `json:"concurrency"`
	Attempts      []Attempt `json:"attempts"`
	CreatedAt     time.Time `json:"created_at"`
	// UncontrolledDifferences states what WB could not hold equal across
	// attempts, so a reader never mistakes a comparison for a controlled trial.
	UncontrolledDifferences []string `json:"uncontrolled_differences,omitempty"`
}

// NewExperimentID mints a fresh experiment ID.
func NewExperimentID() string {
	var random [16]byte
	_, _ = rand.Read(random[:])
	return experimentPrefix + hex.EncodeToString(random[:])
}

// ExperimentDir is the private directory holding one experiment document.
func (store Store) ExperimentDir(experimentID string) string {
	return filepath.Join(store.Root, experimentDirName, experimentID)
}

// ExperimentPath is the experiment document path.
func (store Store) ExperimentPath(experimentID string) string {
	return filepath.Join(store.ExperimentDir(experimentID), experimentFileName)
}

// CreateExperiment writes the document once. A second write is refused so a
// planned experiment cannot be rewritten after results exist.
func (store Store) CreateExperiment(experiment Experiment) error {
	if err := validateExperimentID(experiment.ExperimentID); err != nil {
		return err
	}
	experiment.SchemaVersion = schemaVersion
	dir := store.ExperimentDir(experiment.ExperimentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create experiment directory %s: %w", dir, err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open experiment directory %s: %w", dir, err)
	}
	defer func() { _ = directory.Close() }()
	// An immutable publication is what makes the document a plan rather than a
	// mutable status file: a second write must never overwrite the first.
	if err := filewrite.WriteJSONImmutableAt(directory, experimentFileName, experiment, false, nil); err != nil {
		return fmt.Errorf("create experiment %s: %w", experiment.ExperimentID, err)
	}
	return nil
}

// LoadExperiment reads one experiment document.
func (store Store) LoadExperiment(experimentID string) (Experiment, error) {
	if err := validateExperimentID(experimentID); err != nil {
		return Experiment{}, err
	}
	raw, err := os.ReadFile(store.ExperimentPath(experimentID))
	if err != nil {
		if os.IsNotExist(err) {
			return Experiment{}, &UnknownExperimentError{ExperimentID: experimentID, Root: store.Root}
		}
		return Experiment{}, fmt.Errorf("read experiment %s: %w", experimentID, err)
	}
	var experiment Experiment
	if err := json.Unmarshal(raw, &experiment); err != nil {
		return Experiment{}, fmt.Errorf("parse experiment %s: %w", experimentID, err)
	}
	if experiment.SchemaVersion != schemaVersion {
		return Experiment{}, fmt.Errorf("experiment %s has schema_version %d; this wb supports %d", experimentID, experiment.SchemaVersion, schemaVersion)
	}
	return experiment, nil
}

// UnknownExperimentError is returned when a named experiment does not exist.
type UnknownExperimentError struct {
	ExperimentID string
	Root         string
}

func (err *UnknownExperimentError) Error() string {
	return fmt.Sprintf("no experiment %s under %s", err.ExperimentID, err.Root)
}

// IsUnknownExperiment reports whether an error is a lookup miss.
func IsUnknownExperiment(err error) bool {
	var notFound *UnknownExperimentError
	return errors.As(err, &notFound)
}

func validateExperimentID(experimentID string) error {
	if len(experimentID) != len(experimentPrefix)+32 || experimentID[:len(experimentPrefix)] != experimentPrefix {
		return fmt.Errorf("invalid experiment ID %q; expected %s followed by 32 hex characters", experimentID, experimentPrefix)
	}
	if _, err := hex.DecodeString(experimentID[len(experimentPrefix):]); err != nil {
		return fmt.Errorf("invalid experiment ID %q", experimentID)
	}
	return nil
}

// ProfileSummary aggregates the attempts of one profile.
type ProfileSummary struct {
	Profile   string `json:"profile"`
	Attempts  int    `json:"attempts"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Validated int    `json:"validated"`
	Accepted  int    `json:"accepted"`
	// MeanDurationMS is the mean wall time of terminal attempts, or nil when
	// none finished.
	MeanDurationMS *int64 `json:"mean_duration_ms,omitempty"`
	// TotalTokens is nil unless every completed attempt reported usage.
	TotalTokens *int `json:"total_tokens,omitempty"`
}

// ExperimentReport is the persisted experiment plus its member results and the
// per-profile rollup.
type ExperimentReport struct {
	Experiment
	Runs      []Result         `json:"runs"`
	Summaries []ProfileSummary `json:"summaries"`
}

// BuildReport joins an experiment with its member records. A run that cannot be
// loaded is skipped rather than failing the whole report: a partially completed
// experiment is exactly what an operator needs to see after a crash.
func (store Store) BuildReport(experiment Experiment) ExperimentReport {
	report := ExperimentReport{Experiment: experiment}
	byAttempt := map[string]Result{}
	for _, attempt := range experiment.Attempts {
		record, err := store.Load(attempt.AgentID)
		if err != nil {
			continue
		}
		result := store.Render(record)
		report.Runs = append(report.Runs, result)
		byAttempt[attempt.AgentID] = result
	}
	// The document is immutable, so the outcome an attempt actually reached is
	// derived from its own record rather than written back into the experiment.
	for index := range report.Attempts {
		attempt := &report.Attempts[index]
		result, ok := byAttempt[attempt.AgentID]
		if !ok {
			continue
		}
		attempt.Status = attemptStatus(result)
		attempt.BaseSHA = result.BaseSHA
		attempt.Failure = result.Failure
		if report.BaseSHA == "" && result.BaseSHA != "" {
			report.BaseSHA = result.BaseSHA
		}
	}
	report.Summaries = summarizeProfiles(experiment, report.Runs)
	return report
}

// attemptStatus maps one execution result onto the attempt vocabulary.
func attemptStatus(result Result) string {
	if !result.Terminal {
		return AttemptDispatched
	}
	if result.FailureCategory == FailureBaseMismatch {
		return AttemptBaseMismatch
	}
	return result.Status.String()
}

// String is the string form of an execution status.
func (status ExecutionStatus) String() string { return string(status) }

func summarizeProfiles(experiment Experiment, runs []Result) []ProfileSummary {
	byProfile := map[string]*ProfileSummary{}
	order := make([]string, 0, len(experiment.Profiles))
	for _, profile := range experiment.Profiles {
		byProfile[profile] = &ProfileSummary{Profile: profile}
		order = append(order, profile)
	}
	durations := map[string][]int64{}
	tokens := map[string]int{}
	tokensKnown := map[string]bool{}
	for _, run := range runs {
		profile := run.RequestedProfile
		summary, ok := byProfile[profile]
		if !ok {
			summary = &ProfileSummary{Profile: profile}
			byProfile[profile] = summary
			order = append(order, profile)
		}
		summary.Attempts++
		switch run.Status {
		case StatusCompleted:
			summary.Completed++
		case StatusFailed, StatusCancelled, StatusTimedOut, StatusAbandoned:
			summary.Failed++
		}
		if run.Validation.Status == ValidationPassed {
			summary.Validated++
		}
		if run.Review.Status == ReviewAccepted {
			summary.Accepted++
		}
		if run.Terminal {
			durations[profile] = append(durations[profile], run.DurationMS)
		}
		if run.Status == StatusCompleted {
			if run.Telemetry.Available && run.Telemetry.Tokens != nil && run.Telemetry.Tokens.Input != nil && run.Telemetry.Tokens.Output != nil {
				tokens[profile] += *run.Telemetry.Tokens.Input + *run.Telemetry.Tokens.Output
				tokensKnown[profile] = true
			} else {
				tokensKnown[profile] = false
			}
		}
	}
	summaries := make([]ProfileSummary, 0, len(order))
	for _, profile := range order {
		summary := byProfile[profile]
		if values := durations[profile]; len(values) > 0 {
			var total int64
			for _, value := range values {
				total += value
			}
			mean := total / int64(len(values))
			summary.MeanDurationMS = &mean
		}
		if tokensKnown[profile] {
			total := tokens[profile]
			summary.TotalTokens = &total
		}
		summaries = append(summaries, *summary)
	}
	sort.SliceStable(summaries, func(i, j int) bool { return summaries[i].Profile < summaries[j].Profile })
	return summaries
}
