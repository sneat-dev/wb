package depsrun

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/policy"
)

func (s *PolicyService) Check(_ context.Context, request PolicyRequest) (policy.Result, error) {
	context, err := s.resolvePolicy(request.ProjectsRoot, request.Directory, request.Policy)
	if err != nil {
		return policy.Result{}, err
	}
	declared := request.DeclaredType
	if declared == "" {
		declared = context.declaredType()
	}
	result, err := s.deps.Check(context.loaded, context.module, declared)
	if err != nil {
		return policy.Result{}, s.usage(err.Error())
	}
	if request.Strict || context.config.Strict {
		result.ApplyStrict()
	}
	return result, nil
}
func (s *PolicyService) Explain(_ context.Context, request PolicyRequest, importPath string) (policy.Explanation, error) {
	context, err := s.resolvePolicy(request.ProjectsRoot, request.Directory, request.Policy)
	if err != nil {
		return policy.Explanation{}, err
	}
	declared := request.DeclaredType
	if declared == "" {
		declared = context.declaredType()
	}
	result, err := s.deps.Explain(context.loaded, context.module.Path, declared, importPath)
	if err != nil {
		return policy.Explanation{}, s.usage(err.Error())
	}
	return result, nil
}
func (s *PolicyService) Describe(_ context.Context, request PolicyRequest) (policy.Effective, error) {
	context, err := s.resolvePolicy(request.ProjectsRoot, request.Directory, request.Policy)
	if err != nil {
		return policy.Effective{}, err
	}
	declared := request.DeclaredType
	if declared == "" {
		declared = context.declaredType()
	}
	result, err := s.deps.Describe(context.loaded, context.module.Path, declared, context.config.Path, context.config.Strict)
	if err != nil {
		return policy.Effective{}, s.usage(err.Error())
	}
	return result, nil
}
func (s *PolicyService) Validate(_ context.Context, path string) (PolicyValidation, error) {
	loaded, err := s.deps.Load(path)
	if err != nil {
		return PolicyValidation{}, s.usage(err.Error())
	}
	return PolicyValidation{GroupCount: len(loaded.Groups), TypeCount: len(loaded.Types), Diagnostics: policy.Validate(loaded)}, nil
}
func (s *PolicyService) Expectations(_ context.Context, path string) ([]policy.ExpectationResult, error) {
	loaded, err := s.deps.Load(path)
	if err != nil {
		return nil, s.usage(err.Error())
	}
	return policy.RunExpectations(loaded), nil
}
func (s *PolicyService) Init(_ context.Context, request PolicyRequest, written func(PolicyInitNotice)) (policy.Result, error) {
	if request.Policy == "" {
		return policy.Result{}, s.usage("--policy is required: name the policy that governs this repository")
	}
	context, err := s.resolvePolicy(request.ProjectsRoot, request.Directory, request.Policy)
	if err != nil {
		return policy.Result{}, err
	}
	target := filepath.Join(context.moduleDir, policy.ConfigFileName)
	if _, err := s.deps.Stat(target); err == nil {
		return policy.Result{}, s.usage(fmt.Sprintf("%s already exists", target))
	}
	body := fmt.Sprintf("policy: %s\n", request.Policy)
	detected, detectErr := context.loaded.Detect(context.module.Path)
	if detectErr != nil {
		return policy.Result{}, s.usage(fmt.Sprintf("%s\nAdd a \"type:\" line naming one of: %s",
			detectErr, strings.Join(context.loaded.TypeNames(), ", ")))
	}
	if err := s.deps.WriteFile(target, []byte(body), 0o600); err != nil {
		return policy.Result{}, err
	}
	written(PolicyInitNotice{Path: target, DetectedType: detected})

	result, err := s.deps.Check(context.loaded, context.module, "")
	if err != nil {
		return policy.Result{}, s.usage(err.Error())
	}
	return result, nil
}

type PolicyModuleOutcome struct {
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Directory  string `json:"-"`
	Type       string `json:"type,omitempty"`
	PolicyRef  string `json:"policy,omitempty"`

	Governed bool   `json:"governed"`
	Skipped  string `json:"skipped,omitempty"`
	Blocking int    `json:"blocking"`
	Reported int    `json:"reported"`

	Findings []policy.Finding `json:"-"`
}

