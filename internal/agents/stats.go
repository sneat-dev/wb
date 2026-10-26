package agents

import "sort"

// StatsBucket sums observed execution facts. UsageRuns and CostRuns make
// missing harness telemetry distinct from a measured zero.
type StatsBucket struct {
	SessionID        string  `json:"session_id"`
	SessionName      string  `json:"session_name,omitempty"`
	Repository       string  `json:"repository"`
	Runs             int     `json:"runs"`
	Completed        int     `json:"completed"`
	Failed           int     `json:"failed"`
	Pending          int     `json:"pending"`
	DurationMS       int64   `json:"duration_ms"`
	ToolCalls        int     `json:"tool_calls"`
	UsageRuns        int     `json:"usage_runs"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CostRuns         int     `json:"cost_runs"`
	EstimatedUSD     float64 `json:"estimated_usd"`
}

type StatsReport struct {
	Total               StatsBucket   `json:"total"`
	BySession           []StatsBucket `json:"by_session"`
	ByRepository        []StatsBucket `json:"by_repository"`
	BySessionRepository []StatsBucket `json:"by_session_repository"`
}

// SummarizeRuns groups each durable run once along each requested dimension.
// Older records without caller identity remain in an explicit empty-ID group.
func SummarizeRuns(runs []Result) StatsReport {
	report := StatsReport{
		BySession: []StatsBucket{}, ByRepository: []StatsBucket{}, BySessionRepository: []StatsBucket{},
	}
	bySession := map[string]*StatsBucket{}
	byRepo := map[string]*StatsBucket{}
	byPair := map[string]*StatsBucket{}
	for _, run := range runs {
		id, name := run.Caller.ID, run.Caller.Name
		addRun(&report.Total, run)
		addGroupedRun(bySession, id, id, name, "", run)
		addGroupedRun(byRepo, run.Repository, "", "", run.Repository, run)
		addGroupedRun(byPair, id+"\x00"+run.Repository, id, name, run.Repository, run)
	}
	for _, bucket := range bySession {
		report.BySession = append(report.BySession, *bucket)
	}
	for _, bucket := range byRepo {
		report.ByRepository = append(report.ByRepository, *bucket)
	}
	for _, bucket := range byPair {
		// A name may have been reported by a different run of this caller in
		// another repository. Keep it visible on every session/repository row.
		if bucket.SessionName == "" {
			bucket.SessionName = bySession[bucket.SessionID].SessionName
		}
		report.BySessionRepository = append(report.BySessionRepository, *bucket)
	}
	sort.Slice(report.BySession, func(i, j int) bool { return report.BySession[i].SessionID < report.BySession[j].SessionID })
	sort.Slice(report.ByRepository, func(i, j int) bool { return report.ByRepository[i].Repository < report.ByRepository[j].Repository })
	sort.Slice(report.BySessionRepository, func(i, j int) bool {
		if report.BySessionRepository[i].SessionID != report.BySessionRepository[j].SessionID {
			return report.BySessionRepository[i].SessionID < report.BySessionRepository[j].SessionID
		}
		return report.BySessionRepository[i].Repository < report.BySessionRepository[j].Repository
	})
	return report
}

func addGroupedRun(groups map[string]*StatsBucket, key, sessionID, name, repository string, run Result) {
	bucket := groups[key]
	if bucket == nil {
		bucket = &StatsBucket{SessionID: sessionID, SessionName: name, Repository: repository}
		groups[key] = bucket
	}
	if bucket.SessionName == "" {
		bucket.SessionName = name
	}
	addRun(bucket, run)
}

func addRun(bucket *StatsBucket, run Result) {
	bucket.Runs++
	switch run.Status {
	case StatusCompleted:
		bucket.Completed++
	case StatusFailed, StatusCancelled, StatusTimedOut, StatusAbandoned:
		bucket.Failed++
	default:
		bucket.Pending++
	}
	bucket.DurationMS += run.DurationMS
	bucket.ToolCalls += run.ToolCalls
	if tokens := run.Telemetry.Tokens; tokens != nil {
		bucket.UsageRuns++
		if tokens.Input != nil {
			bucket.InputTokens += int64(*tokens.Input)
		}
		if tokens.Output != nil {
			bucket.OutputTokens += int64(*tokens.Output)
		}
		if tokens.CacheRead != nil {
			bucket.CacheReadTokens += int64(*tokens.CacheRead)
		}
		if tokens.CacheWrite != nil {
			bucket.CacheWriteTokens += int64(*tokens.CacheWrite)
		}
		if tokens.Reasoning != nil {
			bucket.ReasoningTokens += int64(*tokens.Reasoning)
		}
	} else if run.Usage != nil {
		// Pre-telemetry records used only the legacy observed-usage shape.
		bucket.UsageRuns++
		bucket.InputTokens += int64(run.Usage.InputTokens)
		bucket.OutputTokens += int64(run.Usage.OutputTokens)
		bucket.CacheReadTokens += int64(run.Usage.CachedInputTokens)
		bucket.CacheWriteTokens += int64(run.Usage.CacheWriteInputTokens)
		bucket.ReasoningTokens += int64(run.Usage.ReasoningOutputTokens)
	}
	if run.Cost.USD != nil {
		bucket.CostRuns++
		bucket.EstimatedUSD += *run.Cost.USD
	}
}
