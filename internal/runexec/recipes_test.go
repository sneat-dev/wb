package runexec

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/recipe"
	"testing"
)

func TestRecipeFailureOrderingAndObserverErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("failed effect")
	repo := discover.Repo{Org: "acme", Name: "app", Path: t.TempDir(), Remote: true}
	cfg := recipe.Config{Recipes: map[string]recipe.Recipe{"name": {Type: recipe.KindCommand, AppliesIf: "always", DryRunCommand: "true", Command: "true"}}}
	ops := NewRecipes(func(root, filter string, orgs []string) ([]discover.Repo, error) {
		if root != "/root" || filter != "filter" || len(orgs) != 1 || orgs[0] != "org" {
			t.Fatal("selection mismatch")
		}
		orgs[0] = "changed"
		return nil, sentinel
	})
	ops.LoadConfig = func(path string) (recipe.Config, error) {
		if path == "" {
			t.Fatal("default config path empty")
		}
		return cfg, nil
	}
	orgs := []string{"org"}
	request := RecipeRequest{ProjectsRoot: "/root", Filter: "filter", ExtraOrgs: orgs, Name: "name", Observe: func(RecipeEvent) error { t.Fatal("must not warn before discovery succeeds"); return nil }}
	_, err := ops.Run(context.Background(), request)
	var failure *RecipeFailure
	if !errors.As(err, &failure) || !failure.Discovery || !errors.Is(err, sentinel) || orgs[0] != "org" {
		t.Fatalf("failure=%v orgs%v", err, orgs)
	}
	ops.Discover = func(string, string, []string) ([]discover.Repo, error) { return []discover.Repo{repo}, nil }
	request.Observe = func(e RecipeEvent) error {
		if !e.DryRun {
			t.Fatal("preview after warning failure")
		}
		return sentinel
	}
	result, err := ops.Run(context.Background(), request)
	if err != sentinel || len(result.Rows) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	request.Apply = true
	ops.DefaultBranch = func(string) (string, error) { return "main", nil }
	ops.Land = func(recipe.Recipe, string, string) (gitops.Outcome, error) {
		return gitops.Outcome{Changed: true, Detail: "landed"}, nil
	}
	request.Observe = func(e RecipeEvent) error {
		if e.DryRun || e.Row.Bucket != Updated {
			t.Fatal(e)
		}
		return sentinel
	}
	result, err = ops.Run(context.Background(), request)
	if err != sentinel || len(result.Rows) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestRecipeApplyFallbackAndOutcomeContracts(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"fetch-error", "branch-error", "land-error", "unchanged", "changed"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("operation failed")
			branchCalls, fetches, lands := 0, 0, 0
			ops := RecipeOperations{DefaultBranch: func(string) (string, error) {
				branchCalls++
				if branchCalls == 1 || path == "branch-error" {
					return "", sentinel
				}
				return "main", nil
			}, Fetch: func(string) error {
				fetches++
				if path == "fetch-error" {
					return sentinel
				}
				return nil
			}, Land: func(_ recipe.Recipe, root, branch string) (gitops.Outcome, error) {
				lands++
				if root != "/repo" || branch != "main" {
					t.Fatal(root, branch)
				}
				if path == "land-error" {
					return gitops.Outcome{}, sentinel
				}
				return gitops.Outcome{Changed: path == "changed", Detail: "outcome"}, nil
			}}
			outcome, err := ops.apply(recipe.Recipe{}, discover.Repo{Path: "/repo"})
			if fetches != 1 {
				t.Fatal(fetches)
			}
			if path == "fetch-error" || path == "branch-error" || path == "land-error" {
				if err != sentinel || outcome.Changed {
					t.Fatalf("outcome=%+v err=%v", outcome, err)
				}
			} else if err != nil || outcome.Detail != "outcome" || outcome.Changed != (path == "changed") {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if path == "fetch-error" || path == "branch-error" {
				if lands != 0 {
					t.Fatal("land after refusal")
				}
			} else if lands != 1 {
				t.Fatal(lands)
			}
		})
	}
}
func TestRecipeConcretePreviewFailureCleanAndUnchangedLanding(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"preview-error", "preview-clean", "land-unchanged", "land-error"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			r := recipe.Recipe{Type: recipe.KindCommand, AppliesIf: "always", Command: "true", DryRunCommand: "exit 0"}
			if path == "preview-error" {
				r.DryRunCommand = "exit 127"
			}
			ops := fixtureRecipes([]discover.Repo{{Org: "acme", Name: "app", Path: t.TempDir(), Remote: true}})
			ops.LoadConfig = func(string) (recipe.Config, error) {
				return recipe.Config{Recipes: map[string]recipe.Recipe{"name": r}}, nil
			}
			ops.DefaultBranch = func(string) (string, error) { return "main", nil }
			ops.Land = func(recipe.Recipe, string, string) (gitops.Outcome, error) {
				if path == "land-error" {
					return gitops.Outcome{}, errors.New("cannot land")
				}
				return gitops.Outcome{Detail: "current"}, nil
			}
			result, err := ops.Run(context.Background(), RecipeRequest{Name: "name", Apply: path == "land-unchanged" || path == "land-error", Observe: func(RecipeEvent) error { return nil }})
			if err != nil || len(result.Rows) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if path == "preview-error" || path == "land-error" {
				if !result.Findings || result.Rows[0].Bucket != Failed {
					t.Fatal(result)
				}
			} else if result.Findings || result.Rows[0].Bucket != Skipped {
				t.Fatal(result)
			}
		})
	}
}
