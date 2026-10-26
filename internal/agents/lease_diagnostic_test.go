package agents

import (
	"encoding/json"
	"os"
)

// This advisory lease diagnostic has only same-package contract-test callers.
// LeaseHolder reads the current lease body without taking the lock. It is
// advisory only, for reporting.
func (store Store) LeaseHolder(worktreeDir string) (LeaseInfo, bool, error) {
	path, err := store.LeasePath(worktreeDir)
	if err != nil {
		return LeaseInfo{}, false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LeaseInfo{}, false, nil
		}
		return LeaseInfo{}, false, err
	}
	var info LeaseInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return LeaseInfo{}, true, nil
	}
	return info, true, nil
}
