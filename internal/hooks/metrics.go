package hooks

import (
	"bufio"
	"bytes"
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
	Profile       string            `json:"profile,omitempty"`
	Block         string            `json:"block,omitempty"`
	Action        string            `json:"action"`
	Outcome       string            `json:"outcome"`
	DurationMS    int64             `json:"duration_ms"`
	Commit        string            `json:"commit,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type eventContext struct {
	repository string
	commit     string
	branch     string
	labels     map[string]string
}

func loadEventContext(repoRoot string, labels map[string]string) eventContext {
	commit, _ := gitOutput(repoRoot, "rev-parse", "HEAD")
	branch, _ := gitOutput(repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	return eventContext{
		repository: originSlug(repoRoot),
		commit:     commit,
		branch:     branch,
		labels:     cloneLabels(labels),
	}
}

func (context eventContext) newBlockEvent(hook string, block HookBlock, success bool, duration time.Duration, timestamp time.Time) Event {
	event := context.newEvent(hook, success, duration, timestamp)
	event.Action = "hook-block"
	event.Profile = block.Profile
	event.Block = block.ID
	return event
}

func (context eventContext) newEvent(hook string, success bool, duration time.Duration, timestamp time.Time) Event {
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
	return Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     timestamp.UTC(),
		Repository:    context.repository,
		Hook:          hook,
		Action:        action,
		Outcome:       outcome,
		DurationMS:    duration.Milliseconds(),
		Commit:        context.commit,
		Branch:        context.branch,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Labels:        cloneLabels(context.labels),
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
	return AppendEvents(path, []Event{event})
}

func AppendEvents(path string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create hook metrics directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open hook metrics %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect hook metrics %s: %w", path, err)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			return err
		}
	}
	if _, err := file.Write(data.Bytes()); err != nil {
		_ = file.Close()
		return fmt.Errorf("append hook metrics: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close hook metrics: %w", err)
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
	Blocks            []BlockMetrics `json:"blocks,omitempty"`
}

type BlockMetrics struct {
	ID                string `json:"id"`
	Profile           string `json:"profile"`
	Hook              string `json:"hook"`
	Runs              int    `json:"runs"`
	Failures          int    `json:"failures"`
	TotalDurationMS   int64  `json:"total_duration_ms"`
	AverageDurationMS int64  `json:"average_duration_ms"`
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
	byBlock := map[string]*BlockMetrics{}
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
		if event.Action == "hook-block" {
			block := byBlock[event.Block]
			if block == nil {
				block = &BlockMetrics{ID: event.Block, Profile: event.Profile, Hook: event.Hook}
				byBlock[event.Block] = block
			}
			block.Runs++
			block.TotalDurationMS += event.DurationMS
			if event.Outcome == "failed" {
				block.Failures++
			}
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
	blockIDs := make([]string, 0, len(byBlock))
	for id := range byBlock {
		blockIDs = append(blockIDs, id)
	}
	sort.Strings(blockIDs)
	for _, id := range blockIDs {
		block := byBlock[id]
		if block.Runs > 0 {
			block.AverageDurationMS = block.TotalDurationMS / int64(block.Runs)
		}
		summary.Blocks = append(summary.Blocks, *block)
	}
	return summary
}
