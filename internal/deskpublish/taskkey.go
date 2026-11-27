// Package deskpublish turns this machine's Cockpit export into the snapshot
// it publishes to Sneat Dev Desk (spec/features/desk-connect), applying the
// machine's detail level before anything leaves the machine.
package deskpublish

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/credentialfile"
)

// TaskKeyBytes is the length of a task key.
const TaskKeyBytes = 32

// TaskKeyFile is the task key's file name under the credentials directory.
const TaskKeyFile = "desk-task-key"

// TaskKey is the per-person secret under which task names and the entry ids
// derived from them are hashed at `repositories` detail
// (desk-connect#req:task-keys-correlate-without-names). Desk never holds it.
type TaskKey struct{ secret []byte }

// ErrTaskKeyMismatch is returned when a different task key is already
// installed: a person's machines must agree on one, so it is never replaced.
var ErrTaskKeyMismatch = errors.New("this machine already has a different Desk task key")

// ErrInvalidTaskKey is returned for a key that is not TaskKeyBytes of
// base64url text.
var ErrInvalidTaskKey = errors.New("desk task key must be 32 bytes of base64url text")

// ParseTaskKey decodes the text form of a key.
func ParseTaskKey(text string) (TaskKey, error) {
	secret, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || len(secret) != TaskKeyBytes {
		return TaskKey{}, ErrInvalidTaskKey
	}
	return TaskKey{secret: secret}, nil
}

// NewTaskKey draws a fresh key from random.
func NewTaskKey(random io.Reader) (TaskKey, error) {
	secret := make([]byte, TaskKeyBytes)
	if _, err := io.ReadFull(random, secret); err != nil {
		return TaskKey{}, fmt.Errorf("generate desk task key: %w", err)
	}
	return TaskKey{secret: secret}, nil
}

// Text is the key's storage and transfer form.
func (key TaskKey) Text() string { return base64.RawURLEncoding.EncodeToString(key.secret) }

// ID is the public fingerprint Desk may hold: the first 8 bytes of the key's
// SHA-256, hex. It lets Desk say that two machines use different keys.
func (key TaskKey) ID() string {
	sum := sha256.Sum256(key.secret)
	return hex.EncodeToString(sum[:8])
}

func (key TaskKey) mac(domain, value string) []byte {
	mac := hmac.New(sha256.New, key.secret)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

// Of is the task key of a task name: the first 16 bytes of its HMAC-SHA256,
// base64url. An empty name has no key.
func (key TaskKey) Of(task string) string {
	if task == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(key.mac("task", task)[:16])
}

// entry re-keys an entry id whose unkeyed hash covers a task or branch name,
// keeping its kind prefix, so Desk cannot confirm a guessed name by hashing it.
func (key TaskKey) entry(kind, id string) string {
	return kind + "-k" + hex.EncodeToString(key.mac("entry", id)[:10])
}

// LoadOrCreateTaskKey reads the key at path, refusing a file other users can
// read, or creates one from random when none exists.
func LoadOrCreateTaskKey(path string, random io.Reader) (key TaskKey, created bool, err error) {
	if info, statErr := os.Stat(path); statErr == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return TaskKey{}, false, fmt.Errorf("desk task key %s must not be readable by other users", path)
		}
		text, readErr := os.ReadFile(path)
		if readErr != nil {
			return TaskKey{}, false, fmt.Errorf("read desk task key: %w", readErr)
		}
		token, tokenErr := credentialfile.ReadToken(bytes.NewReader(text))
		if tokenErr != nil {
			return TaskKey{}, false, ErrInvalidTaskKey
		}
		key, err = ParseTaskKey(token)
		return key, false, err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return TaskKey{}, false, fmt.Errorf("inspect desk task key: %w", statErr)
	}
	key, err = NewTaskKey(random)
	if err != nil {
		return TaskKey{}, false, err
	}
	if err := InstallTaskKey(path, key); err != nil {
		return TaskKey{}, false, err
	}
	return key, true, nil
}

// InstallTaskKey writes key to path with mode 0600. It refuses to replace a
// different existing key with ErrTaskKeyMismatch.
func InstallTaskKey(path string, key TaskKey) error {
	if existing, err := os.ReadFile(path); err == nil {
		if token, tokenErr := credentialfile.ReadToken(bytes.NewReader(existing)); tokenErr != nil || token != key.Text() {
			return ErrTaskKeyMismatch
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if _, err := credentialfile.WritePrivate(path, key.Text()); err != nil {
		return fmt.Errorf("install desk task key: %w", err)
	}
	return nil
}
