// Command all-repos runs fleet-wide operations across the user's repositories.
//
// Subcommands:
//
//	sync-readme   ensure the canonical "dev-approach" section is present and
//	              current in each repo's root README.md. Read-only by default;
//	              pass --apply to commit and push (PR fallback on protected
//	              branches).
//	audit         read-only report of dev-approach drift across the fleet;
//	              exits non-zero when any repo is missing or outdated.
//
// Only repos containing Go or TypeScript source are considered.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/trakhimenok/workbench/wb/internal/discover"
	"github.com/trakhimenok/workbench/wb/internal/gitops"
	"github.com/trakhimenok/workbench/wb/internal/readme"
	"github.com/trakhimenok/workbench/wb/internal/scan"
	"github.com/trakhimenok/workbench/wb/internal/specscore"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var (
		apply        bool
		localOnly    bool
		fix          bool
		filter       string
		extraOrgs    stringList
		projectsRoot string
	)
	home, _ := os.UserHomeDir()
	fs.StringVar(&projectsRoot, "projects-root", filepath.Join(home, "projects"), "root dir containing {org}/{repo}")
	fs.StringVar(&filter, "filter", "", "only repos whose org/name contains this substring")
	fs.Var(&extraOrgs, "org", "additional GitHub owner to query (repeatable)")
	if cmd == "sync-readme" {
		fs.BoolVar(&apply, "apply", false, "commit & push changes (default: dry-run report)")
		fs.BoolVar(&localOnly, "local-only", false, "only process already-cloned repos; do not clone remote-only repos")
	}
	if cmd == "specscore-lint" {
		fs.BoolVar(&fix, "fix", false, "apply `specscore spec lint --fix` and commit & push (default: dry-run report)")
	}
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "sync-readme":
		os.Exit(runSyncReadme(projectsRoot, filter, extraOrgs, apply, localOnly))
	case "audit":
		os.Exit(runAudit(projectsRoot, filter, extraOrgs))
	case "specscore-lint":
		os.Exit(runSpecscoreLint(projectsRoot, filter, extraOrgs, fix))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: all-repos <sync-readme|audit|specscore-lint> [--filter S] [--org O] [--projects-root P] [--apply] [--local-only] [--fix]")
}

// fleet discovers and reconciles repos, filtered and with archived dropped.
func fleet(projectsRoot, filter string, extraOrgs []string) ([]discover.Repo, error) {
	var (
		wg          sync.WaitGroup
		local       []discover.Repo
		localErr    error
		user        string
		remoteByOrg = map[string][]discover.Repo{}
		mu          sync.Mutex
	)

	var memberOrgs []string
	wg.Add(1)
	go func() {
		defer wg.Done()
		local, localErr = discover.ScanLocal(projectsRoot)
	}()
	if u, err := discover.AuthUser(); err == nil {
		user = u
	}
	if orgs, err := discover.MemberOrgs(); err == nil {
		memberOrgs = orgs
	}
	wg.Wait()
	if localErr != nil {
		return nil, localErr
	}

	// Owners are the user and the orgs they belong to — NOT local directory
	// names, which include third-party clones we must never touch.
	owners := map[string]bool{}
	if user != "" {
		owners[user] = true
	}
	for _, o := range memberOrgs {
		owners[o] = true
	}
	for _, o := range extraOrgs {
		owners[o] = true
	}

	var rwg sync.WaitGroup
	for owner := range owners {
		rwg.Add(1)
		go func(owner string) {
			defer rwg.Done()
			repos, err := discover.ListRemote(owner)
			if err != nil {
				return // owner may not be accessible; local data still used
			}
			mu.Lock()
			remoteByOrg[owner] = repos
			mu.Unlock()
		}(owner)
	}
	rwg.Wait()

	var remote []discover.Repo
	for _, repos := range remoteByOrg {
		remote = append(remote, repos...)
	}

	all := discover.Reconcile(local, remote)
	var out []discover.Repo
	for _, r := range all {
		if filter != "" && !strings.Contains(r.Slug(), filter) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// report accumulates per-repo outcomes for the summary.
type report struct {
	updated, skipped, errors, archived, forked []string
}

// print writes the final summary. Per-repo lines already streamed during the
// run, so this shows counts and re-lists only errors so failures aren't lost in
// scroll.
func (rep *report) print() {
	fmt.Printf("\n━━━ Summary ━━━\n")
	fmt.Printf("Updated  %d\n", len(rep.updated))
	fmt.Printf("Skipped  %d\n", len(rep.skipped))
	fmt.Printf("Forks    %d\n", len(rep.forked))
	fmt.Printf("Archived %d\n", len(rep.archived))
	fmt.Printf("Errors   %d\n", len(rep.errors))
	sort.Strings(rep.errors)
	for _, e := range rep.errors {
		fmt.Printf("  ✗ %s\n", e)
	}
}

// record files an outcome into a bucket and streams a line to stdout
// immediately, so progress is visible as each repo completes.
func (rep *report) record(bucket *[]string, symbol, entry string) {
	*bucket = append(*bucket, entry)
	fmt.Printf("%s %s\n", symbol, entry)
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

func runAudit(projectsRoot, filter string, extraOrgs []string) int {
	tmpl, err := readme.Canonical()
	if err != nil {
		fmt.Fprintln(os.Stderr, "template error:", err)
		return 1
	}
	repos, err := fleet(projectsRoot, filter, extraOrgs)
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

func runSyncReadme(projectsRoot, filter string, extraOrgs []string, apply, localOnly bool) int {
	tmpl, err := readme.Canonical()
	if err != nil {
		fmt.Fprintln(os.Stderr, "template error:", err)
		return 1
	}
	repos, err := fleet(projectsRoot, filter, extraOrgs)
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
		// Clone missing non-archived repos when applying (unless --local-only).
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

// runSpecscoreLint lints every SpecScore-managed repo (one carrying a
// specscore.yaml) the user owns. Dry-run by default — it reports each repo's
// violation count; with --fix it applies `specscore spec lint --fix` in a
// detached worktree and lands the result (direct push or auto-merge PR).
func runSpecscoreLint(projectsRoot, filter string, extraOrgs []string, fix bool) int {
	repos, err := fleet(projectsRoot, filter, extraOrgs)
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
		PRBody:        "Automated `specscore spec lint --fix` run by `all-repos specscore-lint --fix` (canonical status-vocabulary migration).",
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

func applyRepo(r discover.Repo, tmpl readme.Template, rep *report) error {
	def, err := gitops.DefaultBranch(r.Path)
	if err != nil {
		// Fetch first so origin/HEAD is known.
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
		PRBody:        "Automated update of the **Our approach to development** section by `all-repos sync-readme`.",
	}
	outcome, err := gitops.Land(r.Path, opt, func(wt string) (bool, string, error) {
		path := filepath.Join(wt, "README.md")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			// Missing README is a skip, not an error — we don't fabricate one.
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
