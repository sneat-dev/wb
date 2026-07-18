package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trakhimenok/workbench/wb/internal/readme"
	"github.com/trakhimenok/workbench/wb/internal/scan"
)

func newAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Read-only drift report for the dev-approach README section (exits non-zero on drift)",
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runAudit(projectsRoot, filterFlag, extraOrgs)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

func runAudit(projectsRoot, filter string, extraOrgs []string) int {
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
			rep.record(&rep.skipped, "–", r.Slug()+" — remote-only (clone to evaluate)")
			continue
		}
		hasSrc, _ := scan.HasGoOrTS(r.Path)
		if !hasSrc {
			rep.record(&rep.skipped, "–", r.Slug()+" — no Go/TS source")
			continue
		}
		action, hasREADME, err := evaluate(r, tmpl)
		if err != nil {
			rep.record(&rep.errors, "✗", r.Slug()+" — "+err.Error())
			continue
		}
		if !hasREADME {
			rep.record(&rep.skipped, "–", r.Slug()+" — no README.md")
			continue
		}
		switch action {
		case readme.ActionNoop:
			rep.record(&rep.skipped, "–", r.Slug()+" — current")
		default:
			drift = true
			rep.record(&rep.updated, "✓", r.Slug()+" — would "+action.String())
		}
	}
	rep.print()
	if drift || len(rep.errors) > 0 {
		return 1
	}
	return 0
}
