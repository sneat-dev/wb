package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/trakhimenok/workbench/wb/internal/discover"
	"github.com/trakhimenok/workbench/wb/internal/gitops"
	"github.com/trakhimenok/workbench/wb/internal/readme"
	"github.com/trakhimenok/workbench/wb/internal/scan"
)

func newSyncReadmeCmd() *cobra.Command {
	var apply, localOnly bool
	cmd := &cobra.Command{
		Use:   "sync-readme",
		Short: "Ensure the canonical dev-approach section is present & current in each repo's README.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runSyncReadme(projectsRoot, filterFlag, extraOrgs, apply, localOnly)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "commit & push changes (default: dry-run report)")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "only process already-cloned repos; do not clone remote-only repos")
	return cmd
}

// evaluate reads origin/<default>:README.md and returns the action that would
// bring the dev-approach section current. hasREADME is false when the repo has
// no root README.md (a skip, not an error).
func evaluate(r discover.Repo, tmpl readme.Template) (action readme.Action, hasREADME bool, err error) {
	if err := gitops.Fetch(r.Path); err != nil {
		return readme.ActionNoop, false, err
	}
	def, err := gitops.DefaultBranch(r.Path)
	if err != nil {
		return readme.ActionNoop, false, err
	}
	content, ok, err := gitops.ShowFile(r.Path, "origin/"+def, "README.md")
	if err != nil {
		return readme.ActionNoop, false, err
	}
	if !ok {
		return readme.ActionNoop, false, nil
	}
	action, _ = readme.Plan(content, tmpl)
	return action, true, nil
}

func runSyncReadme(projectsRoot, filter string, extraOrgs []string, apply, localOnly bool) int {
	tmpl, err := readme.Canonical()
	if err != nil {
		fmt.Fprintln(os.Stderr, "template error:", err)
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
			if !apply || localOnly {
				rep.record(&rep.skipped, "–", r.Slug()+" — remote-only (skipped)")
				continue
			}
			dest := filepath.Join(projectsRoot, r.Org, r.Name)
			if err := gitops.Clone(r.CloneURL, dest); err != nil {
				rep.record(&rep.errors, "✗", r.Slug()+" — clone failed: "+err.Error())
				continue
			}
			r.Path = dest
		}
		hasSrc, _ := scan.HasGoOrTS(r.Path)
		if !hasSrc {
			rep.record(&rep.skipped, "–", r.Slug()+" — no Go/TS source")
			continue
		}
		if !apply {
			action, hasREADME, err := evaluate(r, tmpl)
			if err != nil {
				rep.record(&rep.errors, "✗", r.Slug()+" — "+err.Error())
			} else if !hasREADME {
				rep.record(&rep.skipped, "–", r.Slug()+" — no README.md")
			} else if action == readme.ActionNoop {
				rep.record(&rep.skipped, "–", r.Slug()+" — current")
			} else {
				rep.record(&rep.updated, "✓", r.Slug()+" — would "+action.String())
			}
			continue
		}
		if err := applyRepo(r, tmpl, &rep); err != nil {
			rep.record(&rep.errors, "✗", r.Slug()+" — "+err.Error())
		}
	}
	rep.print()
	if len(rep.errors) > 0 {
		return 1
	}
	return 0
}

func applyRepo(r discover.Repo, tmpl readme.Template, rep *report) error {
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
		CommitMessage: "docs: sync dev-approach section in README",
		PRBranch:      "chore/dev-approach",
		PRTitle:       "docs: sync dev-approach section in README",
		PRBody:        "Automated update of the **Our approach to development** section by `wb sync-readme`.",
	}
	outcome, err := gitops.Land(r.Path, opt, func(wt string) (bool, string, error) {
		path := filepath.Join(wt, "README.md")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return false, "no README.md", nil
		}
		updated, action := readme.Apply(string(raw), tmpl)
		if action == readme.ActionNoop {
			return false, "current", nil
		}
		if werr := os.WriteFile(path, []byte(updated), 0o644); werr != nil {
			return false, "", werr
		}
		return true, action.String(), nil
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
