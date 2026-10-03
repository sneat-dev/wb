package main

import (
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/progress"
	"io"
	"time"
)

// Temporary adapters serve the remaining dependency and worktree consumers.
// Presentation itself lives exclusively in the shared CLI leaf.
type campaignProgress cliprogress.Campaign

func newCampaignProgress(out io.Writer, enabled bool, operation string) *campaignProgress {
	return (*campaignProgress)(cliprogress.NewCampaign(out, enabled, operation))
}
func newCampaignProgressWithHeartbeat(out io.Writer, enabled bool, operation string, heartbeat time.Duration) *campaignProgress {
	return (*campaignProgress)(cliprogress.NewCampaignWithHeartbeat(out, enabled, operation, heartbeat))
}
func (p *campaignProgress) reporter() progress.Reporter { return (*cliprogress.Campaign)(p).Reporter() }
func (p *campaignProgress) finish(message string)       { (*cliprogress.Campaign)(p).Finish(message) }
