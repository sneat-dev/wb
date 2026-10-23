package depsrun

import (
	"context"
	"fmt"
	goversion "go/version"
	"path/filepath"
	"sort"

	"github.com/sneat-dev/wb/internal/deps"
)

type DirectiveRow struct {
	Repository   string                   `json:"repository"`
	Module       string                   `json:"module,omitempty"`
	Verdict      string                   `json:"verdict"`
	Detail       string                   `json:"detail"`
	Forcing      []deps.ForcingDependency `json:"forcing,omitempty"`
	CodeQLAtRisk bool                     `json:"codeQLAtRisk,omitempty"`
}

const verdictNoModule = "no-module"

type DirectiveCheckRequest struct {
	Directory     string
	Policy        deps.DirectivePolicy
	Options       deps.Options
	Apply         bool
	CodeQLCeiling string
}
type DirectiveCheckRow struct {
	Label      string
	Assessment deps.DirectiveAssessment
	Error      error
	Detail     string
}
type DirectiveCheckResult struct{ ModuleCount, Attention int }

func (service *Service) CheckDirectives(ctx context.Context, request DirectiveCheckRequest, emit func(DirectiveCheckRow)) (DirectiveCheckResult, error) {
	absolute, err := service.deps.Abs(request.Directory)
	if err != nil {
		return DirectiveCheckResult{}, err
	}
	modules := service.deps.DiscoverModules(absolute)
	result := DirectiveCheckResult{ModuleCount: len(modules)}
	for _, moduleDir := range modules {
		row := DirectiveCheckRow{Label: moduleLabel(absolute, moduleDir)}
		if request.Apply {
			row.Assessment, row.Error = service.deps.ApplyDirective(ctx, moduleDir, request.Policy, request.Options)
		} else {
			row.Assessment, row.Error = service.deps.AssessDirective(ctx, moduleDir, request.Policy, request.Options)
		}
		if row.Error != nil {
			result.Attention++
			emit(row)
			continue
		}
		row.Detail = row.Assessment.Detail
		if risk, note := codeQLRisk(row.Assessment, request.CodeQLCeiling); risk {
			row.Detail += " — " + note
		}
		emit(row)
		if needsAttention(row.Assessment.Verdict, request.Apply) {
			result.Attention++
		}
	}
	return result, nil
}
func codeQLRisk(assessment deps.DirectiveAssessment, ceiling string) (bool, string) {
	effective := assessment.EffectiveGoVersion()
	if effective == "" || ceiling == "" {
		return false, ""
	}
	if goversion.Compare(goversion.Lang(goSyntaxLocal(effective)), goversion.Lang(goSyntaxLocal(ceiling))) <= 0 {
		return false, ""
	}
	return true, fmt.Sprintf("CodeQL default setup would fail here: requires go %s, pinned to go%s (GOTOOLCHAIN=local)", effective, ceiling)
}

func goSyntaxLocal(v string) string {
	if len(v) >= 2 && v[:2] == "go" {
		return v
	}
	return "go" + v
}

func needsAttention(verdict deps.DirectiveVerdict, applying bool) bool {
	switch verdict {
	case deps.DirectiveCannotComply, deps.DirectiveError:
		return true
	case deps.DirectiveWouldChange:
		return !applying
	default:
		return false
	}
}

func moduleLabel(root, moduleDir string) string {
	relative, err := filepath.Rel(root, moduleDir)
	if err != nil || relative == "." {
		return filepath.Base(root)
	}
	return relative
}

func (service *Service) ReportDirectives(ctx context.Context, repositories []deps.Repository, policy deps.DirectivePolicy, options deps.Options, codeQLCeiling string) []DirectiveRow {
	var rows []DirectiveRow
	for _, repository := range repositories {
		if repository.Path == "" {
			rows = append(rows, DirectiveRow{Repository: repository.Slug, Verdict: verdictNoModule, Detail: "remote-only — not cloned locally, cannot be assessed"})
			continue
		}
		modules := service.deps.DiscoverModules(repository.Path)
		if len(modules) == 0 {
			rows = append(rows, DirectiveRow{Repository: repository.Slug, Verdict: verdictNoModule, Detail: "no Go module"})
			continue
		}
		for _, moduleDir := range modules {
			row := DirectiveRow{Repository: repository.Slug, Module: moduleLabel(repository.Path, moduleDir)}
			assessment, err := service.deps.AssessDirective(ctx, moduleDir, policy, options)
			if err != nil {
				row.Verdict = string(deps.DirectiveError)
				row.Detail = err.Error()
				rows = append(rows, row)
				continue
			}
			row.Module = assessment.ModulePath
			row.Verdict = string(assessment.Verdict)
			row.Detail = assessment.Detail
			row.Forcing = assessment.Forcing
			if atRisk, note := codeQLRisk(assessment, codeQLCeiling); atRisk {
				row.CodeQLAtRisk = true
				row.Detail += " — " + note
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repository != rows[j].Repository {
			return rows[i].Repository < rows[j].Repository
		}
		return rows[i].Module < rows[j].Module
	})
	return rows
}
