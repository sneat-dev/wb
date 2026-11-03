// Package dependencycache owns the bounded Linux dependency-archive producer.
package dependencycache

import (
	"context"
	_ "embed"
	"io"

	"github.com/sneat-dev/wb/internal/process"
)

// Script is the first-party cache producer consumed by contained validation jobs.
//
//go:embed cache.py
var Script string

// Run preserves the helper's streams and failure. The helper refuses non-Linux
// or root execution before touching a cache or invoking package tools.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	child := process.CommandContext(ctx, "python3", append([]string{"-c", Script}, args...)...)
	child.Stdout, child.Stderr = stdout, stderr
	return child.Run()
}
