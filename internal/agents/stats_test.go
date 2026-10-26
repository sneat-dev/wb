package agents

import "testing"

func TestSummarizeRunsKeepsUnknownUsageAndCallerSeparate(t *testing.T) {
	input, output, cache := 20, 7, 9
	cost := 0.25
	runs := []Result{
		{Caller: CallerSession{ID: "thread-1", Name: "alpha"}, Repository: "acme/app", Status: StatusCompleted,
			DurationMS: 1200, ToolCalls: 3, Telemetry: Telemetry{Tokens: &Tokens{Input: &input, Output: &output, CacheRead: &cache}}, Cost: CostRecord{USD: &cost}},
		{Caller: CallerSession{ID: "thread-1"}, Repository: "acme/lib", Status: StatusFailed,
			DurationMS: 300, ToolCalls: 1, Usage: &Usage{InputTokens: 4, OutputTokens: 2}},
		{Repository: "acme/app", Status: StatusRunning},
	}
	report := SummarizeRuns(runs)
	if report.Total.Runs != 3 || report.Total.Completed != 1 || report.Total.Failed != 1 || report.Total.Pending != 1 ||
		report.Total.DurationMS != 1500 || report.Total.UsageRuns != 2 || report.Total.CostRuns != 1 ||
		report.Total.InputTokens != 24 || report.Total.OutputTokens != 9 || report.Total.CacheReadTokens != 9 ||
		report.Total.EstimatedUSD != 0.25 {
		t.Fatalf("totals hide unavailable usage or miscount work: %+v", report.Total)
	}
	if len(report.BySession) != 2 || report.BySession[0].SessionID != "" ||
		report.BySession[1].SessionID != "thread-1" || report.BySession[1].SessionName != "alpha" ||
		len(report.ByRepository) != 2 || report.ByRepository[0].Repository != "acme/app" ||
		report.ByRepository[0].Runs != 2 || len(report.BySessionRepository) != 3 {
		t.Fatalf("grouping lost old records or repository boundaries: %+v", report)
	}
}
