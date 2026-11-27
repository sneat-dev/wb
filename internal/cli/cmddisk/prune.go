package cmddisk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/diskprune"
	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// PruneDependencies are wb disk prune's seams.
type PruneDependencies struct {
	Env func() diskprune.Env
	Run func(context.Context, diskprune.Env, diskprune.Options) diskprune.Report
	// ConfigPath is the user-level wb.yaml, whose disk.minimum_available
	// wb disk reports against.
	ConfigPath func() string
}

// DefaultPruneDependencies wires the real machine.
func DefaultPruneDependencies() PruneDependencies {
	return PruneDependencies{Env: diskprune.NewEnv, Run: diskprune.Run, ConfigPath: wbconfig.DefaultPath}
}

type pruneOptions struct {
	apply      bool
	olderThan  string
	format     string
	categories []string
}

func newPruneCmd(runtime shared.Runtime, deps PruneDependencies) *cobra.Command {
	options := pruneOptions{format: "text"}
	command := &cobra.Command{
		Use:   "prune",
		Short: "Plan, and with --apply remove, your old scratch and the idle Go build cache",
		Long: strings.TrimSpace(`
Give back the space that is safe to rebuild. Without --apply this only
plans: nothing is changed.

` + shared.CleanVersusPrune + `

Categories:

  scratch         your own go-build*, Test*/Benchmark*/Fuzz* and WB scratch
                  directories directly in TMPDIR (and GOTMPDIR) in which
                  nothing changed for 3 days (--older-than raises that). A
                  directory that is not yours, is a symlink, is on another
                  filesystem, or holds a mount, a .git or a socket is kept.
  go-build-cache  go clean -cache, only while no Go process runs (Linux,
                  where /proc tells); Go trims the cache itself day to day.

Never pruned: worktrees (wb clean), WB state, the Go module cache, and the
npm and pnpm stores (measured by wb disk only).

Exit codes: 0 nothing needs attention, 1 findings (a plan with work, a step
skipped because Go was running, or a failure; see "outcome"), 2 usage.
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o, err := options.validate(cmd)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			report := deps.Run(cmd.Context(), deps.Env(), o)
			if err := writePrune(cmd.OutOrStdout(), options.format, report); err != nil {
				return err
			}
			if report.Outcome != diskprune.OutcomeDone {
				return runtime.ExitError(shared.ExitFindings, "wb disk prune: outcome "+report.Outcome)
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.BoolVar(&options.apply, "apply", false, "Remove what the plan lists")
	flags.StringArrayVar(&options.categories, "category", nil, "Restrict to a category (repeatable): scratch, go-build-cache")
	flags.StringVar(&options.olderThan, "older-than", "", "Scratch must be unchanged at least this long, such as 100h or 5d; never below 3 days")
	flags.StringVar(&options.format, "format", "text", "Output format: text or json")
	return command
}

// validate checks every flag before anything is read. An empty value given
// explicitly (--category= from an unset variable) is refused, never read as
// "everything".
func (options pruneOptions) validate(cmd *cobra.Command) (diskprune.Options, error) {
	o := diskprune.Options{Apply: options.apply}
	if options.format != "text" && options.format != "json" {
		return o, fmt.Errorf("unsupported --format %q: use text or json", options.format)
	}
	for _, occurrence := range options.categories {
		for _, category := range strings.Split(occurrence, ",") {
			category = strings.TrimSpace(category)
			if !slices.Contains(diskprune.Categories, category) {
				return o, fmt.Errorf("unknown --category %q: use %s", category, strings.Join(diskprune.Categories, ", "))
			}
			o.Categories = append(o.Categories, category)
		}
	}
	if cmd.Flags().Changed("older-than") {
		age, err := parseAge(options.olderThan)
		if err != nil || age < diskprune.DefaultScratchAge {
			return o, fmt.Errorf("--older-than %q: use a duration of at least 72h, such as 100h or 5d", options.olderThan)
		}
		o.ScratchAge = age
	}
	return o, nil
}

// parseAge is a Go duration, or a whole number of days such as 5d.
func parseAge(raw string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(strings.TrimSpace(raw), "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 || n > 36500 {
			return 0, fmt.Errorf("invalid number of days %q", raw)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func writePrune(out io.Writer, format string, report diskprune.Report) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	_, err := io.WriteString(out, renderPrune(report))
	return err
}

func renderPrune(report diskprune.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "wb disk prune (%s): outcome %s\n", report.Mode, report.Outcome)
	for _, category := range report.Categories {
		fmt.Fprintf(&b, "\n%s: %s", category.Name, category.Status)
		if category.Bytes > 0 {
			fmt.Fprintf(&b, ", %s", diskusage.Human(category.Bytes))
		}
		if category.Reason != "" {
			fmt.Fprintf(&b, " (%s)", category.Reason)
		}
		b.WriteString("\n")
		for _, item := range category.Items {
			fmt.Fprintf(&b, "  %-8s %s  %s", item.Status, item.Path, diskusage.Human(item.Bytes))
			if item.Error != "" {
				fmt.Fprintf(&b, "  error: %s", item.Error)
			}
			b.WriteString("\n")
		}
		reasons := make([]string, 0, len(category.Kept))
		for reason := range category.Kept {
			reasons = append(reasons, reason)
		}
		slices.Sort(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(&b, "  kept %d: %s\n", category.Kept[reason], reason)
		}
	}
	if report.Mode == "plan" && report.Outcome == diskprune.OutcomePlanned {
		b.WriteString("\nRun again with --apply to remove what is listed.\n")
	}
	return b.String()
}
