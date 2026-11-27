package cmddisk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/diskprune"
	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// PruneDependencies are the operations wb disk prune composes.
type PruneDependencies struct {
	// World assembles the real machine for a projects root.
	World func(ctx context.Context, projectsRoot string, ignoreSumFiles []string) (*diskprune.World, error)
	// Run plans or applies.
	Run func(ctx context.Context, w *diskprune.World, o diskprune.Options) diskprune.Report
	// ConfigPath is the user-level wb.yaml.
	ConfigPath func() string
	// Ticker drives the heartbeat; nil uses a real ticker.
	Ticker func(time.Duration) (<-chan time.Time, func())
	// Interactive reports whether stderr is a terminal.
	Interactive func(nonInteractive bool) bool
	// WriteReceipt records an --apply or --auto run under the state
	// directory (disk-prune#req:daemon-auto-prune); nil records nothing.
	WriteReceipt func(stateDir string, receipt diskprune.Receipt) error
}

// DefaultPruneDependencies are the real operations.
func DefaultPruneDependencies() PruneDependencies {
	return PruneDependencies{
		World: diskprune.NewWorld, Run: diskprune.Run, ConfigPath: wbconfig.DefaultPath, WriteReceipt: diskprune.WriteReceipt,
		Ticker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		Interactive: func(nonInteractive bool) bool { return console.Interactive(os.Stderr, nonInteractive) },
	}
}

type pruneOptions struct {
	apply, auto, allowFullClean, verbose bool
	untilFree, olderThan, format         string
	categories                           []string
}

