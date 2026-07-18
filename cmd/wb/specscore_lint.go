package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trakhimenok/workbench/wb/internal/discover"
	"github.com/trakhimenok/workbench/wb/internal/gitops"
	"github.com/trakhimenok/workbench/wb/internal/specscore"
)

func newSpecscoreLintCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "specscore-lint",
		Short: "Lint (and optionally --fix) every SpecScore-managed repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runSpecscoreLint(projectsRoot, filterFlag, extraOrgs, fix)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "run `specscore spec lint --fix` and commit & push (default: dry-run report)")
	return cmd
}

func runSpecscoreLint(projectsRoot, filter string, extraOrgs []string, fix bool) int {
	repos, err := fleet(projectsRoot, filter, func() []string { return fleetOwners(extraOrgs) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		return 1
	}
	if !fix {
		fmt.Println("(dry-run — pass --fix to apply & push)")
	}
	var rep report
	drift := false
	for _, r := range repos {
		if r.Archived {
			rep.record(&rep.archived, "▪", r.Slug())
			continue
		}
		if !r.Remote {
			rep.record(&rep.skipped, "–", r.Slug()+" — local-only (not under your GitHub orgs)")
			continue
		}
		if r.IsFork {
			rep.record(&rep.forked, "⑂", r.Slug())
			continue
		}
		if r.Path == "" {
			rep.record(&rep.skipped, "–", r.Slug()+" — remote-only (clone to lint)")
			continue
		}
		if !specscore.IsManaged(r.Path) {
			rep.record(&rep.skipped, "–", r.Slug()+" — not SpecScore-managed (no specscore.yaml)")
			continue
		}
		if !fix {
			n, _, lerr := specscore.Lint(r.Path)
			switch {
			case lerr != nil:
				rep.record(&rep.errors, "✗", r.Slug()+" — "+lerr.Error())
			case n == 0:
				rep.record(&rep.skipped, "–", r.Slug()+" — clean")
			default:
				drift = true
				rep.record(&rep.updated, "✓", fmt.Sprintf("%s — %d violation(s); would run --fix", r.Slug(), n))
			}
			continue
		}
		if err := applySpecscoreLint(r, &rep); err != nil {
			rep.record(&rep.errors, "✗", r.Slug()+" — "+err.Error())
		}
	}
	rep.print()
	if len(rep.errors) > 0 || (!fix && drift) {
		return 1
	}
	return 0
}

func applySpecscoreLint(r discover.Repo, rep *report) error {
	def, err := gitops.DefaultBranch(r.Path)
	if err != nil {
		if ferr := gitops.Fetch(r.Path); ferr != nil {
			return ferr
		}
		if def, err = gitops.DefaultBranch(r.Path); err != nil {
			return err
		}
	}
	opt := gitops.LandOptions{
		DefaultBranch: def,
		CommitMessage: "chore(spec): apply specscore lint --fix (status-vocabulary migration)",
		PRBranch:      "chore/specscore-lint-fix",
		PRTitle:       "chore(spec): apply specscore lint --fix",
		PRBody:        "Automated `specscore spec lint --fix` run by `wb specscore-lint --fix` (canonical status-vocabulary migration).",
	}
	outcome, err := gitops.Land(r.Path, opt, func(wt string) (bool, string, error) {
		if _, ferr := specscore.Fix(wt); ferr != nil {
			return false, "", ferr
		}
		changed, cerr := gitops.WorktreeChanged(wt)
		if cerr != nil {
			return false, "", cerr
		}
		if !changed {
			return false, "clean", nil
		}
		return true, "applied specscore lint --fix", nil
	})
	if err != nil {
		return err
	}
	if !outcome.Changed {
		rep.record(&rep.skipped, "–", r.Slug()+" — "+outcome.Detail)
		return nil
	}
	rep.record(&rep.updated, "✓", r.Slug()+" — "+outcome.Detail)
	return nil
}
