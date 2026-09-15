package taskoffload

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DirName         = "parked-tasks"
	schemaVersion   = 1
	maxContextBytes = 1 << 20
	recordFileName  = "task.json"
	contextFileName = "context.md"
)

type Status string

const (
	StatusParked  Status = "parked"
	StatusOffload Status = "offloaded"
)

type Record struct {
	SchemaVersion int       `json:"schema_version"`
	TaskID        string    `json:"task_id"`
	Task          string    `json:"task"`
	WorktreeDir   string    `json:"worktree_dir"`
	Repository    string    `json:"repository"`
	Harness       string    `json:"harness,omitempty"`
	Model         string    `json:"model,omitempty"`
	Target        string    `json:"target,omitempty"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type Store struct {
	Root string
}

func NewStore(root string) Store {
	return Store{Root: root}
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate parked task ID: %w", err)
	}
	return "task-" + hex.EncodeToString(raw[:]), nil
}

func (s Store) Save(record Record, contextBody string) error {
	if err := validate(record, contextBody); err != nil {
		return err
	}
	dir := filepath.Join(s.Root, record.TaskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	contextPath := filepath.Join(dir, contextFileName)
	if err := os.WriteFile(contextPath, []byte(contextBody), 0o600); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordFileName), append(raw, '\n'), 0o600)
}

func (s Store) Load(id string) (Record, string, error) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "task-") {
		return Record{}, "", fmt.Errorf("invalid parked task ID")
	}
	dir := filepath.Join(s.Root, id)
	raw, err := os.ReadFile(filepath.Join(dir, recordFileName))
	if err != nil {
		return Record{}, "", err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, "", err
	}
	body, err := os.ReadFile(filepath.Join(dir, contextFileName))
	if err != nil {
		return Record{}, "", err
	}
	if err := validate(record, string(body)); err != nil {
		return Record{}, "", err
	}
	return record, string(body), nil
}

func validate(record Record, contextBody string) error {
	if record.SchemaVersion != schemaVersion {
		return fmt.Errorf("parked task schema_version %d unsupported; want %d", record.SchemaVersion, schemaVersion)
	}
	if !strings.HasPrefix(record.TaskID, "task-") || strings.TrimSpace(record.Task) == "" || strings.TrimSpace(record.WorktreeDir) == "" {
		return fmt.Errorf("parked task record is incomplete")
	}
	if len(contextBody) == 0 || len(contextBody) > maxContextBytes {
		return fmt.Errorf("parked task continuation must be between 1 and %d bytes", maxContextBytes)
	}
	return nil
}
