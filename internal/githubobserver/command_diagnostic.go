package githubobserver

import "strings"

func CommandDiagnostic(response CommandResponse) string {
	value := strings.TrimSpace(string(response.Stderr))
	if value == "" {
		value = strings.TrimSpace(string(response.Stdout))
	}
	if value == "" && response.Err != nil {
		value = response.Err.Error()
	}
	return value
}
