package main

import (
	"io"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/githubchecks"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
)

type ciWaitProgress cliprogress.Checks

func newCIWaitProgress(out io.Writer, enabled bool) *ciWaitProgress {
	return (*ciWaitProgress)(cliprogress.NewChecks(out, enabled))
}

func (p *ciWaitProgress) report(event githubchecks.PullRequestWaitProgress) {
	(*cliprogress.Checks)(p).Report(event)
}
func (p *ciWaitProgress) operationReporter(operation string) progresspkg.Reporter {
	return (*cliprogress.Checks)(p).OperationReporter(operation)
}
func (p *ciWaitProgress) finishOperation(message string) {
	(*cliprogress.Checks)(p).FinishOperation(message)
}
func (p *ciWaitProgress) start(repository, pullRequest, target, head string) {
	(*cliprogress.Checks)(p).Start(repository, pullRequest, target, head)
}
func (p *ciWaitProgress) fail(err error) { (*cliprogress.Checks)(p).Fail(err) }

func (p *ciWaitProgress) update(message string) { (*cliprogress.Checks)(p).Update(message) }
