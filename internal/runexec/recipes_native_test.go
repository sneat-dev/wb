package runexec

import (
	"context"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/recipe"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
func recipeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	readmeTemplate := filepath.Join(dir, "readme.tmpl")
	gatedTemplate := filepath.Join(dir, "gated.tmpl")
	brokenTemplate := filepath.Join(dir, "broken.tmpl")
	writeFixture(t, readmeTemplate, "<!-- cw-deps:v1 -->\ncw-deps hello\n<!-- /cw-deps -->\n")
	writeFixture(t, gatedTemplate, "<!-- cw-deps-gated:v1 -->\ncw-deps gated\n<!-- /cw-deps-gated -->\n")
	writeFixture(t, brokenTemplate, "<!-- cw-deps-broken:v1 -->\ncw-deps broken\n<!-- /cw-deps-broken -->\n")
	path := filepath.Join(dir, "wb.yaml")
	writeFixture(t, path, `
recipes:
  readme:
    type: template-section
    applies_if: always
    target: README.md
    template: `+readmeTemplate+`
    marker: cw-deps
  gated:
    type: template-section
    applies_if: has_file:go.mod
    target: README.md
    template: `+gatedTemplate+`
    marker: cw-deps-gated
  broken:
    type: template-section
    applies_if: nonsense
    target: README.md
    template: `+brokenTemplate+`
    marker: cw-deps-broken
`)
	return path
}

func recipeFixture(t *testing.T) (string, []discover.Repo) {
	t.Helper()
	root, seeds := t.TempDir(), t.TempDir()
	var repos []discover.Repo
	for _, name := range []string{"app", "localonly", "fork", "archived"} {
		path := filepath.Join(root, "acme", name)
		testenv.CloneWithOrigin(t, seeds, name, path)
		repos = append(repos, discover.Repo{Org: "acme", Name: name, Path: path, Remote: name != "localonly", IsFork: name == "fork", Archived: name == "archived"})
	}
	return root, append(repos, discover.Repo{Org: "acme", Name: "remoteonly", Remote: true})
}
func fixtureRecipes(repos []discover.Repo) RecipeOperations {
	return NewRecipes(func(string, string, []string) ([]discover.Repo, error) { return repos, nil })
}
func TestRecipesDryRunClassifiesEveryRepositoryBucket(t *testing.T) {
	t.Parallel()
	root, repos := recipeFixture(t)
	ops := fixtureRecipes(repos)
	var events []RecipeEvent
	result, err := ops.Run(context.Background(), RecipeRequest{ProjectsRoot: root, ExtraOrgs: []string{"acme"}, ConfigPath: recipeConfig(t), Name: "readme", Observe: func(e RecipeEvent) error { events = append(events, e); return nil }})
	if err != nil || !result.Findings || !result.Drift {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(events) != 6 || !events[0].DryRun {
		t.Fatalf("dry-run intent must precede five rows: %+v", events)
	}
	for bucket, want := range map[RecipeBucket]string{Updated: "acme/app — would", Skipped: "acme/localonly — local-only (not under your GitHub orgs)", Forked: "acme/fork", Archived: "acme/archived"} {
		found := false
		for _, row := range result.Rows {
			if row.Bucket == bucket && strings.Contains(row.Text, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %d %q: %+v", bucket, want, result.Rows)
		}
	}
	found := false
	for _, row := range result.Rows {
		if row.Bucket == Skipped && row.Text == "acme/remoteonly — remote-only (clone to evaluate)" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote-only skip absent")
	}
}
func TestRecipesGatedAndInvalidPredicates(t *testing.T) {
	t.Parallel()
	root, repos := recipeFixture(t)
	config := recipeConfig(t)
	for _, name := range []string{"gated", "broken"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := fixtureRecipes(repos).Run(context.Background(), RecipeRequest{ProjectsRoot: root, ConfigPath: config, Name: name, Observe: func(RecipeEvent) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			want := "recipe does not apply"
			bucket := Skipped
			if name == "broken" {
				want = "unknown applies_if"
				bucket = Failed
			}
			if result.Findings != (name == "broken") {
				t.Fatalf("findings=%v", result.Findings)
			}
			found := false
			for _, row := range result.Rows {
				if strings.Contains(row.Text, "acme/app") {
					found = true
					if row.Bucket != bucket || !strings.Contains(row.Text, want) {
						t.Fatalf("row=%+v", row)
					}
				}
			}
			if !found {
				t.Fatal("app outcome absent")
			}
		})
	}
}
func TestRecipesListUnknownAndUnreadableConfig(t *testing.T) {
	t.Parallel()
	ops := NewRecipes(func(string, string, []string) ([]discover.Repo, error) {
		t.Fatal("list/error must not discover")
		return nil, nil
	})
	config := recipeConfig(t)
	for _, list := range []bool{true, false} {
		result, err := ops.Run(context.Background(), RecipeRequest{ConfigPath: config, List: list})
		if err != nil || strings.Join(result.Names, ",") != "broken,gated,readme" {
			t.Fatalf("list=%+v %v", result, err)
		}
	}
	_, err := ops.Run(context.Background(), RecipeRequest{ConfigPath: config, Name: "absent"})
	if err == nil || !strings.Contains(err.Error(), `unknown recipe "absent"`) {
		t.Fatal(err)
	}
	_, err = ops.Run(context.Background(), RecipeRequest{ConfigPath: filepath.Join(t.TempDir(), "absent.yaml"), Name: "readme"})
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatal(err)
	}
}
func TestRecipesApplyPublishesAndReapplyReportsTheRepository(t *testing.T) {
	t.Parallel()
	root, seeds := t.TempDir(), t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	remote := testenv.CloneWithOrigin(t, seeds, "app", clone)
	ops := fixtureRecipes([]discover.Repo{{Org: "acme", Name: "app", Path: clone, Remote: true}})
	request := RecipeRequest{ProjectsRoot: root, ConfigPath: recipeConfig(t), Name: "readme", Apply: true, Observe: func(RecipeEvent) error { return nil }}
	result, err := ops.Run(context.Background(), request)
	if err != nil || result.Findings || len(result.Rows) != 1 || result.Rows[0].Bucket != Updated || !strings.Contains(result.Rows[0].Text, "acme/app") || !strings.Contains(result.Rows[0].Text, "pushed to main") {
		t.Fatalf("apply=%+v %v", result, err)
	}
	published := testenv.Git(t, root, "--git-dir", remote, "show", "main:README.md")
	if !strings.Contains(published, "cw-deps hello") {
		t.Fatalf("published=%q", published)
	}
	result, err = ops.Run(context.Background(), request)
	if err != nil || len(result.Rows) != 1 || !strings.Contains(result.Rows[0].Text, "acme/app") {
		t.Fatalf("reapply must report outcome/refusal: %+v %v", result, err)
	}
}
func TestApplyRecipeSurfacesRealFetchFailureWithoutOutcome(t *testing.T) {
	t.Parallel()
	cfg, err := recipe.LoadConfig(recipeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixtureRecipes(nil).apply(cfg.Recipes["readme"], discover.Repo{Org: "acme", Name: "app", Path: t.TempDir()})
	if err == nil || result.Changed || result.Detail != "" {
		t.Fatalf("failed apply=%+v %v", result, err)
	}
}
