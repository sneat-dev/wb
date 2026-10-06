package cmdquality

import (
	"context"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
)

type Dependencies struct {
	Coverage              func(context.Context, qualityrun.CoverageRequest) (qualityrun.CoverageResult, error)
	Verification          func(context.Context, qualityrun.VerificationRequest) (qualityrun.VerificationResult, error)
	Changed               func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error)
	Stored                func(context.Context, qualityrun.StoredRequest) (qualityrun.StoredResult, error)
	Baseline              func(context.Context, qualityrun.BaselineRequest) error
	Summary               func(context.Context, qualityrun.SummaryRequest) error
	Worklist              func(context.Context, qualityrun.WorklistRequest) (quality.Worklist, error)
	Analyze               func(context.Context, string, quality.DeadcodeOptions) (quality.DeadcodeReport, error)
	WriteDeadcodeBaseline func(string, []quality.DeadcodeFinding) error
	Abs                   func(string) (string, error)
	WorkflowAnnotations   func() bool
}
