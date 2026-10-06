package qualityrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
)

type BaselineRequest struct {
	Profile, Module, SHA, Out string
	IncludeE2E                bool
}
type SummaryRequest struct {
	Profile, Module, Out string
	Meta                 quality.CoverageSummaryMeta
}
type WorklistRequest struct {
	Profile, Module string
	UnitSize        int
}

func Baseline(_ context.Context, request BaselineRequest) error {
	module, err := quality.ReadModulePath(request.Module)
	if err != nil {
		return err
	}
	blocks, err := quality.ParseCoverageProfile(request.Profile)
	if err != nil {
		return err
	}
	baseline := quality.BaselineFromProfile(blocks, module, request.SHA)
	baseline.IncludeE2E = request.IncludeE2E
	return quality.WriteBaseline(request.Out, baseline)
}
func Summary(_ context.Context, request SummaryRequest) error {
	module, err := quality.ReadModulePath(request.Module)
	if err != nil {
		return err
	}
	blocks, err := quality.ParseCoverageProfile(request.Profile)
	if err != nil {
		return err
	}
	summary := quality.SummaryFromProfile(blocks, module, request.Meta)
	if err := quality.WriteCoverageSummary(request.Out, summary); err != nil {
		return fmt.Errorf("write coverage summary %s: %w", request.Out, err)
	}
	return nil
}
func Worklist(_ context.Context, request WorklistRequest) (quality.Worklist, error) {
	module, err := quality.ReadModulePath(request.Module)
	if err != nil {
		return quality.Worklist{}, err
	}
	blocks, err := quality.ParseCoverageProfile(request.Profile)
	if err != nil {
		return quality.Worklist{}, err
	}
	return quality.BuildWorklist(blocks, module, request.Module, request.UnitSize)
}
