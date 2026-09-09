// Package hostload refuses to admit more CPU-heavy WB work when the host is
// already saturated. Two orphaned fixture processes ran at 100% CPU for
// nearly seven days (2026-08-31, PIDs 43481/43483), driving load average to
// 7-12 on a shared machine; WB's merge-gate shards then failed unrelated
// candidates because everything on the box was starved for CPU. This
// package gives `wb run` and `wb worktree merge`/`prepare`/`resume` a floor
// below which they refuse new CPU-heavy work instead of piling onto an
// already-overloaded host.
//
// The floor is disabled entirely — every check admits, and Resolve/Floor
// report a floor of 0 with a reason — in three cases, evaluated in this
// order:
//
//  1. WB_ADMISSION_LOAD_FLOOR is set to a positive number: that number is
//     the floor, and it wins even inside CI. This lets the dedicated
//     host-load tests force real gating behavior no matter where they run.
//  2. WB_ADMISSION_LOAD_FLOOR is set to "0" (or any non-positive number):
//     disabled, reason "env". Env always overrides wb.yaml.
//  3. Otherwise, CI is declared (CI=true or GITHUB_ACTIONS=true): disabled,
//     reason "ci". GitHub's shared runners routinely report a load average
//     of 8-10 on 4 vCPUs, so a fixed floor would refuse genuine `wb run` /
//     `wb worktree merge` work inside CI workflows, not just protect a
//     shared developer machine.
//  4. Otherwise, wb.yaml sets admission.load_floor explicitly to 0:
//     disabled, reason "config".
//
// Absent all of the above, the floor is wb.yaml's admission.load_floor when
// positive, else the default of 2*runtime.NumCPU() — a 4-core developer Mac
// refuses new CPU-heavy work above a load average of 8.
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

// EnvLoadFloor overrides the resolved admission floor. A positive value sets
// the floor directly, taking priority over both wb.yaml and CI detection. A
// value of "0" (or any non-positive number) disables admission entirely.
// Unset defers to CI detection, then wb.yaml. See the package doc for the
// full resolution order.
const EnvLoadFloor = "WB_ADMISSION_LOAD_FLOOR"

// config is the subset of wb.yaml this package understands.
type config struct {
	Admission struct {
		LoadFloor *float64 `yaml:"load_floor"`
	} `yaml:"admission"`
}

// runningInCI reports whether the environment declares itself a CI runner.
func runningInCI() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CI")), "true") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("GITHUB_ACTIONS")), "true")
}

// defaultFloor is the admission ceiling when nothing overrides it: twice the
// host's CPU count, never less than 1.
func defaultFloor() float64 {
	def := 2 * float64(runtime.NumCPU())
	if def < 1 {
		def = 1
	}
	return def
}

// Resolve computes the admission floor and, when admission is disabled,
// names why: "env" (WB_ADMISSION_LOAD_FLOOR is 0 or negative), "ci"
// (CI/GITHUB_ACTIONS declared), or "config" (wb.yaml admission.load_floor:
// 0 explicitly). An empty reason means admission is active and floor is the
// ceiling Check should refuse above. See the package doc for resolution
// order — notably, a positive WB_ADMISSION_LOAD_FLOOR wins even inside CI.
// configPath "" resolves wbconfig.DefaultPath(). A missing or unparsable
// config is not an error here — it just keeps the default.
func Resolve(configPath string) (floor float64, reason string) {
	if raw, ok := os.LookupEnv(EnvLoadFloor); ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			if v > 0 {
				return v, ""
			}
			return 0, "env"
		}
	}
	if runningInCI() {
		return 0, "ci"
	}
	def := defaultFloor()
	path := strings.TrimSpace(configPath)
	if path == "" {
		path = wbconfig.DefaultPath()
	}
	raw, err := os.ReadFile(expandPath(path))
	if err != nil {
		return def, ""
	}
	var cfg config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return def, ""
	}
	if cfg.Admission.LoadFloor != nil {
		if *cfg.Admission.LoadFloor == 0 {
			return 0, "config"
		}
		if *cfg.Admission.LoadFloor > 0 {
			return *cfg.Admission.LoadFloor, ""
		}
	}
	return def, ""
}

// Floor resolves the load-average ceiling above which new CPU-heavy work is
// refused, discarding the disablement reason. Use Resolve when the reason
// needs to be recorded (e.g. on a receipt or runlog event).
func Floor(configPath string) float64 {
	floor, _ := Resolve(configPath)
	return floor
}

// Disabled reports whether host-load admission is turned off entirely, and
// why. See Resolve for the reason values.
func Disabled(configPath string) (bool, string) {
	floor, reason := Resolve(configPath)
	return floor <= 0, reason
}

// Check refuses admission when the host's 1-minute load average exceeds
// floor, unless allow is true (the caller passed --allow-saturated-host).
// floor <= 0 means admission is disabled (see Resolve/Disabled) and Check
// always admits, without even reading the load. read is normally nil, which
// selects System; tests inject a fake Reader. A Reader error (including
// ErrUnsupported) never blocks admission — an unreadable or unsupported
// load source fails open.
func Check(read Reader, floor float64, allow bool) error {
	if allow {
		return nil
	}
	if floor <= 0 {
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