func (s *PolicyService) sweep(projectsRoot string, repositories []deps.Repository, policyOverride string) []PolicyModuleOutcome {
	var outcomes []PolicyModuleOutcome
	for _, repository := range repositories {
		for _, moduleDir := range DiscoverModules(repository.Path) {
			outcome := PolicyModuleOutcome{
				Repository: repository.Slug,
				Directory:  moduleDir,
			}
			context, err := s.resolvePolicy(projectsRoot, moduleDir, policyOverride)
			if err != nil {
				outcome.Skipped = strings.SplitN(err.Error(), "\n", 2)[0]
				if module, scanErr := s.deps.Scan(moduleDir); scanErr == nil {
					outcome.Module = module.Path
				}
				outcomes = append(outcomes, outcome)
				continue
			}
			outcome.Module = context.module.Path
			outcome.PolicyRef = context.loaded.Source
			outcome.Governed = true

			result, err := s.deps.Check(context.loaded, context.module, context.declaredType())
			if err != nil {
				outcome.Governed = false
				outcome.Skipped = err.Error()
				outcomes = append(outcomes, outcome)
				continue
			}
			if context.config.Strict {
				result.ApplyStrict()
			}
			outcome.Type = result.Type
			outcome.Blocking = result.Blocking()
			outcome.Reported = result.Reported()
			outcome.Findings = result.Findings
			outcomes = append(outcomes, outcome)
		}
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].Repository != outcomes[j].Repository {
			return outcomes[i].Repository < outcomes[j].Repository
		}
		return outcomes[i].Directory < outcomes[j].Directory
	})
	return outcomes
}
func (s *PolicyService) Report(ctx context.Context, request PolicyFleetRequest) ([]PolicyModuleOutcome, error) {
	repos, err := s.deps.Select(ctx, request.Selection)
	if err != nil {
		return nil, s.usage(err.Error())
	}
	return s.sweep(request.Selection.ProjectsRoot, repos, request.Policy), nil
}
func (s *PolicyService) Drift(ctx context.Context, request PolicyFleetRequest) (PolicyDriftResult, error) {
	repositories, err := s.deps.Select(ctx, request.Selection)
	if err != nil {
		return PolicyDriftResult{}, s.usage(err.Error())
	}
	var rows []PolicyDriftRow
	issues := 0
	for _, repository := range repositories {
		for _, moduleDir := range DiscoverModules(repository.Path) {
			row := PolicyDriftRow{Repository: repository.Slug}
			context, err := s.resolvePolicy(request.Selection.ProjectsRoot, moduleDir, request.Policy)
			if err != nil {
				row.Issue = "no policy: " + strings.SplitN(err.Error(), "\n", 2)[0]
				if module, scanErr := s.deps.Scan(moduleDir); scanErr == nil {
					row.Module = module.Path
				} else {
					row.Module = filepath.Base(moduleDir)
				}
				issues++
				rows = append(rows, row)
				continue
			}
			row.Module = context.module.Path
			row.Policy = context.loaded.Source
			row.Declared = context.declaredType()
			if detected, detectErr := context.loaded.Detect(context.module.Path); detectErr == nil {
				row.Detected = detected
			}
			if row.Declared != "" && row.Detected != "" && row.Declared != row.Detected {
				row.Issue = fmt.Sprintf("declared %q but detection chooses %q", row.Declared, row.Detected)
				issues++
			}
			if row.Declared == "" && row.Detected == "" {
				row.Issue = "no type declared and none detected"
				issues++
			}
			rows = append(rows, row)
		}
	}
	return PolicyDriftResult{Rows: rows, Issues: issues}, nil
}
func (s *PolicyService) Impact(ctx context.Context, request PolicyFleetRequest, candidateArgument string) (PolicyImpactResult, error) {
	candidatePath, err := s.deps.Abs(candidateArgument)
	if err != nil {
		return PolicyImpactResult{}, s.usage(err.Error())
	}
	if _, err := s.deps.Load(candidatePath); err != nil {
		return PolicyImpactResult{}, s.usage(err.Error())
	}
	repositories, err := s.deps.Select(ctx, request.Selection)
	if err != nil {
		return PolicyImpactResult{}, s.usage(err.Error())
	}
	baseline := s.sweep(request.Selection.ProjectsRoot, repositories, "")
	candidate := s.sweep(request.Selection.ProjectsRoot, repositories, candidatePath)

	index := map[string]PolicyModuleOutcome{}
	for _, outcome := range baseline {
		index[outcome.Directory] = outcome
	}
	var newlyFailing, newlyPassing, unchanged []PolicyChange
	for _, outcome := range candidate {
		before, ok := index[outcome.Directory]
		if !ok || !before.Governed || !outcome.Governed {
			continue
		}
		entry := PolicyChange{Repository: outcome.Repository, Module: outcome.Module, Before: before.Blocking, After: outcome.Blocking}
		switch {
		case before.Blocking == 0 && outcome.Blocking > 0:
			newlyFailing = append(newlyFailing, entry)
		case before.Blocking > 0 && outcome.Blocking == 0:
			newlyPassing = append(newlyPassing, entry)
		default:
			unchanged = append(unchanged, entry)
		}
	}
	return PolicyImpactResult{Candidate: candidateArgument, NewlyFailing: newlyFailing, NewlyPassing: newlyPassing, Unchanged: len(unchanged)}, nil
}
