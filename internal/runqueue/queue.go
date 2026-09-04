// Package runqueue coordinates CPU-heavy commands across WB processes.
package runqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const retryInterval = 100 * time.Millisecond

// Budget leaves one logical CPU available for the harness and operating
// system. Even a single-core machine retains one execution slot.
func Budget() int {
	if runtime.NumCPU() <= 1 {
		return 1
	}
	return runtime.NumCPU() - 1
}

// Units classifies the CPU share required by argv within budget.
func Units(argv []string, budget int) int {
	if budget < 1 || len(argv) == 0 {
		return 0
	}
	tool := strings.ToLower(filepath.Base(argv[0]))
	arguments := argv[1:]
	clamp := func(value int) int {
		if value > budget {
			return budget
		}
		return value
	}
	switch tool {
	case "go":
		if !hasAny(arguments, "test", "vet", "build") {
			return 0
		}
		if hasPrefix(arguments, "-race") || hasPrefix(arguments, "-cover") {
			return budget
		}
		if hasBroadScope(arguments) {
			return clamp(2)
		}
		return 1
	case "golangci-lint", "staticcheck", "pytest", "vitest", "jest", "mocha":
		return 1
	case "nx":
		if hasAny(arguments, "build", "run-many", "affected") {
			return clamp(2)
		}
		if hasAny(arguments, "test", "lint", "e2e") {
			return 1
		}
	case "npm", "pnpm", "yarn", "bun", "npx":
		joined := strings.ToLower(strings.Join(arguments, " "))
		if strings.Contains(joined, "build") || strings.Contains(joined, "e2e") || strings.Contains(joined, "affected") {
			return clamp(2)
		}
		if strings.Contains(joined, "test") || strings.Contains(joined, "lint") {
			return 1
		}
	case "cargo":
		if hasAny(arguments, "test", "build", "check", "clippy") {
			return clamp(2)
		}
	}
	return 0
}

func hasBroadScope(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "..." || argument == "./..." || strings.HasSuffix(argument, "/...") {
			return true
		}
	}
	return false
}

// Lease holds units machine-wide until Release. Slot files live below the
// projects root so harnesses already permitted to write repositories can join
// the same budget without requiring access to the user's home directory.
type Lease struct {
	files []*os.File
}

func (lease *Lease) Release() {
	for index := len(lease.files) - 1; index >= 0; index-- {
		_ = unix.Flock(int(lease.files[index].Fd()), unix.LOCK_UN)
		_ = lease.files[index].Close()
	}
	lease.files = nil
}

// Acquire waits for units from one projects-root budget. Each attempt either
// acquires every requested slot or releases all partial locks before waiting,
// preventing two multi-unit commands from deadlocking one another.
func Acquire(ctx context.Context, projectsRoot string, units, budget int) (*Lease, time.Duration, error) {
	if units <= 0 {
		return &Lease{}, 0, nil
	}
	if budget < 1 {
		budget = 1
	}
	if units > budget {
		units = budget
	}
	directory := filepath.Join(projectsRoot, ".wb", "runtime", "cpu")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, 0, fmt.Errorf("create WB CPU lease directory: %w", err)
	}
	started := time.Now()
	for {
		lease := &Lease{}
		for slot := 0; slot < budget && len(lease.files) < units; slot++ {
			path := filepath.Join(directory, fmt.Sprintf("slot-%02d.lock", slot))
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				lease.Release()
				return nil, time.Since(started), fmt.Errorf("open WB CPU slot: %w", err)
			}
			if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				_ = file.Close()
				continue
			}
			lease.files = append(lease.files, file)
		}
		if len(lease.files) == units {
			return lease, time.Since(started), nil
		}
		lease.Release()
		select {
		case <-ctx.Done():
			return nil, time.Since(started), ctx.Err()
		case <-time.After(retryInterval):
		}
	}
}

func hasAny(arguments []string, targets ...string) bool {
	for _, argument := range arguments {
		for _, target := range targets {
			if argument == target {
				return true
			}
		}
	}
	return false
}

func hasPrefix(arguments []string, prefix string) bool {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return true
		}
	}
	return false
}
