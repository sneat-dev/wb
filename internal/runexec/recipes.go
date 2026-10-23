package runexec

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/recipe"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"sort"
)

type RecipeRequest struct {
	ProjectsRoot, Filter, ConfigPath, Name string
	ExtraOrgs                              []string
	List, Apply                            bool
	// Observe renders warnings and rows before further effects; required for named runs.
	Observe func(RecipeEvent) error
}
type RecipeBucket uint8

const (
	Updated RecipeBucket = iota
	Skipped
	Archived
	Forked
	Failed
)

type RecipeRow struct {
	Bucket RecipeBucket
	Text   string
}
type RecipeEvent struct {
	DryRun bool
	Row    RecipeRow
}
type RecipeResult struct {
	Names           []string
	Rows            []RecipeRow
	Drift, Findings bool
}

// RecipeFailure identifies the existing diagnostic prefix without CLI exit types.
type RecipeFailure struct {
	Discovery, Unknown bool
	Err                error
}

func (failure *RecipeFailure) Error() string { return failure.Err.Error() }
func (failure *RecipeFailure) Unwrap() error { return failure.Err }

type RecipeOperations struct {
	LoadConfig    func(string) (recipe.Config, error)
	Discover      func(string, string, []string) ([]discover.Repo, error)
	DefaultBranch func(string) (string, error)
	Fetch         func(string) error
	Land          func(recipe.Recipe, string, string) (gitops.Outcome, error)
}

func NewRecipes(discoverRepos func(string, string, []string) ([]discover.Repo, error)) RecipeOperations {
	return RecipeOperations{recipe.LoadConfig, discoverRepos, gitops.DefaultBranch, gitops.Fetch, recipe.Land}
}
func (ops RecipeOperations) Run(_ context.Context, request RecipeRequest) (RecipeResult, error) {
	result := RecipeResult{}
	path := request.ConfigPath
	if path == "" {
		path = wbconfig.DefaultPath()
	}
	cfg, err := ops.LoadConfig(path)
	if err != nil {
		return result, &RecipeFailure{Err: err}
	}
	if request.List || request.Name == "" {
		for name := range cfg.Recipes {
			result.Names = append(result.Names, name)
		}
		sort.Strings(result.Names)
		return result, nil
	}
	r, ok := cfg.Recipes[request.Name]
	if !ok {
		return result, &RecipeFailure{Unknown: true, Err: fmt.Errorf("unknown recipe %q (see `wb run --list`)", request.Name)}
	}
	repos, err := ops.Discover(request.ProjectsRoot, request.Filter, append([]string(nil), request.ExtraOrgs...))
	if err != nil {
		return result, &RecipeFailure{Discovery: true, Err: err}
	}
	if !request.Apply {
		if err := request.Observe(RecipeEvent{DryRun: true}); err != nil {
			return result, err
		}
	}
	record := func(bucket RecipeBucket, text string) error {
		row := RecipeRow{bucket, text}
		result.Rows = append(result.Rows, row)
		if bucket == Failed {
			result.Findings = true
		}
		return request.Observe(RecipeEvent{Row: row})
	}
	for _, repo := range repos {
		var bucket RecipeBucket
		var text string
		switch {
		case repo.Archived:
			bucket, text = Archived, repo.Slug()
		case !repo.Remote:
			bucket, text = Skipped, repo.Slug()+" — local-only (not under your GitHub orgs)"
		case repo.IsFork:
			bucket, text = Forked, repo.Slug()
		case repo.Path == "":
			bucket, text = Skipped, repo.Slug()+" — remote-only (clone to evaluate)"
		default:
			applies, applyErr := r.AppliesTo(repo.Path)
			switch {
			case applyErr != nil:
				bucket, text = Failed, repo.Slug()+" — "+applyErr.Error()
			case !applies:
				bucket, text = Skipped, repo.Slug()+" — recipe does not apply"
			case !request.Apply:
				preview, previewErr := recipe.Evaluate(r, repo.Path)
				switch {
				case previewErr != nil:
					bucket, text = Failed, repo.Slug()+" — "+previewErr.Error()
				case !preview.Changed:
					bucket, text = Skipped, repo.Slug()+" — "+preview.Summary
				default:
					result.Drift = true
					bucket, text = Updated, repo.Slug()+" — would "+preview.Summary
				}
			default:
				outcome, landErr := ops.apply(r, repo)
				switch {
				case landErr != nil:
					bucket, text = Failed, repo.Slug()+" — "+landErr.Error()
				case !outcome.Changed:
					bucket, text = Skipped, repo.Slug()+" — "+outcome.Detail
				default:
					bucket, text = Updated, repo.Slug()+" — "+outcome.Detail
				}
			}
		}
		if err := record(bucket, text); err != nil {
			return result, err
		}
	}
	result.Findings = result.Findings || (!request.Apply && result.Drift)
	return result, nil
}
func (ops RecipeOperations) apply(r recipe.Recipe, repo discover.Repo) (gitops.Outcome, error) {
	branch, err := ops.DefaultBranch(repo.Path)
	if err != nil {
		if err := ops.Fetch(repo.Path); err != nil {
			return gitops.Outcome{}, err
		}
		branch, err = ops.DefaultBranch(repo.Path)
		if err != nil {
			return gitops.Outcome{}, err
		}
	}
	return ops.Land(r, repo.Path, branch)
}
