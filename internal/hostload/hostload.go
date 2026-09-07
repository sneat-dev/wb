// Package hostload refuses to admit more CPU-heavy WB work when the host is
// already saturated. Two orphaned fixture processes ran at 100% CPU for
// nearly seven days (2026-08-31, PIDs 43481/43483), driving load average to
// 7-12 on a shared machine; WB's merge-gate shards then failed unrelated
// candidates because everything on the box was starved for CPU. This
// package gives `wb run` and `wb worktree merge`/`prepare`/`resume` a floor
// below which they refuse new CPU-heavy work instead of piling onto an
// already-overloaded host.
package hostload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/wbconfig"
)

// Reader returns the current 1-minute load average for this host.
type Reader func() (float64, error)

// System is the production Reader. It is platform-specific: see
// loadavg_darwin.go, loadavg_linux.go, and loadavg_other.go.
var System Reader = readLoadAvg1

// ErrUnsupported is returned by a platform Reader that has no load-average
// source. Callers must treat it as "never refuse", not as a failure.
var ErrUnsupported = errors.New("hostload: 1-minute load average is not available on this platform")

// config is the subset of wb.yaml this package understands.
type config struct {
	Admission struct {
		LoadFloor *float64 `yaml:"load_floor"`
	} `yaml:"admission"`
}

// Floor resolves the load-average ceiling above which new CPU-heavy work is
// refused. Default is runtime.NumCPU() (never less than 1); wb.yaml's
// admission.load_floor key overrides it. configPath "" resolves
// wbconfig.DefaultPath(). A missing or unparsable config is not an error
// here — it just keeps the default.
func Floor(configPath string) float64 {
	def := float64(runtime.NumCPU())
	if def < 1 {
		def = 1
	}
	path := strings.TrimSpace(configPath)
	if path == "" {
		path = wbconfig.DefaultPath()
	}
	raw, err := os.ReadFile(expandPath(path))
	if err != nil {
		return def
	}
	var cfg config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return def
	}
	if cfg.Admission.LoadFloor != nil && *cfg.Admission.LoadFloor > 0 {
		return *cfg.Admission.LoadFloor
	}
	return def
}

// Check refuses admission when the host's 1-minute load average exceeds
// floor, unless allow is true (the caller passed --allow-saturated-host).
// read is normally nil, which selects System; tests inject a fake Reader.
// A Reader error (including ErrUnsupported) never blocks admission — an
// unreadable or unsupported load source fails open.
func Check(read Reader, floor float64, allow bool) error {
	if allow {
		return nil
	}
	if read == nil {
		read = System
	}
	if read == nil {
		return nil
	}
	load, err := read()
	if err != nil {
		return nil
	}
	if load <= floor {
		return nil
	}
	consumers := Consumers(5)
	message := fmt.Sprintf(
		"host load average %.2f exceeds the admission floor %.2f; refusing to admit more CPU-heavy work",
		load, floor)
	if len(consumers) > 0 {
		message += ":\n" + strings.Join(consumers, "\n")
	}
	message += "\npass --allow-saturated-host to override"
	return errors.New(message)
}

// consumersTimeout bounds how long Consumers waits on `ps` before giving up.
// A hung or slow diagnostics command must never block admission. It is a
// var, not a const, so tests can shrink it instead of actually waiting.
var consumersTimeout = 2 * time.Second

// consumerRunner executes the consumer-listing command and returns its raw
// output. It must honor ctx the way exec.CommandContext does — return once
// ctx is done, not before — so Consumers stays bounded by consumersTimeout
// regardless of implementation. Tests inject a fake that blocks on
// ctx.Done() to simulate a hang, or one that fails outright, without
// depending on a real `ps` binary or the real clock.
var consumerRunner = runConsumerCommand

func runConsumerCommand(ctx context.Context) ([]byte, error) {
	// pid,pcpu,etime,comm — never "command"/"args": the executable name only,
	// never argv, so a secret passed as a CLI flag (e.g. --token=...) can
	// never be echoed into a refusal message. See truncateConsumerName.
	return exec.CommandContext(ctx, "ps", "-Ao", "pid,pcpu,etime,comm").Output()
}

// Consumers returns up to n lines describing the busiest processes on the
// host, most CPU-hungry first, for use in a refusal message. Best-effort:
// any failure to run or parse `ps`, or a timeout, yields either an empty
// slice or a single one-line note — never an error, and never argv. It never
// blocks admission: the command is bounded by consumersTimeout regardless of
// how long a real `ps` would otherwise take.
func Consumers(n int) []string {
	if n <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), consumersTimeout)
	defer cancel()
	output, err := consumerRunner(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return []string{"(process diagnostics omitted: ps timed out)"}
		}
		return nil
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) <= 1 {
		return nil
	}
	type entry struct {
		pcpu float64
		text string
	}
	var entries []entry
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Only the first four whitespace-separated fields are ever trusted,
		// matching the exact "pid pcpu etime comm" shape requested from ps.
		// Anything beyond field 4 is discarded rather than joined back in,
		// so a hostile or misbehaving `ps` cannot smuggle argv-shaped text
		// (e.g. a secret token) past this parser into a refusal message.
		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}
		pid, pcpuField, etime, comm := fields[0], fields[1], fields[2], fields[3]
		pcpu, err := strconv.ParseFloat(pcpuField, 64)
		if err != nil {
			continue
		}
		text := fmt.Sprintf("pid=%s cpu=%s%% etime=%s comm=%s", pid, pcpuField, etime, truncateConsumerName(comm))
		entries = append(entries, entry{pcpu: pcpu, text: text})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].pcpu > entries[j].pcpu })
	if len(entries) > n {
		entries = entries[:n]
	}
	result := make([]string, len(entries))
	for i, e := range entries {
		result[i] = e.text
	}
	return result
}

const maxConsumerNameLength = 60

// truncateConsumerName reduces a comm field to its executable basename and
// bounds its length. comm is a single field (no spaces), so this can never
// reveal argv — only the process name ps itself reported.
func truncateConsumerName(comm string) string {
	name := filepath.Base(comm)
	if len(name) <= maxConsumerNameLength {
		return name
	}
	return name[:maxConsumerNameLength] + "…"
}

// expandPath expands a leading "~/" to the user's home directory, matching
// internal/recipe's config-path handling.
func expandPath(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}
