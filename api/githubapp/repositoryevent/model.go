// Package repositoryevent defines the public, privacy-safe contract used by
// the Workbench GitHub App to notify enrolled WB daemons about repository
// changes.
package repositoryevent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	ContractVersion = 1
	EventsPath      = "/v0/workbench/repository-events"
	AckPath         = "/v0/workbench/repository-events/ack"

	DefaultLimit       = 50
	MaxLimit           = 100
	DefaultWaitSeconds = 25
	MaxWaitSeconds     = 30
	MaxCursorBytes     = 1024
	MaxEventIDBytes    = 256
	MaxRepositoryBytes = 256
	MaxRefBytes        = 512
)

type Reason string

const (
	ReasonDefaultBranchUpdated Reason = "default_branch_updated"
	ReasonRepositoryRenamed    Reason = "repository_renamed"
)

var (
	safeRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeEventID        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	hexSHA             = regexp.MustCompile(`^[0-9a-fA-F]+$`)

	ErrInvalidEvent  = errors.New("invalid repository event")
	ErrInvalidCursor = errors.New("invalid repository event cursor")
)

// Event is the complete allowlist of GitHub metadata delivered to a WB
// daemon. It intentionally excludes webhook payloads, installation IDs,
// credentials, paths, actor identities, commit messages, and diagnostics.
type Event struct {
	Version            int        `json:"version" firestore:"version"`
	ID                 string     `json:"id" firestore:"id"`
	Repository         string     `json:"repository" firestore:"repository"`
	PreviousRepository string     `json:"previous_repository,omitempty" firestore:"previous_repository,omitempty"`
	Ref                string     `json:"ref" firestore:"ref"`
	Reason             Reason     `json:"reason" firestore:"reason"`
	TargetSHA          string     `json:"target_sha,omitempty" firestore:"target_sha,omitempty"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty" firestore:"occurred_at,omitempty"`
}

func (event Event) Validate() error {
	if event.Version != ContractVersion {
		return fmt.Errorf("%w: version must be %d", ErrInvalidEvent, ContractVersion)
	}
	if err := ValidateEventID(event.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if err := ValidateRepository(event.Repository); err != nil {
		return fmt.Errorf("%w: repository: %v", ErrInvalidEvent, err)
	}
	if err := validateRef(event.Ref); err != nil {
		return fmt.Errorf("%w: ref: %v", ErrInvalidEvent, err)
	}
	if event.TargetSHA != "" && (len(event.TargetSHA) != 40 && len(event.TargetSHA) != 64 || !hexSHA.MatchString(event.TargetSHA)) {
		return fmt.Errorf("%w: target_sha must be a 40- or 64-character hexadecimal object ID", ErrInvalidEvent)
	}
	if event.OccurredAt != nil && event.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at must be non-zero when present", ErrInvalidEvent)
	}
	switch event.Reason {
	case ReasonDefaultBranchUpdated:
		if event.PreviousRepository != "" {
			return fmt.Errorf("%w: previous_repository is only valid for repository_renamed", ErrInvalidEvent)
		}
	case ReasonRepositoryRenamed:
		if err := ValidateRepository(event.PreviousRepository); err != nil {
			return fmt.Errorf("%w: previous_repository: %v", ErrInvalidEvent, err)
		}
		if strings.EqualFold(event.PreviousRepository, event.Repository) {
			return fmt.Errorf("%w: repository rename must change identity", ErrInvalidEvent)
		}
	default:
		return fmt.Errorf("%w: unsupported reason %q", ErrInvalidEvent, event.Reason)
	}
	return nil
}

func ValidateEventID(id string) error {
	if len(id) == 0 || len(id) > MaxEventIDBytes || !safeEventID.MatchString(id) {
		return fmt.Errorf("id must be 1-%d opaque safe bytes", MaxEventIDBytes)
	}
	return nil
}

func ValidateRepository(repository string) error {
	if len(repository) == 0 || len(repository) > MaxRepositoryBytes || !strings.HasPrefix(repository, "github.com/") {
		return errors.New("must be a bounded github.com/owner/repository identity")
	}
	parts := strings.Split(strings.TrimPrefix(repository, "github.com/"), "/")
	if len(parts) != 2 || !safeRepositoryPart.MatchString(parts[0]) || !safeRepositoryPart.MatchString(parts[1]) {
		return errors.New("must be a bounded github.com/owner/repository identity")
	}
	return nil
}

func validateRef(ref string) error {
	if len(ref) <= len("refs/heads/") || len(ref) > MaxRefBytes || !strings.HasPrefix(ref, "refs/heads/") {
		return errors.New("must be a bounded full branch ref")
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\x00\r\n") {
		return errors.New("must be a valid full branch ref")
	}
	return nil
}

func ValidateCursor(cursor string, required bool) error {
	if required && cursor == "" {
		return fmt.Errorf("%w: cursor is required", ErrInvalidCursor)
	}
	if len(cursor) > MaxCursorBytes || strings.ContainsAny(cursor, "\x00\r\n") {
		return fmt.Errorf("%w: cursor exceeds %d safe bytes", ErrInvalidCursor, MaxCursorBytes)
	}
	return nil
}

type PollResponse struct {
	Version    int     `json:"version"`
	Cursor     string  `json:"cursor"`
	NextCursor string  `json:"next_cursor"`
	Events     []Event `json:"events"`
}

func (response PollResponse) Validate(requestCursor string) error {
	if response.Version != ContractVersion {
		return fmt.Errorf("repository event response version must be %d", ContractVersion)
	}
	if response.Cursor != requestCursor {
		return errors.New("repository event response cursor does not match the request")
	}
	if err := ValidateCursor(response.NextCursor, true); err != nil {
		return err
	}
	if len(response.Events) > MaxLimit {
		return fmt.Errorf("repository event response exceeds %d events", MaxLimit)
	}
	seen := make(map[string]bool, len(response.Events))
	for index, event := range response.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("events[%d]: %w", index, err)
		}
		if seen[event.ID] {
			return fmt.Errorf("events[%d]: duplicate event id %q", index, event.ID)
		}
		seen[event.ID] = true
	}
	return nil
}

type AckRequest struct {
	Version  int      `json:"version"`
	Cursor   string   `json:"cursor"`
	EventIDs []string `json:"event_ids"`
}

func (request AckRequest) Validate() error {
	if request.Version != ContractVersion {
		return fmt.Errorf("repository event acknowledgement version must be %d", ContractVersion)
	}
	if err := ValidateCursor(request.Cursor, true); err != nil {
		return err
	}
	if len(request.EventIDs) == 0 || len(request.EventIDs) > MaxLimit {
		return fmt.Errorf("repository event acknowledgement requires 1-%d event_ids", MaxLimit)
	}
	seen := make(map[string]bool, len(request.EventIDs))
	for _, id := range request.EventIDs {
		if ValidateEventID(id) != nil || seen[id] {
			return errors.New("repository event acknowledgement contains an invalid or duplicate event id")
		}
		seen[id] = true
	}
	return nil
}

type AckResponse struct {
	Version int    `json:"version"`
	Cursor  string `json:"cursor"`
}
