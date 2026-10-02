package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const pendingMetricsReceiptSchemaVersion = 1

// ExecutionLayout is the private writable state a hook may use without
// touching the repository under test or requiring authority to the whole home
// directory.
type ExecutionLayout struct {
	Root               string
	ReportRoot         string
	PendingMetricsRoot string
}

type PendingMetricsReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	RecordedAt    time.Time `json:"recorded_at"`
	TargetPath    string    `json:"target_path"`
	AppendError   string    `json:"append_error"`
	Events        []Event   `json:"events"`
}

func ResolveExecutionLayout(repoRoot, projectsRoot string) (ExecutionLayout, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return ExecutionLayout{}, err
	}
	// Runtime paths must remain stable while Git creates a linked worktree.
	// Staging hooks cannot spawn Git (the worktree lock is held), so use the
	// canonical checkout path identity here rather than the hosted-origin
	// identity used for metrics.
	slug := canonicalCheckoutSlug(repoRoot)
	segments := strings.Split(strings.Trim(slug, "/"), "/")
	pathSegments := make([]string, 0, len(segments)+2)
	pathSegments = append(pathSegments, home, "hook-runtime")
	for _, segment := range segments {
		pathSegments = append(pathSegments, sanitizeRuntimeSegment(segment))
	}
	root := filepath.Join(pathSegments...)
	return ExecutionLayout{
		Root:               root,
		ReportRoot:         filepath.Join(root, "reports"),
		PendingMetricsRoot: filepath.Join(root, "pending-metrics"),
	}, nil
}

func ensureExecutionLayout(layout ExecutionLayout) error {
	return ensureExecutionLayoutInjected(layout, nil)
}

func ensureExecutionLayoutInjected(layout ExecutionLayout, inj *filewrite.Injector) error {
	for _, path := range []string{
		layout.Root,
		layout.ReportRoot,
		layout.PendingMetricsRoot,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create hook runtime path %s: %w", path, err)
		}
		if err := filewrite.ChmodPath(path, 0o700, inj); err != nil {
			return fmt.Errorf("protect hook runtime path %s: %w", path, err)
		}
	}
	return nil
}

func recordMetrics(policy Policy, layout ExecutionLayout, events []Event, now time.Time) error {
	if len(events) == 0 {
		return nil
	}
	_, replayErr := replayPendingMetrics(policy.Metrics.Path, layout)
	appendErr := AppendEvents(policy.Metrics.Path, events)
	if appendErr == nil {
		if replayErr != nil {
			return fmt.Errorf("replay pending hook metrics: %w", replayErr)
		}
		return nil
	}
	receiptPath, pendingErr := persistPendingMetricsReceipt(layout.PendingMetricsRoot, policy.Metrics.Path, events, appendErr, now)
	if pendingErr != nil {
		if replayErr != nil {
			return fmt.Errorf("append hook metrics: %v; replay pending hook metrics: %v; persist pending hook metrics receipt: %w", appendErr, replayErr, pendingErr)
		}
		return fmt.Errorf("append hook metrics: %v; persist pending hook metrics receipt: %w", appendErr, pendingErr)
	}
	if replayErr != nil {
		return fmt.Errorf("append hook metrics: %v; replay pending hook metrics: %v; wrote replayable pending hook metrics receipt %s", appendErr, replayErr, receiptPath)
	}
	return fmt.Errorf("append hook metrics: %v; wrote replayable pending hook metrics receipt %s", appendErr, receiptPath)
}

func replayPendingMetrics(targetPath string, layout ExecutionLayout) (int, error) {
	entries, err := os.ReadDir(layout.PendingMetricsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pending hook metrics directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	replayed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(layout.PendingMetricsRoot, entry.Name())
		receipt, err := readPendingMetricsReceipt(path)
		if err != nil {
			return replayed, err
		}
		if filepath.Clean(receipt.TargetPath) != filepath.Clean(targetPath) {
			continue
		}
		if err := AppendEvents(targetPath, receipt.Events); err != nil {
			return replayed, fmt.Errorf("replay pending hook metrics %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			return replayed, fmt.Errorf("remove replayed pending hook metrics receipt %s: %w", path, err)
		}
		replayed++
	}
	return replayed, nil
}

func persistPendingMetricsReceipt(root, targetPath string, events []Event, appendErr error, now time.Time) (string, error) {
	return persistPendingMetricsReceiptInjected(root, targetPath, events, appendErr, now, nil)
}

func persistPendingMetricsReceiptInjected(root, targetPath string, events []Event, appendErr error, now time.Time, inj *filewrite.Injector) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create pending hook metrics directory: %w", err)
	}
	if err := filewrite.ChmodPath(root, 0o700, inj); err != nil {
		return "", fmt.Errorf("protect pending hook metrics directory: %w", err)
	}
	token := randomToken(8)
	path := filepath.Join(root, now.UTC().Format("20060102T150405.000000000Z")+"-"+token+".json")
	receipt := PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    now.UTC(),
		TargetPath:    targetPath,
		AppendError:   appendErr.Error(),
		Events:        append([]Event(nil), events...),
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode pending hook metrics receipt: %w", err)
	}
	data = append(data, '\n')
	if err := filewrite.WriteFile(path, data, 0o600, inj); err != nil {
		return "", fmt.Errorf("write pending hook metrics receipt %s: %w", path, err)
	}
	return path, nil
}

func readPendingMetricsReceipt(path string) (PendingMetricsReceipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PendingMetricsReceipt{}, fmt.Errorf("read pending hook metrics receipt %s: %w", path, err)
	}
	var receipt PendingMetricsReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return PendingMetricsReceipt{}, fmt.Errorf("decode pending hook metrics receipt %s: %w", path, err)
	}
	if receipt.SchemaVersion != pendingMetricsReceiptSchemaVersion {
		return PendingMetricsReceipt{}, fmt.Errorf("pending hook metrics receipt %s uses schema version %d; supported version is %d", path, receipt.SchemaVersion, pendingMetricsReceiptSchemaVersion)
	}
	return receipt, nil
}

func sanitizeRuntimeSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, character := range segment {
		switch {
		case character >= 'a' && character <= 'z':
			builder.WriteRune(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character)
		case character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == '.', character == '_', character == '-':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func randomToken(bytes int) string {
	buffer := make([]byte, bytes)
	// Go1.27 crypto/rand.Read fills the buffer or terminates on entropy failure.
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
