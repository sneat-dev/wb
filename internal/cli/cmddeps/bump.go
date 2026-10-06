package cmddeps

import (
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/spf13/cobra"
)

func newBump(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	options := depsSetOptions{}
	var changed []string
	command := &cobra.Command{
		Use:   "bump <ecosystem>",
		Short: "Propagate published dependency versions through recalculated waves",
		Long: `Propagate published dependency versions through recalculated consumer waves.

Seeding the campaign:

  --changed <module@version>  The published release event, typed exactly.

  --latest --scope <glob>     WB reads the modules the selected repositories
                              declare, keeps the ones a --scope glob matches,
                              and asks the registry for each one's published
                              latest version — producing the same --changed
                              list without typing it. Every matched module is
                              listed in the report, including the ones that
                              published nothing, so a scope's coverage is
                              auditable rather than assumed. The two compose:
                              the newest version observed for a dependency
                              wins, so a release still in flight can be named
                              with --changed alongside a --latest sweep.

Two flags shape which repositories the campaign touches, and they mean
different things:

  --exclude <org/repo glob>   The repository is removed from the campaign
                              entirely, before anything is discovered: no graph
                              entry, no wave membership, no worktree, no pull
                              request. Use it for an archived or irrelevant
                              repository. Excluded slugs are listed in the
                              report, so "needed nothing" is never confused
                              with "never looked at".

  --hold <org/repo glob>      The repository IS bumped, verified, pushed, and
                              has its pull request opened and its exact PR-head
                              GitHub checks waited on — and is then left OPEN,
                              even under --merge. Use it for a repository whose
                              merge is a human decision, such as a gated deploy
                              repository. Because a release that needs a human
                              merge cannot be waited for, a wave containing a
                              held repository stops the campaign with status
                              awaiting_hold_release and names the pull requests
                              the remaining waves are waiting on.

Both accept path.Match globs, where "*" never crosses a "/", and an exact
"owner/name" always matches itself. --scope uses the same glob semantics
against a module path or package name, exactly as "wb deps drift --scope"
does: "@sneat/*" matches "@sneat/core", and "github.com/dal-go/*" matches
"github.com/dal-go/dalgo" but not a nested "github.com/dal-go/dalgo/x".`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			options := options
			flags := runtime.Flags()
			ecosystem := deps.Ecosystem(args[0])
			switch ecosystem {
			case deps.EcosystemGo, deps.EcosystemNPM:
			default:
				return fmt.Errorf("dependency waves currently support only the go and npm ecosystems")
			}
			if !options.fleet {
				return fmt.Errorf("deps bump requires --fleet")
			}
			validationMode, checks, err := dependencyValidationOptions(command, options)
			if err != nil {
				return err
			}
			options.validation = string(validationMode)
			options.parallelExplicit = depsBumpParallelExplicit(command)
			// Both halves of the derivation are checked before a single
			// repository is discovered: a fleet-wide registry sweep with no
			// selection is nobody's intended default, and a scope that
			// silently does nothing is worse than a refusal.
			if len(options.scopes) > 0 && !options.latest {
				return fmt.Errorf("--scope selects which published modules --latest derives release events from; pass --latest, or name the events with --changed")
			}
			if options.latest && len(deps.NormalizeScopes(options.scopes)) == 0 {
				return fmt.Errorf("--latest derives release events for the modules --scope selects; pass at least one --scope glob, e.g. --scope '@sneat/*'")
			}
			campaign := operations.Campaign(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), flags.NonInteractive), "deps bump")
			options.campaign = campaign
			// Repository selection comes first now: --latest derives its seed
			// events from the modules the selected repositories actually
			// declare, so there is nothing to derive from until the selection
			// exists.
			repositories, err := dependencyRepositories(command, flags, []string{args[0], "events"}, options, operations)
			if err != nil {
				campaign.Finish("failed")
				return err
			}
			lifecycle := dependencyOptions(flags, options, checks)
			events, err := depsBumpSeedEvents(command, ecosystem, changed, repositories, &options, lifecycle, operations)
			if err != nil {
				campaign.Finish("failed")
				return err
			}
			return runDepsBump(flags, command, ecosystem, events, repositories, options, lifecycle, operations)
		},
	}
	command.Flags().StringArrayVar(&changed, "changed", nil, "published module@version release event (repeatable)")
	command.Flags().BoolVar(&options.latest, "latest", false, "derive the seed release events from the registry's published latest version of every module matching --scope")
	command.Flags().StringArrayVar(&options.scopes, "scope", nil, "with --latest, glob matched against a published module path or package name, e.g. @sneat/* (repeatable)")
	command.Flags().StringArrayVar(&options.exclude, "exclude", nil, "org/repo glob removed from the campaign entirely: no graph entry, no wave, no worktree, no PR (repeatable)")
	command.Flags().StringArrayVar(&options.hold, "hold", nil, "org/repo glob whose PR is opened and CI-waited but never merged, even under --merge; downstream waves stop and name the held PRs (repeatable)")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and process selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref for operation worktrees")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories or release observations to process concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "interval between provider release observations")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck release events older than this before starting a downstream build (0 disables)")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "inspect the first wave without creating worktrees or changing dependency files")
	command.Flags().BoolVar(&options.fetchCache, "fetch-cache", false, "memoize DISCOVERY fetches for up to 15m within this run for repositories the campaign never pushed to, opened a PR for, or merged (opt-in; wave mutation bases always re-fetch; nothing persists across invocations; avoid when others may land on main mid-campaign)")
	command.Flags().BoolVar(&options.resume, "resume", false, "reuse existing wave worktrees, branches, PRs, and report state")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit a release event lower than an observed semantic version")
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().StringVar(&options.validation, "validation", string(deps.ValidationModeFull), "validation mode: full, or fast with mandatory exact PR-head CI before merge or PR validation")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "legacy explicit escape hatch that skips local verification (distinct from --validation=fast)")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check, CI wait, or release wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit dependency changes on wave branches")
	command.Flags().BoolVar(&options.push, "push", false, "push wave branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "merge passing PRs and observe releases; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-bump.md and deps-bump.yaml to this directory")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}
