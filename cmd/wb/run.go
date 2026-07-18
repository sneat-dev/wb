package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/recipe"
)

func newRunCmd() *cobra.Command {
	var (
		apply      bool
		configPath string
		list       bool
	)
	cmd := &cobra.Command{
		Use:   "run [recipe]",
		Short: "Run a fleet-wide recipe defined in config (dry-run by default; --apply lands it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			code := runRun(projectsRoot, filterFlag, extraOrgs, configPath, name, list, apply)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "commit & push changes (default: dry-run report)")
	cmd.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	cmd.Flags().BoolVar(&list, "list", false, "list configured recipes and exit")
	return cmd
}

// defaultConfigPath returns the default recipe-config location,
// ~/.config/wb/wb.yaml.
func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "wb", "wb.yaml")
}

func runRun(projectsRoot, filter string, extraOrgs []string, configPath, name string, list, apply bool) int {
	if configPath == "" {
		configPath = defaultConfigPath()
	}
	cfg, err := recipe.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if list || name == "" {
		names := make([]string, 0, len(cfg.Recipes))
		for n := range cfg.Recipes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
		return 0
	}

	r, ok := cfg.Recipes[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown recipe %q (see `wb run --list`)\n", name)
		return 1
	}

	repos, err := fleet(projectsRoot, filter, func() []string { return fleetOwners(extraOrgs) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		return 1
	}
	if !apply {
		fmt.Println("(dry-run — pass --apply to commit & push)")
	}

	var rep report
	drift := false
	for _, repoItem := range repos {
		if repoItem.Archived {
			rep.record(&rep.archived, "▪", repoItem.Slug())
			continue
		}
		if !repoItem.Remote {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — local-only (not under your GitHub orgs)")
			continue
		}
		if repoItem.IsFork {
			rep.record(&rep.forked, "⑂", repoItem.Slug())
			continue
		}
		if repoItem.Path == "" {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — remote-only (clone to evaluate)")
			continue
		}
		applies, err := r.AppliesTo(repoItem.Path)
		if err != nil {
			rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
			continue
		}
		if !applies {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — recipe does not apply")
			continue
		}
		if !apply {
			preview, err := recipe.Evaluate(r, repoItem.Path)
			switch {
			case err != nil:
				rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
			case !preview.Changed:
				rep.record(&rep.skipped, "–", repoItem.Slug()+" — "+preview.Summary)
			default:
				drift = true
				rep.record(&rep.updated, "✓", repoItem.Slug()+" — would "+preview.Summary)
			}
			continue
		}
		if err := applyRecipe(r, repoItem, &rep); err != nil {
			rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
		}
	}
	rep.print()
	if len(rep.errors) > 0 || (!apply && drift) {
		return 1
	}
	return 0
}

func applyRecipe(r recipe.Recipe, repoItem discover.Repo, rep *report) error {
	def, err := gitops.DefaultBranch(repoItem.Path)
	if err != nil {
		if ferr := gitops.Fetch(repoItem.Path); ferr != nil {
			return ferr
		}
		if def, err = gitops.DefaultBranch(repoItem.Path); err != nil {
			return err
		}
	}
	outcome, err := recipe.Land(r, repoItem.Path, def)
	if err != nil {
		return err
	}
	if !outcome.Changed {
		rep.record(&rep.skipped, "–", repoItem.Slug()+" — "+outcome.Detail)
		return nil
	}
	rep.record(&rep.updated, "✓", repoItem.Slug()+" — "+outcome.Detail)
	return nil
}
