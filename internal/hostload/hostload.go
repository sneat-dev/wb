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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

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

// Consumers returns up to n lines describing the busiest processes on the
// host, most CPU-hungry first, for use in a refusal message. Best-effort:
// any failure to run or parse `ps` yields an empty slice, never an error.
func Consumers(n int) []string {
	if n <= 0 {
		return nil
	}
	output, err := exec.Command("ps", "-Ao", "pid,pcpu,etime,command").Output()
	if err != nil {
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
		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}
		pcpu, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		entries = append(entries, entry{pcpu: pcpu, text: truncateConsumer(trimmed)})
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

const maxConsumerLineLength = 120

func truncateConsumer(line string) string {
	if len(line) <= maxConsumerLineLength {
		return line
	}
	return line[:maxConsumerLineLength] + "…"
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
