package shared

import "fmt"

// SelectJSONFormat preserves the independent --format/--json selector contract.
func SelectJSONFormat(format string, jsonOut bool) (string, error) {
	if jsonOut {
		if format != "text" && format != "json" {
			return "", fmt.Errorf("--json cannot be combined with --format=%s", format)
		}
		return "json", nil
	}
	if err := RequireOutputFormat(format, "text", "json"); err != nil {
		return "", err
	}
	return format, nil
}