func newPruneCmd(runtime shared.Runtime, deps PruneDependencies) *cobra.Command {
	var options pruneOptions
	command := &cobra.Command{
		Use:   "prune",
		Short: "Plan, and with --apply reclaim, the regenerable bytes wb disk reports",
		Long: strings.TrimSpace(`
Give back the space wb disk reports, cheapest to rebuild first, stopping once
each affected filesystem meets its free-space target. Without --apply this
only plans: nothing is created, deleted, renamed or locked, and neither go nor
pnpm is run.

` + shared.CleanVersusPrune + `

Categories, in pruning order:

  scratch             temporary directories whose producer is known (go-build*,
                      orphaned test directories, WB's regenerable temp dirs,
                      Go caches named wb-*-cache) that nothing uses and that
                      hold no checkout, socket or cache data
  go-build-cache      Go build-cache entries older than the floor: the larger
                      of --older-than (default 24h) and the oldest running Go
                      build tool's age plus one hour
  node-package-store  pnpm store prune, run by pnpm itself
  go-module-cache     only with --category go-module-cache: versions no sum
                      file in the fleet names, older than 7 days, that the
                      proxy can provably serve again
  --allow-full-clean  every build-cache entry, only while no Go tool runs

Never pruned: worktrees (wb clean), WB state and the retired ~/.wb (reported
for a person to decide), and the npm cache (measured only). Prune never runs
go clean.

Exit codes: 0 nothing needs attention, 1 findings (a plan with work, a target
unmet, a failure or a fail-closed skip; see "outcome"), 2 usage.
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o, err := options.validate()
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			config, err := wbconfig.LoadDisk(deps.ConfigPath())
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			o.Floor = config.MinimumAvailable
			if options.untilFree == "" {
				o.Target = diskprune.Target{Kind: diskprune.TargetRatio, Ratio: config.MinimumAvailable}
			}
			flags := runtime.Flags()
			w, err := deps.World(cmd.Context(), flags.ProjectsRoot, config.IgnoreSumFiles)
			if err != nil {
				return err
			}
			progress := newPruneProgress(cmd.ErrOrStderr(), deps, flags, options.verbose)
			w.Progress = progress.event
			report := deps.Run(cmd.Context(), w, o)
			progress.finish(report)
			if (o.Apply || o.Auto) && deps.WriteReceipt != nil {
				trigger := "manual"
				if o.Auto {
					trigger = "manual-auto"
				}
				// Best-effort, after deletion: on a full disk the write may fail,
				// and that is reported, never fatal.
				if err := deps.WriteReceipt(w.StateDir, diskprune.ReceiptFor(trigger, time.Now(), w, report)); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "wb disk prune: receipt not written:", err)
				}
			}
			if err := writePruneReport(cmd.OutOrStdout(), options.format, options.verbose, report); err != nil {
				return err
			}
			if report.IsFinding() {
				return runtime.ExitError(shared.ExitFindings, "wb disk prune: outcome "+report.Outcome+"; see the report above")
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.BoolVar(&options.apply, "apply", false, "Delete what the plan lists; without it nothing changes")
	flags.StringVar(&options.untilFree, "until-free", "", "Free-space target per filesystem: 15%, 20GiB or max (default disk.minimum_available, 10%)")
	flags.StringSliceVar(&options.categories, "category", nil, "Restrict to a category (repeatable): scratch, go-build-cache, node-package-store, go-module-cache")
	flags.StringVar(&options.olderThan, "older-than", "", "Raise every age floor to at least this duration, such as 100h; never lowers one")
	flags.BoolVar(&options.allowFullClean, "allow-full-clean", false, "Also remove every Go build-cache entry while no Go tool runs; a build that starts meanwhile can fail once")
	flags.BoolVar(&options.auto, "auto", false, "The daemon's unattended subset: apply scratch, the build-cache trim and pnpm to the floor")
	flags.StringVar(&options.format, "format", "text", "Stdout format: text or json")
	flags.BoolVarP(&options.verbose, "verbose", "v", false, "Print each category as it runs, even when stderr is not a terminal")
	return command
}

// validate checks every flag before anything is read.
func (options pruneOptions) validate() (diskprune.Options, error) {
	o := diskprune.Options{Apply: options.apply, Auto: options.auto, AllowFullClean: options.allowFullClean}
	if options.format != "text" && options.format != "json" {
		return o, fmt.Errorf("unsupported --format %q: use text or json", options.format)
	}
	for _, category := range options.categories {
		switch {
		case slices.Contains(diskprune.SelectableCategories, category):
		case slices.Contains(diskprune.ReportOnlyCategories, category):
			return o, fmt.Errorf("--category %s is report-only: wb disk prune never deletes it", category)
		default:
			return o, fmt.Errorf("unknown --category %q: use %s", category, strings.Join(diskprune.SelectableCategories, ", "))
		}
	}
	o.Categories = options.categories
	if options.untilFree != "" {
		target, err := diskprune.ParseTarget(options.untilFree)
		if err != nil {
			return o, err
		}
		o.Target = target
	}
	if options.olderThan != "" {
		age, err := time.ParseDuration(options.olderThan)
		if err != nil || age <= 0 {
			return o, fmt.Errorf("--older-than %q: use a positive duration such as 100h", options.olderThan)
		}
		o.OlderThan = age
	}
	if options.auto && (len(options.categories) > 0 || options.untilFree != "" || options.olderThan != "" || options.allowFullClean) {
		return o, fmt.Errorf("--auto runs a fixed subset to the floor and takes no --category, --until-free, --older-than or --allow-full-clean")
	}
	if options.allowFullClean && len(options.categories) > 0 && !slices.Contains(options.categories, diskprune.CategoryBuild) {
		return o, fmt.Errorf("--allow-full-clean needs the go-build-cache category selected")
	}
	return o, nil
}

// pruneProgress is prune's stderr liveness: a line per phase change on a
// terminal or with --verbose, otherwise a heartbeat every 10 s that is
// silent for a run that finishes before the first tick.
type pruneProgress struct {
	out      io.Writer
	perLine  bool
	enabled  bool
	mu       sync.Mutex
	started  time.Time
	phase    string
	category string
	unlinked int64
	beats    int
	stop     func()
	done     chan struct{}
}

const pruneHeartbeat = 10 * time.Second

func newPruneProgress(out io.Writer, deps PruneDependencies, flags shared.Flags, verbose bool) *pruneProgress {
	p := &pruneProgress{out: out, started: time.Now(), done: make(chan struct{})}
	if flags.Quiet {
		return p
	}
	p.enabled = true
	p.perLine = verbose || deps.Interactive(flags.NonInteractive)
	if !p.perLine {
		ticks, stop := deps.Ticker(pruneHeartbeat)
		p.stop = stop
		// The goroutine keeps its own copy of the channel: finish clears the
		// field under the lock, and reading the field here would race it.
		done := p.done
		go func() {
			for {
				select {
				case <-done:
					return
				case <-ticks:
					p.beat()
				}
			}
		}()
	}
	return p
}

func (p *pruneProgress) event(event diskprune.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := event.Phase != p.phase || event.Category != p.category
	p.phase, p.category, p.unlinked = event.Phase, event.Category, event.Unlinked
	if p.enabled && p.perLine && changed {
		_, _ = fmt.Fprintf(p.out, "wb disk prune: %s\n", p.statusLocked())
	}
}

func (p *pruneProgress) statusLocked() string {
	status := p.phase
	if p.category != "" {
		status += " " + p.category
	}
	return fmt.Sprintf("%s, %s, %s unlinked so far", status, time.Since(p.started).Round(time.Second), diskusage.Human(p.unlinked))
}

func (p *pruneProgress) beat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done == nil {
		return
	}
	p.beats++
	line := "wb disk prune: " + p.statusLocked()
	if p.beats == 1 {
		line += fmt.Sprintf(" (heartbeat every %s)", pruneHeartbeat)
	}
	_, _ = fmt.Fprintln(p.out, line)
}

func (p *pruneProgress) finish(report diskprune.Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		p.stop()
	}
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
	if p.beats > 0 {
		_, _ = fmt.Fprintf(p.out, "wb disk prune: done; outcome %s in %s\n", report.Outcome, time.Since(p.started).Round(time.Second))
	}
}

func writePruneReport(out io.Writer, format string, verbose bool, report diskprune.Report) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	_, err := io.WriteString(out, renderPrune(report, verbose))
	return err
}

// renderPrune is the human plan or result: filesystems first, then each
// category with what it would delete or deleted, and what it kept and why.
func renderPrune(report diskprune.Report, verbose bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "wb disk prune (%s): outcome %s", report.Mode, report.Outcome)
	if len(report.Reasons) > 1 {
		fmt.Fprintf(&b, " (%s)", strings.Join(report.Reasons, ", "))
	}
	b.WriteString("\n")
	if report.Mode != "plan" {
		fmt.Fprintf(&b, "admission: %s\n", report.Admission.Status)
	}
	for _, fs := range report.Filesystems {
		fmt.Fprintf(&b, "\nfilesystem %s: %s free of %s", fs.Path, diskusage.Human(fs.Before.AvailableBytes), diskusage.Human(fs.Before.TotalBytes))
		if report.Mode != "plan" {
			fmt.Fprintf(&b, " -> %s free (unlinked %s, observed %s)", diskusage.Human(fs.After.AvailableBytes), diskusage.Human(fs.UnlinkedBytes), signedHuman(fs.ObservedGainBytes))
		} else {
			fmt.Fprintf(&b, ", plan estimates %s", diskusage.Human(fs.EstimatedBytes))
		}
		if fs.Note != "" {
			fmt.Fprintf(&b, " [%s]", fs.Note)
		}
		b.WriteString("\n")
	}
	for _, category := range report.Categories {
		fmt.Fprintf(&b, "\n%d. %s: %s", category.Order, category.Name, category.Status)
		if category.Reason != "" {
			fmt.Fprintf(&b, " (%s)", category.Reason)
		}
		if category.EffectiveFloor != "" {
			fmt.Fprintf(&b, ", floor %s", category.EffectiveFloor)
		}
		if category.EstimateBytes != nil {
			fmt.Fprintf(&b, ", estimate %s", diskusage.Human(*category.EstimateBytes))
		}
		b.WriteString("\n")
		for _, pid := range category.PIDs {
			fmt.Fprintf(&b, "     pid %d: %s\n", pid.PID, pid.Command)
		}
		for _, shard := range category.Shards {
			fmt.Fprintf(&b, "   %-13s %s  %d entries, %s\n", shard.Status, shard.Path, shard.Entries, diskusage.Human(shard.Bytes))
		}
		kept := map[string]int{}
		var keptOrder []string
		for _, it := range category.Items {
			if it.Status == diskprune.StatusKeep && !verbose {
				code := it.Reason
				if i := strings.IndexAny(code, ":"); i >= 0 {
					code = code[:i]
				}
				if kept[code] == 0 {
					keptOrder = append(keptOrder, code)
				}
				kept[code]++
				continue
			}
			fmt.Fprintf(&b, "   %-13s %s", it.Status, it.Path)
			if it.Bytes > 0 {
				fmt.Fprintf(&b, "  %s", diskusage.Human(it.Bytes))
			}
			if it.Reason != "" {
				fmt.Fprintf(&b, "  %s", it.Reason)
			}
			if it.Error != "" {
				fmt.Fprintf(&b, "  error: %s", it.Error)
			}
			b.WriteString("\n")
		}
		for _, code := range keptOrder {
			fmt.Fprintf(&b, "   kept %d: %s (--verbose lists them)\n", kept[code], code)
		}
	}
	for _, state := range report.State {
		fmt.Fprintf(&b, "\nreport-only %s (never pruned):\n", state.Root)
		for _, entry := range state.Entries {
			fmt.Fprintf(&b, "   %-17s %s  %s\n", entry.Class, entry.Path, diskusage.Human(entry.Bytes))
		}
	}
	return b.String()
}

// signedHuman shows a free-space change with its sign.
func signedHuman(bytes int64) string {
	if bytes < 0 {
		return "-" + diskusage.Human(-bytes)
	}
	return "+" + diskusage.Human(bytes)
}
