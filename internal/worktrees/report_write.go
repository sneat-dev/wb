package worktrees

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// writeLifecycleReportInjected publishes the cleanup and rename reports with
// their shared, non-durable temporary-file contract. Branch cleanup reports
// use a separate durable writer and directory sync.
func writeLifecycleReportInjected(reportDir, kind string, report any, inj *filewrite.Injector) (string, error) {
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s report directory: %w", kind, err)
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s report: %w", kind, err)
	}
	content = append(content, '\n')
	path := filepath.Join(reportDir, kind+".json")
	temporary := path + ".tmp"
	if err := filewrite.WriteFile(temporary, content, 0o644, inj); err != nil {
		return "", fmt.Errorf("write %s report: %w", kind, err)
	}
	if err := filewrite.Rename(temporary, path, inj); err != nil {
		return "", fmt.Errorf("activate %s report: %w", kind, err)
	}
	return path, nil
}
