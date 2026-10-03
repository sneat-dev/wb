package main

import (
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"io"
	"time"
)

type liveProgress cliprogress.Live

const universalProgressHeartbeat = cliprogress.Heartbeat

func newLiveProgress(out io.Writer, enabled bool) *liveProgress {
	return (*liveProgress)(cliprogress.NewLive(out, enabled))
}
func newLiveProgressWithHeartbeat(out io.Writer, enabled bool, heartbeat time.Duration) *liveProgress {
	return (*liveProgress)(cliprogress.NewLiveWithHeartbeat(out, enabled, heartbeat))
}
func (p *liveProgress) start(message string)     { (*cliprogress.Live)(p).Start(message) }
func (p *liveProgress) update(message string)    { (*cliprogress.Live)(p).Update(message) }
func (p *liveProgress) finish(message string)    { (*cliprogress.Live)(p).Finish(message) }
func (p *liveProgress) printLine(message string) { (*cliprogress.Live)(p).PrintLine(message) }
func progressOutput(out io.Writer, interactive bool) io.Writer {
	return cliprogress.Output(out, interactive)
}

func (p *liveProgress) Enabled() bool { return (*cliprogress.Live)(p).Enabled() }
