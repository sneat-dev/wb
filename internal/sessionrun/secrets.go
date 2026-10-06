package sessionrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/secretscan"
)

func DefaultScanner() (*secretscan.Scanner, []string, error) {
	return secretscan.LoadDefault(secretscan.LoadOptions{})
}

func ScanContinuation(load func() (*secretscan.Scanner, []string, error), overrides secretscan.Overrides, segments ...secretscan.Segment) ([]secretscan.Finding, error) {
	scanner, _, err := load()
	if err != nil {
		return nil, fmt.Errorf("load secret scan rules: %w", err)
	}
	result := scanner.Scan(segments...)
	if blocking := result.Blocking(overrides); len(blocking) > 0 {
		return nil, secretscan.FormatRefusal(blocking)
	}
	return result.Warnings(overrides), nil
}
