package hooks

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const EventSchemaVersion = 1

type Event struct {
	SchemaVersion int               `json:"schema_version"`
	Timestamp     time.Time         `json:"timestamp"`
	Repository    string            `json:"repository"`
	Hook          string            `json:"hook"`
	Action        string            `json:"action"`
	Outcome       string            `json:"outcome"`
	DurationMS    int64             `json:"duration_ms"`
	Commit        string            `json:"commit,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Labels        map[string]string `json:"labels,omitempty"`
}

func newEvent(repoRoot, hook string, success bool, duration time.Duration, timestamp time.Time, labels map[string]string) Event {
	outcome := "passed"
	if !success {
		outcome = "failed"
	}
	action := hook
	switch hook {
	case "post-commit":
		action = "commit"
	case "pre-push":
		action = "push-attempt"
	case "pre-commit":
		action = "commit-check"
	}
	commit, _ := gitOutput(repoRoot, "rev-parse", "HEAD")
	branch, _ := gitOutput(repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	return Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     timestamp.UTC(),
		Repository:    originSlug(repoRoot),
		Hook:          hook,
		Action:        action,
		Outcome:       outcome,
		DurationMS:    duration.Milliseconds(),
		Commit:        commit,
		Branch:        branch,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Labels:        cloneLabels(labels),
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func AppendEvent(path string, event Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create hook metrics directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open hook metrics %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("append hook metrics: %w", err)
	}
	return nil
}

func ReadEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var events []Event
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse hook metrics %s line %d: %w", path, line, err)
		}
		if event.SchemaVersion != EventSchemaVersion {
			return nil, fmt.Errorf("hook metrics %s line %d uses schema version %d; supported version is %d", path, line, event.SchemaVersion, EventSchemaVersion)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type DailyMetrics struct {
	Date              string `json:"date"`
	Commits           int    `json:"commits"`
	PushAttempts      int    `json:"push_attempts"`
	CommitChecks      int    `json:"commit_checks"`
	HookFailures      int    `json:"hook_failures"`
	HookRuns          int    `json:"hook_runs"`
	TotalDurationMS   int64  `json:"total_duration_ms"`
	AverageDurationMS int64  `json:"average_duration_ms"`
}

type MetricsSummary struct {
	From              string         `json:"from"`
	Through           string         `json:"through"`
	RepositoryFilter  string         `json:"repository_filter,omitempty"`
	Commits           int            `json:"commits"`
	PushAttempts      int            `json:"push_attempts"`
	CommitChecks      int            `json:"commit_checks"`
	HookFailures      int            `json:"hook_failures"`
	HookRuns          int            `json:"hook_runs"`
	AverageDurationMS int64          `json:"average_duration_ms"`
	Days              []DailyMetrics `json:"days"`
}

func Summarize(events []Event, days int, repositoryFilter string, now time.Time) MetricsSummary {
	if days < 1 {
		days = 1
	}
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	from := today.AddDate(0, 0, -(days - 1))
	summary := MetricsSummary{
		From:             from.Format("2006-01-02"),
		Through:          today.Format("2006-01-02"),
		RepositoryFilter: repositoryFilter,
		Days:             make([]DailyMetrics, 0, days),
	}
	byDate := map[string]*DailyMetrics{}
	for offset := 0; offset < days; offset++ {
		date := from.AddDate(0, 0, offset).Format("2006-01-02")
		daily := &DailyMetrics{Date: date}
		byDate[date] = daily
		summary.Days = append(summary.Days, *daily)
	}
	for _, event := range events {
		if repositoryFilter != "" && !strings.Contains(strings.ToLower(event.Repository), strings.ToLower(repositoryFilter)) {
			continue
		}
		date := event.Timestamp.In(location).Format("2006-01-02")
		daily, ok := byDate[date]
		if !ok {
			continue
		}
		daily.HookRuns++
		daily.TotalDurationMS += event.DurationMS
		switch event.Action {
		case "commit":
			daily.Commits++
		case "push-attempt":
			daily.PushAttempts++
		case "commit-check":
			daily.CommitChecks++
		}
		if event.Outcome == "failed" {
			daily.HookFailures++
		}
	}
	// byDate stores pointers while Days stores values; copy finalized values in
	// chronological order to retain a stable JSON and chart contract.
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	summary.Days = summary.Days[:0]
	var totalDuration int64
	for _, date := range dates {
		daily := byDate[date]
		if daily.HookRuns > 0 {
			daily.AverageDurationMS = daily.TotalDurationMS / int64(daily.HookRuns)
		}
		summary.Days = append(summary.Days, *daily)
		summary.Commits += daily.Commits
		summary.PushAttempts += daily.PushAttempts
		summary.CommitChecks += daily.CommitChecks
		summary.HookFailures += daily.HookFailures
		summary.HookRuns += daily.HookRuns
		totalDuration += daily.TotalDurationMS
	}
	if summary.HookRuns > 0 {
		summary.AverageDurationMS = totalDuration / int64(summary.HookRuns)
	}
	return summary
}
